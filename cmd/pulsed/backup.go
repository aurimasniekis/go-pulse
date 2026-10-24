package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/spf13/cobra"

	"github.com/aurimasniekis/go-pulse/internal/app"
	"github.com/aurimasniekis/go-pulse/internal/backup"
	"github.com/aurimasniekis/go-pulse/internal/config"
	"github.com/aurimasniekis/go-pulse/internal/logging"
	"github.com/aurimasniekis/go-pulse/internal/store"
)

func newBackupCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "backup", Short: "Backups: keygen, list, run, restore (PL-BAK-006)"}
	cmd.AddCommand(newBackupKeygenCmd(), newBackupListCmd(), newBackupRunCmd(), newBackupRestoreCmd())
	return cmd
}

func newBackupKeygenCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "keygen",
		Short: "Print a new age identity (keep it offline) and its recipient (PULSE_BACKUP_AGE_RECIPIENTS)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			id, rcpt, err := backup.GenerateIdentity()
			if err != nil {
				return err
			}
			fmt.Fprint(cmd.OutOrStdout(), id)
			fmt.Fprintln(cmd.ErrOrStderr(), "recipient:", rcpt)
			return nil
		},
	}
}

func backupConfig() (*config.Config, backup.Options, error) {
	cfg, err := config.Load(nil)
	if err != nil {
		return nil, backup.Options{}, err
	}
	if !cfg.BackupEnabled {
		return nil, backup.Options{}, errors.New("backups are not configured (PULSE_BACKUP_ENABLED and PULSE_BACKUP_S3_*)")
	}
	o, err := app.BackupOptions(cfg)
	return cfg, o, err
}

func readIdentity(path string) (*backup.Reader, error) {
	if path == "" {
		return nil, errors.New("--identity is required (the age identity file from `pulsed backup keygen`)")
	}
	b, err := os.ReadFile(path) //nolint:gosec // operator-supplied path
	if err != nil {
		return nil, err
	}
	ids, err := backup.ParseIdentities(string(b))
	if err != nil {
		return nil, err
	}
	return &backup.Reader{Identities: ids}, nil
}

func newBackupListCmd() *cobra.Command {
	var identity string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List complete backups in the object store; with --identity, also their streams",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, o, err := backupConfig()
			if err != nil {
				return err
			}
			svc := backup.New(nil, nil, nil, o)
			refs, err := svc.Remote(cmd.Context())
			if err != nil {
				return err
			}
			var rd *backup.Reader
			if identity != "" {
				if rd, err = readIdentity(identity); err != nil {
					return err
				}
				rd.Store, rd.NameKey = o.Store, o.NameKey
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tCREATED\tSIZE\tCHUNKS\tSTREAMS")
			for _, r := range refs {
				streams := "-"
				if rd != nil {
					m, err := rd.Manifest(cmd.Context(), r.ID)
					if err != nil {
						streams = "error: " + err.Error()
					} else {
						var names []string
						for _, s := range m.Streams {
							names = append(names, fmt.Sprintf("%s(%d)", s.Name, s.Msgs))
						}
						streams = strings.Join(names, " ")
					}
				}
				fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%s\n", r.ID, r.Created.Format(time.RFC3339), r.LogicalBytes, len(r.Chunks), streams)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().StringVar(&identity, "identity", "", "age identity file (decrypts manifests to show streams)")
	return cmd
}

func newBackupRunCmd() *cobra.Command {
	var adminURL string
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Ask the running pulsed to back up now",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if adminURL == "" {
				cfg, err := config.Load(nil)
				if err != nil {
					return err
				}
				_, port, err := net.SplitHostPort(cfg.AdminAddr)
				if err != nil {
					return err
				}
				adminURL = "http://127.0.0.1:" + port
			}
			req, err := http.NewRequestWithContext(cmd.Context(), http.MethodPost, strings.TrimRight(adminURL, "/")+"/api/admin/v1/backups/run", nil)
			if err != nil {
				return err
			}
			req.Header.Set("X-Pulse-CSRF", "1")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != http.StatusAccepted {
				return fmt.Errorf("pulsed answered %s: %s", resp.Status, bytes.TrimSpace(body))
			}
			fmt.Fprintln(cmd.OutOrStdout(), "backup started; follow it on the Backups page or in the logs")
			return nil
		},
	}
	cmd.Flags().StringVar(&adminURL, "admin-url", "", "admin listener base URL (default http://127.0.0.1:<PULSE_ADMIN_ADDR port>)")
	return cmd
}

func newBackupRestoreCmd() *cobra.Command {
	var (
		id, identity string
		streams      []string
		all, replace bool
		files        bool
		dryRun       bool
		overwrite    bool
	)
	cmd := &cobra.Command{
		Use:   "restore",
		Short: "Restore files (pulse.db, keys.enc; pulsed stopped) or streams (NATS running) from a backup",
		Long: `Disaster recovery on a new server:
  1. pulsed backup restore --id <id> --identity id.txt --files     (before pulsed init; pulsed stopped)
  2. pulsed init, then start nats and pulsed                       (pulsed pushes the restored accounts)
  3. pulsed backup restore --id <id> --identity id.txt --all       (streams; pulsed may be running)`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if id == "" {
				return errors.New("--id is required (see `pulsed backup list`)")
			}
			if files == (all || len(streams) > 0) {
				return errors.New("choose --files, or --all / --streams")
			}
			cfg, o, err := backupConfig()
			if err != nil {
				return err
			}
			rd, err := readIdentity(identity)
			if err != nil {
				return err
			}
			rd.Store, rd.NameKey = o.Store, o.NameKey
			ctx := cmd.Context()
			m, err := rd.Manifest(ctx, id)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if files {
				if dryRun {
					for _, f := range m.Files {
						fmt.Fprintf(out, "would restore %s (%d bytes)\n", f.Name, f.Size)
					}
					return nil
				}
				// The data-directory lock proves pulsed is stopped.
				db, err := store.Open(ctx, store.Options{Path: cfg.Layout.DatabaseFile(), LockPath: cfg.Layout.LockFile()})
				if err != nil {
					return fmt.Errorf("stop pulsed first: %w", err)
				}
				_ = db.Close()
				targets := map[string]string{"pulse.db": cfg.Layout.DatabaseFile(), "keys.enc": cfg.Layout.KeysFile()}
				for _, f := range m.Files {
					p := targets[f.Name]
					if p == "" {
						continue
					}
					if err := rd.WriteFile(ctx, m, f.Name, p, overwrite || f.Name == "pulse.db"); err != nil {
						return err
					}
					if f.Name == "pulse.db" {
						_ = os.Remove(p + "-wal")
						_ = os.Remove(p + "-shm")
					}
					fmt.Fprintf(out, "restored %s → %s\n", f.Name, p)
				}
				return nil
			}
			nc, err := app.ConnectApp(ctx, cfg, logging.New(cmd.ErrOrStderr(), cfg.LogLevel))
			if err != nil {
				return err
			}
			defer nc.Close()
			js, err := jetstream.New(nc)
			if err != nil {
				return err
			}
			res, err := rd.RestoreStreams(ctx, nc, js, m, backup.RestoreOptions{Streams: streams, Replace: replace, DryRun: dryRun})
			tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "STREAM\tMSGS\tSEQ\tEXISTS\tACTION\tRESULT")
			for _, p := range res {
				fmt.Fprintf(tw, "%s\t%d\t%d–%d\t%v\t%s\t%s\n", p.Name, p.Msgs, p.FirstSeq, p.LastSeq, p.Exists, p.Action, p.Result)
			}
			_ = tw.Flush()
			return err
		},
	}
	f := cmd.Flags()
	f.StringVar(&id, "id", "", "backup id")
	f.StringVar(&identity, "identity", "", "age identity file")
	f.StringSliceVar(&streams, "streams", nil, "streams to restore")
	f.BoolVar(&all, "all", false, "restore every stream in the backup")
	f.BoolVar(&replace, "replace", false, "delete existing streams first")
	f.BoolVar(&files, "files", false, "restore pulse.db and keys.enc into the data directory (pulsed stopped)")
	f.BoolVar(&overwrite, "overwrite", false, "with --files: replace an existing keys.enc")
	f.BoolVar(&dryRun, "dry-run", false, "show what would be restored")
	return cmd
}
