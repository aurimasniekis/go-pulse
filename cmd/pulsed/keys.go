package main

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/aurimasniekis/go-pulse/internal/audit"
	"github.com/aurimasniekis/go-pulse/internal/bootstrap"
	"github.com/aurimasniekis/go-pulse/internal/config"
	"github.com/aurimasniekis/go-pulse/internal/store"
)

func newKeysCmd() *cobra.Command {
	keys := &cobra.Command{Use: "keys", Short: "Key store operations"}
	var confirm bool
	export := &cobra.Command{
		Use:   "export",
		Short: "Print the decrypted key set as JSON to stdout. Requires pulsed to be stopped.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !confirm {
				return errors.New("this prints every signing key in plaintext; re-run with --yes-i-understand")
			}
			cfg, err := config.Load(nil)
			if err != nil {
				return err
			}
			ks, err := bootstrap.LoadKeySet(cfg.Layout.KeysFile(), cfg.MasterKey)
			if err != nil {
				return err
			}
			// The audit record is written before anything is printed; the database lock means pulsed must be stopped.
			db, err := store.Open(cmd.Context(), store.Options{Path: cfg.Layout.DatabaseFile(), LockPath: cfg.Layout.LockFile()})
			if err != nil {
				return fmt.Errorf("open database for the audit record (stop pulsed first): %w", err)
			}
			defer db.Close()
			if err := audit.Write(cmd.Context(), db, audit.Actor{Name: "cli"}, "keys.export", "keys.enc", audit.OK, nil); err != nil {
				return err
			}
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(ks)
		},
	}
	export.Flags().BoolVar(&confirm, "yes-i-understand", false, "confirm printing secrets")
	keys.AddCommand(export)
	return keys
}
