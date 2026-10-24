package main

import (
	"fmt"
	"log/slog"
	"net/url"
	"time"

	"github.com/spf13/cobra"

	"github.com/aurimasniekis/go-pulse/internal/app"
	"github.com/aurimasniekis/go-pulse/internal/config"
	"github.com/aurimasniekis/go-pulse/internal/store"
	"github.com/aurimasniekis/go-pulse/internal/users"
)

// newAdminCmd is first-administrator bootstrap and break-glass recovery (ADR-0038). Both run on the host with the
// master key (inside the pulsed container: docker compose exec pulsed pulsed admin …) and work while pulsed serves.
func newAdminCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "admin", Short: "Dashboard bootstrap and break-glass recovery (needs the master key; audited)"}
	cmd.AddCommand(newAdminBootstrapCmd(), newAdminRecoverCmd(), newAdminTokenCmd())
	return cmd
}

func openUsers(cmd *cobra.Command) (*config.Config, *store.DB, *users.Service, error) {
	cfg, err := config.Load(nil)
	if err != nil {
		return nil, nil, nil, err
	}
	db, err := store.Open(cmd.Context(), store.Options{Path: cfg.Layout.DatabaseFile(), LockPath: cfg.Layout.LockFile(), Shared: true})
	if err != nil {
		return nil, nil, nil, err
	}
	u, err := app.NewUserAuth(db, cfg, slog.New(slog.DiscardHandler))
	if err != nil {
		_ = db.Close()
		return nil, nil, nil, err
	}
	return cfg, db, u, nil
}

func tokenLink(base, path, token string) string {
	return base + path + "?token=" + url.QueryEscape(token)
}

func newAdminBootstrapCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "bootstrap",
		Short: "Issue the one-time setup link for the first administrator",
		Long: "Issues a single-use setup link (30 minutes) for creating the first administrator. It works only while no " +
			"administrator exists. Open it on the dashboard origin (PULSE_ADMIN_URL), e.g. through ssh -L 8080:127.0.0.1:8080.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, db, u, err := openUsers(cmd)
			if err != nil {
				return err
			}
			defer db.Close()
			tok, exp, err := u.IssueSetupToken(cmd.Context())
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "One-time setup link (single use, expires %s); it is shown only once:\n  %s\n  token: %s\n",
				exp.UTC().Format(time.RFC3339), tokenLink(cfg.AdminURL, "/setup", tok), tok)
			return nil
		},
	}
}

func newAdminRecoverCmd() *cobra.Command {
	var email string
	var newAdmin bool
	cmd := &cobra.Command{
		Use:   "recover",
		Short: "Break-glass: issue a single-use 30-minute recovery login (audited)",
		Long: "Issues a single-use recovery login for a user who lost their password or second factor. Redeeming it signs out " +
			"their sessions, removes their TOTP factor and recovery codes, and offers a password reset. With --new-admin it creates a " +
			"new administrator instead. Issuing and redeeming are written to the audit log.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if email == "" {
				return fmt.Errorf("--email is required")
			}
			norm, err := users.NormalizeEmail(email)
			if err != nil {
				return err
			}
			cfg, db, u, err := openUsers(cmd)
			if err != nil {
				return err
			}
			defer db.Close()
			var id, newEmail string
			if newAdmin {
				newEmail = norm
			} else {
				existing, err := u.GetByEmail(cmd.Context(), norm)
				if err != nil {
					return fmt.Errorf("no user with email %s (use --new-admin to create a new administrator)", norm)
				}
				id = existing.ID
			}
			tok, exp, err := u.IssueRecoveryLogin(cmd.Context(), id, newEmail)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "BREAK-GLASS recovery login for %s issued and audited (user.breakglass.issue).\n", norm)
			fmt.Fprintf(out, "Single use, expires %s. It is shown only once; do not store or forward it.\n  %s\n  token: %s\n",
				exp.UTC().Format(time.RFC3339), tokenLink(cfg.AdminURL, "/recover", tok), tok)
			fmt.Fprintln(out, "After signing in, review the audit log and rotate credentials as needed.")
			return nil
		},
	}
	cmd.Flags().StringVar(&email, "email", "", "email of the user to recover (or of the new administrator with --new-admin)")
	cmd.Flags().BoolVar(&newAdmin, "new-admin", false, "create a new local administrator with --email instead of recovering an existing user")
	return cmd
}

func newAdminTokenCmd() *cobra.Command {
	var email, name, role string
	var days int
	var create bool
	cmd := &cobra.Command{
		Use:   "token",
		Short: "Issue a personal access token for automation (prints it once; audited)",
		Long: "Issues a personal access token for the loopback admin listener, e.g. for deployment checks or scripts. With --create a " +
			"missing user is created without a password, so it can only use tokens. The role is added to the user's roles when missing.",
		Example: "  pulsed admin token --email automation@example.com --create --role operator --days 1",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if email == "" {
				return fmt.Errorf("--email is required")
			}
			_, db, u, err := openUsers(cmd)
			if err != nil {
				return err
			}
			defer db.Close()
			t, secret, err := u.IssuePATOnHost(cmd.Context(), email, name, role, time.Duration(days)*24*time.Hour, create)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), secret)
			fmt.Fprintf(cmd.ErrOrStderr(), "token %s (%s, role %s) expires %s; it works only on the loopback admin listener\n",
				t.ID, t.Name, t.Role, t.ExpiresAt.UTC().Format(time.RFC3339))
			return nil
		},
	}
	cmd.Flags().StringVar(&email, "email", "", "user the token belongs to")
	cmd.Flags().BoolVar(&create, "create", false, "create the user (without a password) when missing")
	cmd.Flags().StringVar(&role, "role", "operator", "role of the token: administrator, operator, auditor or viewer")
	cmd.Flags().StringVar(&name, "name", "automation", "token name")
	cmd.Flags().IntVar(&days, "days", 1, "lifetime in days (at most 366)")
	return cmd
}
