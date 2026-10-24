package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

// cliConfig is stored in <user config dir>/pulse/config.json (0600). It holds paths, never secrets.
type cliConfig struct {
	Current  string                 `json:"current"`
	Contexts map[string]*cliContext `json:"contexts"`
}

type cliContext struct {
	URL      string `json:"url"`
	Creds    string `json:"creds"`
	CA       string `json:"ca,omitempty"`
	AdminURL string `json:"admin_url,omitempty"`
	// AdminTokenFile holds a personal access token for the admin commands (a path, like Creds).
	AdminTokenFile string `json:"admin_token_file,omitempty"`
}

func configPath() (string, error) {
	if p := os.Getenv("PULSE_CONFIG"); p != "" {
		return p, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "pulse", "config.json"), nil
}

func loadConfig() (*cliConfig, string, error) {
	p, err := configPath()
	if err != nil {
		return nil, "", err
	}
	c := &cliConfig{Contexts: map[string]*cliContext{}}
	b, err := os.ReadFile(p) //nolint:gosec // user config path
	if errors.Is(err, fs.ErrNotExist) {
		return c, p, nil
	}
	if err != nil {
		return nil, "", err
	}
	if err := json.Unmarshal(b, c); err != nil {
		return nil, "", fmt.Errorf("%s: %w", p, err)
	}
	if c.Contexts == nil {
		c.Contexts = map[string]*cliContext{}
	}
	return c, p, nil
}

func (c *cliConfig) save(p string) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, append(b, '\n'), 0o600)
}

// activeContext resolves --context, $PULSE_CONTEXT or the current context; flags override its fields.
func activeContext(name string) (*cliContext, error) {
	c, _, err := loadConfig()
	if err != nil {
		return nil, err
	}
	if name == "" {
		name = os.Getenv("PULSE_CONTEXT")
	}
	if name == "" {
		name = c.Current
	}
	if name == "" {
		return &cliContext{}, nil
	}
	ctx, ok := c.Contexts[name]
	if !ok {
		return nil, fmt.Errorf("unknown context %q (pulse context ls)", name)
	}
	cp := *ctx
	return &cp, nil
}

func newContextCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "context", Short: "Manage connection contexts (URL + creds path)"}
	var add cliContext
	addCmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Add or replace a context",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if add.URL == "" || add.Creds == "" {
				return errors.New("--url and --creds are required")
			}
			abs, err := filepath.Abs(add.Creds)
			if err != nil {
				return err
			}
			add.Creds = abs
			if add.CA != "" {
				if add.CA, err = filepath.Abs(add.CA); err != nil {
					return err
				}
			}
			if add.AdminTokenFile != "" {
				if add.AdminTokenFile, err = filepath.Abs(add.AdminTokenFile); err != nil {
					return err
				}
			}
			c, p, err := loadConfig()
			if err != nil {
				return err
			}
			cp := add
			c.Contexts[args[0]] = &cp
			if c.Current == "" {
				c.Current = args[0]
			}
			if err := c.save(p); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "saved context %q in %s\n", args[0], p)
			return err
		},
	}
	addCmd.Flags().StringVar(&add.URL, "url", "", "NATS URL, e.g. tls://r.events.example.com:4222")
	addCmd.Flags().StringVar(&add.Creds, "creds", "", "path to the .creds file")
	addCmd.Flags().StringVar(&add.CA, "ca", "", "extra CA PEM (internal CA during bootstrap)")
	addCmd.Flags().StringVar(&add.AdminURL, "admin-url", "", "admin API base URL (via ssh -L), e.g. http://127.0.0.1:8080")
	addCmd.Flags().StringVar(&add.AdminTokenFile, "admin-token-file", "", "file with a personal access token (dashboard → Account) for the admin commands")

	useCmd := &cobra.Command{
		Use:   "use <name>",
		Short: "Select the current context",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, p, err := loadConfig()
			if err != nil {
				return err
			}
			if _, ok := c.Contexts[args[0]]; !ok {
				return fmt.Errorf("unknown context %q", args[0])
			}
			c.Current = args[0]
			return c.save(p)
		},
	}
	lsCmd := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List contexts",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, _, err := loadConfig()
			if err != nil {
				return err
			}
			names := make([]string, 0, len(c.Contexts))
			for n := range c.Contexts {
				names = append(names, n)
			}
			sort.Strings(names)
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 2, 2, ' ', 0)
			_, _ = fmt.Fprintln(tw, "\tNAME\tURL\tCREDS")
			for _, n := range names {
				mark := ""
				if n == c.Current {
					mark = "*"
				}
				_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", mark, n, c.Contexts[n].URL, c.Contexts[n].Creds)
			}
			return tw.Flush()
		},
	}
	rmCmd := &cobra.Command{
		Use:   "rm <name>",
		Short: "Remove a context (the creds file is left alone)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, p, err := loadConfig()
			if err != nil {
				return err
			}
			delete(c.Contexts, args[0])
			if c.Current == args[0] {
				c.Current = ""
			}
			return c.save(p)
		},
	}
	cmd.AddCommand(addCmd, useCmd, lsCmd, rmCmd)
	return cmd
}
