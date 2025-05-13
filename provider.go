package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

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

	subjectBySenderComponent      map[string]string               // [subject][target.name] = target
	websocketConnectionsBySubject map[string]map[string]*nats.Msg // [subject][connection] = NATS message
}

func NewH8SHandler() *H8SHandler {
	handler := &H8SHandler{
		linkedFrom: make(map[string]map[string]string),
		linkedTo:   make(map[string]map[string]string),

		// Holds NATS connection for components that might use
		// diffrent credentials and different NATS infrastructure.
		natsConnByComponent: make(map[string]*nats.Conn),

		// Map that holds subscribe filters for a component.
		subsByComponent:               make(map[string]string),
		subjectBySenderComponent:      make(map[string]string),
		websocketConnectionsBySubject: make(map[string]map[string]*nats.Msg),
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
			//handle receiver
		default:
			h8s.provider.Logger.Warn("unexpected valid interface", "warning", "source link contained valid but unhandled interface", "interface", item)
		}
	}

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

		h8s.provider.Logger.Error("invalid SourceConfig", "error", errInvalidSourceConfigProperties)
		return errors.New(errInvalidSourceConfigProperties)
	}

	url := fmt.Sprintf("%s://%s%s", link.SourceConfig["method"], link.SourceConfig["host"], link.SourceConfig["path"])

	mapper := subjectmapper.NewWebSocketMap(url)
	h8s.subjectBySenderComponent[link.Target] = mapper.WebSocketPublishSubject()
	h8s.provider.Logger.Info("watching for connections on subject", "subject", mapper.WebSocketPublishSubject())
	h8s.runSenderHandler(link.Target)
	return nil
}

func (h8s *H8SHandler) GetConnections(context.Context) (*wrpc.Result[[]*types.Msg, string], error) {
	var connections []*types.Msg

	for subject, value := range h8s.websocketConnectionsBySubject {
		h8s.provider.Logger.Info("finding connections for subject", "subject", subject)
		for conn, msg := range value {
			h8s.provider.Logger.Info("found connection", "connection", conn)
			connections = append(connections, natsMsgToWasmMsg(msg))
		}

	}
	return &wrpc.Result[[]*types.Msg, string]{&connections, nil}, nil
}

func (h8s *H8SHandler) GetConnectionsBySubject(context context.Context, subject string) (*wrpc.Result[[]*types.Msg, string], error) {

	return nil, nil
}

func (h8s *H8SHandler) Send(context context.Context, conn string, payload []uint8) (*wrpc.Result[struct{}, string], error) {

	return nil, nil
}

func (h8s *H8SHandler) RemoveComponent(link provider.InterfaceLinkDefinition) error {
	h8s.provider.Logger.Info("handling target link delete", "link", link.Name)

	return nil
}

// runSenderHandler will subscribe to nats and listen for websocket connections and build up
// the internal h8s-provider state.
// todo: this is simlar to the receiver needs, check this again when we get to that.
func (h8s *H8SHandler) runSenderHandler(target string) error {
	nc, err := nats.Connect("nats://localhost:4222", nats.Name("h8s-provider-request-reply"))
	if err != nil {
		return errors.New("failed to connect to NATS server")
	}

	subject := h8s.subjectBySenderComponent[target]

	_, subErr := nc.Subscribe(subject, func(msg *nats.Msg) {
		h8s.provider.Logger.Info("Received message", "subject", msg.Subject, "data", msg.Data)

		h8s.websocketConnectionsBySubject[msg.Subject][msg.Reply] = msg
		h8s.provider.Logger.Info("Added websocket connection", "connection", msg.Reply, "subject", msg.Subject)
	})
	if subErr != nil {
		h8s.provider.Logger.Error("failed to subscribe to subject", "subject", subject, "error", err)
		return err
	}
	h8s.provider.Logger.Info("Subscribed to subject", "subject", subject)
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
		h8s.provider.Logger.Error("failed to subscribe to subject", "subject", subject, "error", err)
		return err
	}
	h8s.provider.Logger.Info("Subscribed to subject", "subject", subject)
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
