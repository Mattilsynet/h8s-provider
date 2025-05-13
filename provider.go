package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"

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

	websocketSubjectByConnection  map[string]string
	websocketConnectionsBySubject map[string][]string
}

func NewH8SHandler() *H8SHandler {
	handler := &H8SHandler{
		linkedFrom: make(map[string]map[string]string),
		linkedTo:   make(map[string]map[string]string),

		// Holds NATS connection for components that might
		// diffrent credentials and diffrent NATS infrastructure.
		natsConnByComponent: make(map[string]*nats.Conn),

		// Map that holds subscribe filters for a component.
		subsByComponent: make(map[string]string),
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

		h8s.provider.Logger.Error("invalid target config", "error", errInvalidSourceConfigProperties)
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
	h8s.Run(link.Target)
	return nil

}

func (h8s *H8SHandler) addSenderComponent(link provider.InterfaceLinkDefinition) error {
	return nil
}

func (h8s *H8SHandler) GetConnections(context.Context) (*wrpc.Result[[]*types.Msg, string], error) {

	// return the nats msg of the initial handshake that we persist
	// in the handler keyed on conneciton.

	return nil, nil
}

func (h8s *H8SHandler) GetConnectionsBySubject(context context.Context, subject string) (*wrpc.Result[[]*types.Msg, string], error) {

	return nil, nil
}

func (h8s *H8SHandler) Send(context context.Context, conn string, payload []uint8) (*wrpc.Result[struct{}, string], error) {

	return nil, nil
}

func (h8s *H8SHandler) RemoveComponent(link provider.InterfaceLinkDefinition) error {
	h8s.provider.Logger.Info("handling target link delete", "link", link)
	return nil
}

func (h8s *H8SHandler) Run(target string) error {
	// Get the NATS connection for the target component
	/*
		nc, ok := h8s.natsConnByComponent[target]

		if !ok {
			h8s.provider.Logger.Error("no NATS connection found for target", "target", target)
			return errors.New("no NATS connection found for target")
		}
	*/
	nc, err := nats.Connect("nats://localhost:4222", nats.Name("h8s-provider"))
	if err != nil {
		return errors.New("failed to connect to NATS server")
	}

	subject := h8s.subsByComponent[target]

	// This is the "server": Subscribe to publish subject for request going through h8s.
	_, subErr := nc.Subscribe(subject, func(msg *nats.Msg) {
		h8s.provider.Logger.Info("Received message", "subject", msg.Subject, "data", string(msg.Data))
		wrpcclient := h8s.provider.OutgoingRpcClient(target)

		// Convert NATS message to wasmcloud types.Msg
		newHeaders := []*types.KeyValue{}
		for k, v := range msg.Header {
			newHeaders = append(newHeaders, &types.KeyValue{
				Key:   k,
				Value: v,
			})
		}
		newMsg := types.Msg{
			Headers: newHeaders,
			Data:    msg.Data,
			Subject: msg.Subject,
			Reply:   msg.Reply,
		}
		mapOfInboxesVsComponents := make(map[string]string)
		mapOfInboxesVsComponents[target] = msg.Reply
		result, hmErr := request_reply.HandleMessage(context.Background(), wrpcclient, &newMsg)
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
