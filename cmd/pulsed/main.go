// Command pulsed is the Pulse server: NATS bootstrap, edge, dashboard, rollups, bans, webhooks, backups.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
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
	root := &cobra.Command{
		Use:           "pulsed",
		Short:         "Pulse server: a personal durable event bus on NATS JetStream",
		SilenceUsage:  true,
		SilenceErrors: false,
	}
	root.AddCommand(newVersionCmd(), newHealthcheckCmd(), newInitCmd(), newServeCmd(), newKeysCmd(), newOpenAPICmd(), newBanagentCmd(), newBackupCmd())
	return root
}
