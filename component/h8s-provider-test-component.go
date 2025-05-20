//go:generate go tool wit-bindgen-go generate --world component --out gen ./wit
package main

import (
	cronjob "github.com/Mattilsynet/h8s-provider/component/gen/mattilsynet/cronjob/cronjob"
	receiver "github.com/Mattilsynet/h8s-provider/component/gen/mattilsynet/h8s-provider/receiver"
	requestreply "github.com/Mattilsynet/h8s-provider/component/gen/mattilsynet/h8s-provider/request-reply"
	sender "github.com/Mattilsynet/h8s-provider/component/gen/mattilsynet/h8s-provider/sender"
	"github.com/Mattilsynet/h8s-provider/component/gen/mattilsynet/h8s-provider/types"
	"go.bytecodealliance.org/cm"
	"go.wasmcloud.dev/component/log/wasilog"
)

func init() {
	// When cronjob triggers component, get all websocket connections
	// And send some bytes to all of them to demonstrate bidirectional
	// communication.
	cronjob.Exports.CronHandler = func() {
		logger := wasilog.ContextLogger("cronjob-handler")
		logger.Info("Cronjob handler called")

		result := sender.GetConnections()
		if result.OK().Len() == 0 {
			logger.Info("nobody is connected")
			return
		}

		for _, conn := range result.OK().Slice() {
			list := cm.ToList([]uint8("stuff"))
			sender.Send(conn.Reply, list)

		}
	}

	receiver.Exports.HandleMessage = func(msg receiver.Msg) (result cm.Result[string, string, string]) {
		logger := wasilog.ContextLogger("receiver handle-message")
		logger.Info(
			"Receiver payload from websocket connetion",
			"connection", msg.Reply,
			"payload", msg.Data)

		return cm.OK[cm.Result[string, string, string]]("ok")
	}

	requestreply.Exports.HandleMessage = func(msg requestreply.Msg) (result cm.Result[requestreply.MsgShape, types.Msg, string]) {
		logger := wasilog.ContextLogger("handle-message")
		logger.Info("handler-message called")
		logger.Info("msg", "data", msg.Data)
		headers := make(map[string][]string)
		headers["h8s-provider-test-header"] = []string{"You can do it!"}
		replyMsg := types.Msg{
			Headers: toWitNatsHeaders(headers),
			// Sending back the same thing as we got!
			Data:  cm.ToList([]uint8("This is the component response")),
			Reply: msg.Reply,
		}
		return cm.OK[cm.Result[requestreply.MsgShape, types.Msg, string]](replyMsg)
	}
}

func main() {}

func toNatsHeaders(header cm.List[types.KeyValue]) map[string][]string {
	natsHeaders := make(map[string][]string)
	for _, kv := range header.Slice() {
		natsHeaders[kv.Key] = kv.Value.Slice()
	}
	return natsHeaders
}

func toWitNatsHeaders(header map[string][]string) cm.List[types.KeyValue] {
	keyValueList := make([]types.KeyValue, 0)
	for k, v := range header {
		keyValueList = append(keyValueList, types.KeyValue{
			Key:   k,
			Value: cm.ToList(v),
		})
	}
	return cm.ToList(keyValueList)
}
