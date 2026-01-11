package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/flynn/flynn/router/acme"
	"github.com/inconshreveable/log15"
)

func main() {
	log := log15.New("component", "acme")
	log.Info("starting ACME service")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle shutdown signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		log.Info("received signal, shutting down", "signal", sig)
		cancel()
	}()

	if err := acme.RunService(ctx); err != nil {
		log.Error("ACME service error", "err", err)
		os.Exit(1)
	}
}

