package main

import (
	"errors"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/aurimasniekis/go-pulse/internal/banagent"
	"github.com/aurimasniekis/go-pulse/internal/logging"
)

func envOr(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

// banagent needs no master key: it only reads the ban feed with its token and writes two nftables sets (PL-BAN-007).
func newBanagentCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "banagent",
		Short: "Mirror pulsed's bans into nftables (host network, NET_ADMIN)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			log := logging.New(os.Stdout, envOr("PULSE_LOG_LEVEL", "info"))
			tokenFile := envOr("PULSE_BANAGENT_TOKEN_FILE", "/run/pulse-banagent/token")
			tok, err := os.ReadFile(tokenFile) //nolint:gosec // operator-supplied path
			if err != nil {
				return err
			}
			if strings.TrimSpace(string(tok)) == "" {
				return errors.New("banagent token file is empty")
			}
			a := &banagent.Agent{
				URL:   envOr("PULSE_BANAGENT_URL", "http://127.0.0.1:9464/internal/v1/bans"),
				Token: strings.TrimSpace(string(tok)),
				Backend: &banagent.NFT{
					Table: envOr("PULSE_BANAGENT_TABLE", "pulse_edge"),
					SetV4: envOr("PULSE_BANAGENT_SET_V4", "pulse_ban_v4"),
					SetV6: envOr("PULSE_BANAGENT_SET_V6", "pulse_ban_v6"),
				},
				Log: log,
			}
			log.Info("banagent starting", "feed", a.URL)
			return a.Run(cmd.Context())
		},
	}
}
