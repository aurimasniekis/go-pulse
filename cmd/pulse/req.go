package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"
)

func newReqCmd() *cobra.Command {
	var cf connFlags
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "req <subject> [data|-]",
		Short: "Send a request and print the reply (needs a request grant)",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			var data []byte
			if len(args) == 2 {
				if args[1] == "-" {
					b, err := io.ReadAll(os.Stdin)
					if err != nil {
						return err
					}
					data = b
				} else {
					data = []byte(args[1])
				}
			}
			c, err := cf.connect()
			if err != nil {
				return err
			}
			defer c.Close()
			ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
			defer cancel()
			m, err := c.Request(ctx, args[0], data)
			if err != nil {
				return err
			}
			if _, err := cmd.OutOrStdout().Write(m.Data); err != nil {
				return err
			}
			if len(m.Data) > 0 && m.Data[len(m.Data)-1] != '\n' {
				_, _ = fmt.Fprintln(cmd.OutOrStdout())
			}
			return nil
		},
	}
	cf.register(cmd)
	cmd.Flags().DurationVar(&timeout, "timeout", 5*time.Second, "how long to wait for a reply")
	return cmd
}

// newTailCmd is `sub --new`: follow new events only.
func newTailCmd() *cobra.Command {
	cmd := newSubCmd()
	cmd.Use = "tail <filter>"
	cmd.Short = "Follow new events (sub --new)"
	cmd.Example = "  pulse tail 'evt.home.>'"
	_ = cmd.Flags().Set("new", "true")
	return cmd
}
