//go:generate wit-bindgen-wrpc go --out-dir bindings --package github.com/Mattilsynet/h8s-provider/bindings wit

package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"go.wasmcloud.dev/provider"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	// Initialize the provider with callbacks to track linked components
	providerHandler := NewH8SHandler()
	p, err := provider.New(
		//		provider.SourceLinkPut(func(link provider.InterfaceLinkDefinition) error {
		//			return handleNewSourceLink(providerHandler, link)
		//		}),
		provider.SourceLinkPut(func(link provider.InterfaceLinkDefinition) error {
			return providerHandler.AddComponent(link)
		}),
		//		provider.SourceLinkDel(func(link provider.InterfaceLinkDefinition) error {
		//			return handleDelSourceLink(providerHandler, link)
		//		}),
		provider.TargetLinkDel(func(link provider.InterfaceLinkDefinition) error {
			return handleDelTargetLink(providerHandler, link)
		}),
		provider.HealthCheck(func() string {
			return handleHealthCheck(providerHandler)
		}),
		provider.Shutdown(func() error {
			return handleShutdown(providerHandler)
		}),
	)
	if err != nil {
		return err
	}
	// Store the provider for use in the handlers
	providerHandler.provider = p

	// Setup two channels to await RPC and control interface operations
	providerCh := make(chan error, 1)
	signalCh := make(chan os.Signal, 1)

	// Handle control interface operations
	go func() {
		startErr := p.Start()
		providerCh <- startErr
	}()
	// Shutdown on SIGINT
	signal.Notify(signalCh, syscall.SIGINT)
	// Run provider until either a shutdown is requested or a SIGINT is received
	select {
	case err = <-providerCh:
		//		stopFunc()
		return err
	case <-signalCh:
		p.Shutdown()
		//		stopFunc()
	}
	return nil
}

/*
func handleNewSourceLink(handler *H8SHandler, link provider.InterfaceLinkDefinition) error {
	fmt.Println("Handling new source link", "link", link)
	handler.linkedTo[link.Target] = link.SourceConfig
	return nil
}
*/

/*
func handleDelSourceLink(handler *H8SHandler, link provider.InterfaceLinkDefinition) error {
	fmt.Println("Handling del source link", "link", link)
	delete(handler.linkedTo, link.SourceID)
	return nil
}
*/

func handleDelTargetLink(handler *H8SHandler, link provider.InterfaceLinkDefinition) error {
	fmt.Println("Handling del target link", "link", link)
	delete(handler.linkedFrom, link.Target)
	return nil
}

func handleHealthCheck(_ *H8SHandler) string {
	fmt.Println("Handling health check")
	return "provider healthy"
}

func handleShutdown(handler *H8SHandler) error {
	fmt.Println("Handling shutdown")
	clear(handler.linkedFrom)
	clear(handler.linkedTo)
	return nil
}
