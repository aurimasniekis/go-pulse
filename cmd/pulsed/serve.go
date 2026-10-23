package main

import (
	"context"
	"os"

	"github.com/spf13/cobra"

	"github.com/aurimasniekis/go-pulse/internal/app"
	"github.com/aurimasniekis/go-pulse/internal/config"
	"github.com/aurimasniekis/go-pulse/internal/logging"
)

func newInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Create or complete keys, operator JWT, nats.conf, bootstrap TLS and banagent token (idempotent)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(nil)
			if err != nil {
				return err
			}
			return app.Init(cfg, logging.New(os.Stdout, cfg.LogLevel))
		},
	}
}

func newServeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Run the Pulse server",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(nil)
			if err != nil {
				return err
			}
			log := logging.New(os.Stdout, cfg.LogLevel)
			ctx := cmd.Context()
			a, err := app.Start(ctx, cfg, log)
			if err != nil {
				log.Error("start failed", "err", err)
				return err
			}
			<-ctx.Done()
			log.Info("shutting down")
			a.Shutdown(context.WithoutCancel(ctx))
			return nil
		},
	}
}
