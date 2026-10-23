// Command pulse is the Pulse CLI: publish, subscribe and replay events, manage contexts.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/aurimasniekis/go-pulse/internal/app"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := newRootCmd().ExecuteContext(ctx)
	stop()
	if err != nil {
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "pulse",
		Short:        "Pulse CLI",
		Version:      app.Version,
		SilenceUsage: true,
	}
}
