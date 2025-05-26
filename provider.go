package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/Mattilsynet/h8s-provider/bindings/mattilsynet/h8s_provider/receiver"
	"github.com/Mattilsynet/h8s-provider/bindings/mattilsynet/h8s_provider/request_reply"
	"github.com/Mattilsynet/h8s-provider/bindings/mattilsynet/h8s_provider/types"
	"github.com/Mattilsynet/h8s/pkg/subjectmapper"
	"github.com/nats-io/nats.go"
	"go.wasmcloud.dev/provider"
	wrpc "wrpc.io/go"
)

var (
	validImportInterfaces = []string{"request-reply", "receiver"}
	validExportInterfaces = []string{"sender"}
)

const (
	errInvalidSourceConfigProperties = "host, method or path property is empty, check your wadm"
)

type H8SHandler struct {
	// The provider instance
	provider *provider.WasmcloudProvider
	// All components linked to this provider and their config.
	linkedFrom map[string]map[string]string
	// All components this provider is linked to and their config
	linkedTo            map[string]map[string]string
	natsConnByComponent map[string]*nats.Conn
	subsByComponent     map[string]string

	// Websockets Incoming
	subjectByReceiverTarget map[string]string // [subject][target.name] = target
	// Websockets Outgoing
	subjectBySenderComponent    map[string]string    // [subject][target.name] = target
	websocketConnections        map[string]*nats.Msg // [subject][connection] = NATS message
	websocketConnectionTracking bool
}

func NewH8SHandler() *H8SHandler {
	handler := &H8SHandler{
		linkedFrom: make(map[string]map[string]string),
		linkedTo:   make(map[string]map[string]string),

		// Holds NATS connection for components that might use
		// diffrent credentials and different NATS infrastructure.
		natsConnByComponent: make(map[string]*nats.Conn),

		// Map that holds subscribe filters for a component.
		subsByComponent:             make(map[string]string),
		subjectByReceiverTarget:     make(map[string]string),
		subjectBySenderComponent:    make(map[string]string),
		websocketConnections:        make(map[string]*nats.Msg),
		websocketConnectionTracking: false,
	}
	return handler
}

func (h8s *H8SHandler) AddTargetLinkComponent(link provider.InterfaceLinkDefinition) error {
	h8s.provider.Logger.Info("Handling new target link", "link", link)

	ok, valid, invalid := validateInterfaces(validExportInterfaces, link.Interfaces)
	if !ok {
		h8s.provider.Logger.Error(
			"invalid target link",
			"error",
			"target link contains no valid interfaces for this provider",
			"valid interfaces", validImportInterfaces,
			"invalid values", invalid,
		)
	}

	for _, item := range valid {
		switch item {
		case "sender":
			h8s.addSenderComponent(link)
		default:
			h8s.provider.Logger.Warn(
				"unexpected valid interface",
				"warning", "target link contained valid but unhandled interface",
				"interface", item,
			)
		}
	}

	return nil
}

func (h8s *H8SHandler) AddSourceLinkComponent(link provider.InterfaceLinkDefinition) error {
	h8s.provider.Logger.Info("Handling new source link", "link", link)

	ok, valid, invalid := validateInterfaces(validImportInterfaces, link.Interfaces)
	if !ok {
		h8s.provider.Logger.Error(
			"invalid source link",
			"error", "source link contains no valid interfaces for this provider",
			"valid interfaces", validExportInterfaces,
			"invalid values", invalid,
		)
		return errors.New("invalid source link, link contained no valid interfaces")
	}

	for _, item := range valid {
		switch item {
		case "request-reply":
			h8s.addRequestReplyComponent(link)
		case "receiver":
			h8s.addReceiverComponent(link)
		default:
			h8s.provider.Logger.Warn(
				"unexpected valid interface",
				"warning", "source link contained valid but unhandled interface",
				"interface", item,
			)
		}
	}

	return nil
}

func (h8s *H8SHandler) addReceiverComponent(link provider.InterfaceLinkDefinition) error {
	if len(link.SourceConfig["host"]) == 0 ||
		len(link.SourceConfig["path"]) == 0 ||
		len(link.SourceConfig["method"]) == 0 {
		h8s.provider.Logger.Error("invalid SourceConfig", "error", errInvalidSourceConfigProperties)
		return errors.New(errInvalidSourceConfigProperties)
	}

	url := fmt.Sprintf(
		"%s://%s%s",
		link.SourceConfig["method"],
		link.SourceConfig["host"],
		link.SourceConfig["path"])

	mapper := subjectmapper.NewWebSocketMap(url)
	h8s.subjectByReceiverTarget[link.Target] = mapper.WebSocketPublishSubject()
	h8s.provider.Logger.Info(
		"addReceiverComponent, found link adding component",
		"link", link,
	)
	h8s.runReceiverHandler(link.Target)
	return nil
}

func (h8s *H8SHandler) addRequestReplyComponent(link provider.InterfaceLinkDefinition) error {
	// Handle sourceConfig for target component
	if len(link.SourceConfig["host"]) == 0 ||
		len(link.SourceConfig["path"]) == 0 ||
		len(link.SourceConfig["method"]) == 0 {

		h8s.provider.Logger.Error("invalid SourceConfig", "error", errInvalidSourceConfigProperties)
		return errors.New(errInvalidSourceConfigProperties)
	}

	// Use module from h8s to create a nats subscribe subject filter.
	// TODO: the ergonomics of this module should be improved.
	req := &http.Request{
		Host:   link.SourceConfig["host"],
		Method: link.SourceConfig["method"],
		URL: &url.URL{
			Scheme: "http",
			Host:   link.SourceConfig["host"],
			Path:   link.SourceConfig["path"],
		},
	}
	subject := subjectmapper.NewSubjectMap(req)
	fmt.Println("PublishSubject:", subject.PublishSubject())

	h8s.subsByComponent[link.Target] = subject.PublishSubject()
	h8s.provider.Logger.Debug("subscribed to subjects", "subject", h8s.subsByComponent[link.Target])
	h8s.runRequestReplyHandler(link.Target)
	return nil
}

// addSenderComponent builds up the list of components keyed by their h8s conventional
// publish subjects. We will use this for building a map and track actual connections from h8s.
func (h8s *H8SHandler) addSenderComponent(link provider.InterfaceLinkDefinition) error {
	if len(link.SourceConfig["host"]) == 0 ||
		len(link.SourceConfig["path"]) == 0 ||
		len(link.SourceConfig["method"]) == 0 {

		h8s.provider.Logger.Error(
			"invalid SourceConfig",
			"error", errInvalidSourceConfigProperties)
		return errors.New(errInvalidSourceConfigProperties)
	}

	url := fmt.Sprintf(
		"%s://%s%s",
		link.SourceConfig["method"],
		link.SourceConfig["host"],
		link.SourceConfig["path"])

	mapper := subjectmapper.NewWebSocketMap(url)
	h8s.subjectBySenderComponent[link.Target] = mapper.WebSocketPublishSubject()
	h8s.provider.Logger.Info(
		"addSenderComponent: adding sender component",
		"subject", mapper.WebSocketPublishSubject())
	h8s.runSenderHandler()
	return nil
}

func (h8s *H8SHandler) GetConnections(context.Context) (*wrpc.Result[[]*types.Msg, string], error) {
	var connections []*types.Msg

	for _, msg := range h8s.websocketConnections {
		connections = append(connections, natsMsgToWasmMsg(msg))
	}
	return &wrpc.Result[[]*types.Msg, string]{&connections, nil}, nil
}

func (h8s *H8SHandler) GetConnectionsBySubject(context context.Context, subject string) (*wrpc.Result[[]*types.Msg, string], error) {
	var connections []*types.Msg

	for _, msg := range h8s.websocketConnections {
		for _, subject := range h8s.subjectBySenderComponent {
			if subject != msg.Subject {
				continue
			}
			connections = append(connections, natsMsgToWasmMsg(msg))
		}
	}
	return &wrpc.Result[[]*types.Msg, string]{&connections, nil}, nil
}

func (h8s *H8SHandler) Send(ctx context.Context, conn string, payload []byte) (*wrpc.Result[string, string], error) {
	connInfo, ok := h8s.websocketConnections[conn]
	if !ok {
		h8s.provider.Logger.Error(
			"no websocket connection found",
			"conn", conn,
		)
		return nil, fmt.Errorf("connection %s not found ", conn)
	}

	if len(connInfo.Reply) == 0 {
		h8s.provider.Logger.Error(
			"reply subject is empty",
			"conn", conn,
		)
		return nil, errors.New("reply subject is empty")
	}

	nc, err := nats.Connect("nats://localhost:4222", nats.Name("h8s-provider-send-method"))
	if err != nil {
		h8s.provider.Logger.Error("unable to connect to NATS", "error", err)
		return nil, err
	}
	defer nc.Close()

	if err := nc.Publish(connInfo.Reply, payload); err != nil {
		h8s.provider.Logger.Error("failed to publish payload", "error", err, "reply", connInfo.Reply)
		return nil, err
	}
	okstring := "bytes sent"
	return &wrpc.Result[string, string]{Ok: &okstring}, nil
}

func (h8s *H8SHandler) RemoveComponent(link provider.InterfaceLinkDefinition) error {
	h8s.provider.Logger.Info("handling target link delete", "link", link.Name)

	return nil
}

// runSenderHandler will subscribe to nats and listen for websocket connections and build up
// the internal h8s-provider state.
func (h8s *H8SHandler) runSenderHandler() error {
	if h8s.websocketConnectionTracking {
		// We only need to run one, we can return.
		return nil
	}
	// Toggle websocketConnectionTracking on
	h8s.websocketConnectionTracking = true

	nc, err := nats.Connect(
		"nats://localhost:4222",
		nats.Name("h8s-provider-connection-tracker"))
	if err != nil {
		return errors.New("failed to connect to NATS server")
	}

	controlSubject := "h8s.control.ws.>"

	_, subErr := nc.Subscribe(controlSubject, func(msg *nats.Msg) {
		switch msg.Subject {
		case "h8s.control.ws.conn.established":
			for _, subject := range h8s.subjectBySenderComponent {
				if msg.Header.Get("X-H8S-PublishSubject") != subject {
					continue
				}
				h8s.websocketConnections[msg.Reply] = msg
				h8s.websocketConnections[msg.Reply].Subject = msg.Header.Get("X-H8S-PublishSubject")
				h8s.provider.Logger.Info(
					"tracking new websocket connection",
					"connection", msg.Reply,
				)
			}
		case "h8s.control.ws.conn.closed":
			delete(h8s.websocketConnections, msg.Reply)
			h8s.provider.Logger.Info(
				"removed closed websocket connection",
				"connection", msg.Reply,
			)
		}
	})
	if subErr != nil {
		h8s.provider.Logger.Error(
			"failed to subscribe to h8s control subject",
			"subject", controlSubject,
			"error", err,
		)
		return err
	}
	return nil
}

func (h8s *H8SHandler) runReceiverHandler(target string) error {
	nc, err := nats.Connect(
		"nats://localhost:4222",
		nats.Name("h8s-provider-receiver"))
	if err != nil {
		return errors.New("failed to connect to NATS server")
	}

	subject := h8s.subjectByReceiverTarget[target]
	h8s.provider.Logger.Info(
		"runReceiverHandler: subscribing for incoming websocket data",
		"subject", subject,
	)
	_, subErr := nc.Subscribe(subject, func(msg *nats.Msg) {
		wrpcclient := h8s.provider.OutgoingRpcClient(target)
		newMsg := natsMsgToWasmMsg(msg)
		result, wrpcErr := receiver.HandleMessage(context.Background(), wrpcclient, newMsg)
		if wrpcErr != nil {
			h8s.provider.Logger.Error(
				"runReceiverHandler: fail to invoke receiver component",
				"error", wrpcErr)
		}
		if result.Err != nil {
			h8s.provider.Logger.Error(
				"runReceiverHandler: component returned error",
				"error", result.Err)
		}
	})
	if subErr != nil {
		h8s.provider.Logger.Error("failed to subscribe to subject", "subject", subject, "error", err)
		return err
	}
	return nil
}

func (h8s *H8SHandler) runRequestReplyHandler(target string) error {
	// Get the NATS connection for the target component
	/*
		nc, ok := h8s.natsConnByComponent[target]

		if !ok {
			h8s.provider.Logger.Error("no NATS connection found for target", "target", target)
			return errors.New("no NATS connection found for target")
		}
	*/
	nc, err := nats.Connect("nats://localhost:4222", nats.Name("h8s-provider-request-reply"))
	if err != nil {
		return errors.New("failed to connect to NATS server")
	}

	subject := h8s.subsByComponent[target]

	// This is the "server": Subscribe to publish subject for request going through h8s.
	_, subErr := nc.Subscribe(subject, func(msg *nats.Msg) {
		h8s.provider.Logger.Info("Received message", "subject", msg.Subject, "data", string(msg.Data))
		wrpcclient := h8s.provider.OutgoingRpcClient(target)

		newMsg := natsMsgToWasmMsg(msg)

		mapOfInboxesVsComponents := make(map[string]string)
		mapOfInboxesVsComponents[target] = msg.Reply
		result, hmErr := request_reply.HandleMessage(context.Background(), wrpcclient, newMsg)
		if hmErr != nil {
			h8s.provider.Logger.Error("failed to handle message", "error", err)
			return

		}
		if result.Err != nil {
			if errPub := nc.Publish(msg.Reply, []byte("something went wrong")); errPub != nil {
				h8s.provider.Logger.Error("failed to handle message", "error", result.Err)
				return
			}
			return
		}

		replyHeaders := make(map[string][]string)
		for _, kv := range result.Ok.Headers {
			replyHeaders[kv.Key] = kv.Value
		}
		replyMsg := &nats.Msg{
			Subject: result.Ok.Reply,
			Data:    result.Ok.Data,
			Header:  replyHeaders,
		}

		if pubErr := nc.PublishMsg(replyMsg); pubErr != nil {
			h8s.provider.Logger.Error("failed to publish reply", "error", err)
			return
		}
		h8s.provider.Logger.Info("info", "reply:", replyMsg)
	})
	if subErr != nil {
		h8s.provider.Logger.Error("runRequestReplyHandler: failed to subscribe to subject", "subject", subject, "error", err)
		return err
	}
	h8s.provider.Logger.Debug("runRequestReplyHandler: subscribed to subject", "subject", subject)
	return nil
}

func (h *H8SHandler) Shutdown() {
	/*
		for target := range h.cron {
			h.Stop(target)
		}
	*/
}

func wasmMsgToNatsMsg(msg *types.Msg) *nats.Msg {
	natsmsg := &nats.Msg{}

	for _, v := range msg.Headers {
		natsmsg.Header.Add(v.Key, strings.Join(v.Value, ", "))
	}

	natsmsg.Data = msg.Data
	natsmsg.Subject = msg.Subject
	natsmsg.Reply = msg.Reply
	return natsmsg
}

func natsMsgToWasmMsg(msg *nats.Msg) *types.Msg {
	// Convert NATS message to wasmcloud types.Msg
	newHeaders := []*types.KeyValue{}
	for k, v := range msg.Header {
		newHeaders = append(newHeaders, &types.KeyValue{
			Key:   k,
			Value: v,
		})
	}
	return &types.Msg{
		Headers: newHeaders,
		Data:    msg.Data,
		Subject: msg.Subject,
		Reply:   msg.Reply,
	}
}

// validateInterfaces() validates (duh) a list of interfaces on a link
// against a set of valid interfaces.
func validateInterfaces(validInterfaces, input []string) (ok bool, valid, invalid []string) {
	validSet := make(map[string]struct{}, len(validInterfaces))
	for _, v := range validInterfaces {
		validSet[v] = struct{}{}
	}

	for _, val := range input {
		if _, exists := validSet[val]; exists {
			valid = append(valid, val)
		} else {
			invalid = append(invalid, val)
		}
	}

	ok = len(valid) > 0
	return
}
