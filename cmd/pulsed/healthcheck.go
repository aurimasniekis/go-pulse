package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/spf13/cobra"
)

// healthcheck probes /readyz on the metrics listener. It exists because the distroless image has no shell or curl.
func newHealthcheckCmd() *cobra.Command {
	var url string
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "healthcheck",
		Short: "Probe the local readiness endpoint (container HEALTHCHECK)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return probe(cmd.Context(), url, timeout)
		},
	}
	def := "http://127.0.0.1:9464/readyz"
	if v := os.Getenv("PULSE_HEALTHCHECK_URL"); v != "" {
		def = v
	}
	cmd.Flags().StringVar(&url, "url", def, "readiness URL")
	cmd.Flags().DurationVar(&timeout, "timeout", 3*time.Second, "request timeout")
	return cmd
}

func probe(ctx context.Context, url string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("not ready: %s", resp.Status)
	}
	return nil
}
