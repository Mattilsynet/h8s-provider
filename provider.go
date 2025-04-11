package main

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/Mattilsynet/h8s-provider/bindings/mattilsynet/h8s_provider/request_reply"
	"github.com/Mattilsynet/h8s-provider/bindings/mattilsynet/h8s_provider/types"
	"github.com/nats-io/nats.go"
	"go.wasmcloud.dev/provider"
)

type H8SHandler struct {
	// The provider instance
	provider *provider.WasmcloudProvider
	// All components linked to this provider and their config.
	linkedFrom map[string]map[string]string
	// All components this provider is linked to and their config
	linkedTo            map[string]map[string]string
	natsConnByComponent map[string]*nats.Conn
	subsByComponent     map[string][]string
}

func NewH8SHandler() *H8SHandler {
	handler := &H8SHandler{
		linkedFrom: make(map[string]map[string]string),
		linkedTo:   make(map[string]map[string]string),

		// Holds NATS connection for components that might
		// diffrent credentials and diffrent NATS infrastructure.
		natsConnByComponent: make(map[string]*nats.Conn),

		// Map that holds subscribe filters for a component.
		subsByComponent: map[string][]string{},
	}
	return handler
}

func (h8s *H8SHandler) AddComponent(link provider.InterfaceLinkDefinition) error {
	h8s.provider.Logger.Info("Handling new source link", "link", link)
	if !slices.Contains(link.Interfaces, "request-reply") {
		h8s.provider.Logger.Error("invalid source link", "error", "source link interface does not contain the 'handle-message' method")
		interfacesJoined := strings.Join(link.Interfaces, ",")
		return errors.New("the target link interfaces didn't contain 'handle-message' method, got: " + interfacesJoined)
	}
	// We don't need to update these maps.
	// h8s.linkedFrom[link.SourceID] = link.TargetConfig

	// Handle TargetConfig from the component.
	if len(link.SourceConfig["subjects"]) == 0 {
		h8s.provider.Logger.Error("invalid target config", "error", "target link subjects is empty")
		return errors.New("the target link subject is empty")
	}
	h8s.subsByComponent[link.Target] = strings.Split(link.SourceConfig["subjects"], ",")
	h8s.provider.Logger.Debug("subscribed to subjects", "subjects", h8s.subsByComponent[link.Target])
	h8s.Run(link.Target)
	return nil
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
	nc, err := nats.Connect("nats://localhost:4222")
	if err != nil {
	}
	// Subscribe to the subjects for the target component
	for _, subject := range h8s.subsByComponent[target] {
		_, err := nc.Subscribe(subject, func(msg *nats.Msg) {
			h8s.provider.Logger.Info("Received message", "subject", msg.Subject, "data", string(msg.Data))
			client := h8s.provider.OutgoingRpcClient(target)

			// convert nats msg to types.Msg
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

			result, err := request_reply.HandleMessage(context.Background(), client, &newMsg)
			if err != nil {
				h8s.provider.Logger.Error("failed to handle message", "error", err)
				return

			}
			if result.Err != nil {
				if err := nc.Publish(msg.Reply, []byte("something went wrong")); err != nil {
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

			if err := nc.PublishMsg(replyMsg); err != nil {
				h8s.provider.Logger.Error("failed to publish reply", "error", err)
				return
			}
			h8s.provider.Logger.Info("info", "reply:", replyMsg)
		})
		if err != nil {
			h8s.provider.Logger.Error("failed to subscribe to subject", "subject", subject, "error", err)
			return err
		}
		h8s.provider.Logger.Info("Subscribed to subject", "subject", subject)
	}
	return nil
}

func (h *H8SHandler) Shutdown() {
	/*
		for target := range h.cron {
			h.Stop(target)
		}
	*/
}
