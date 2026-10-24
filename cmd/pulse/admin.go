package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

// The admin commands talk to pulsed's admin API on the server's loopback listener: open a tunnel first, e.g.
// `ssh -L 8080:127.0.0.1:8080 <server>` (ADR-0008). They authenticate with a personal access token, which pulsed accepts
// only on that listener (ADR-0038).

type adminFlags struct {
	context   string
	url       string
	tokenFile string
}

func (f *adminFlags) register(cmd *cobra.Command) {
	cmd.PersistentFlags().StringVar(&f.context, "context", "", "context name (default: current)")
	cmd.PersistentFlags().StringVar(&f.url, "admin-url", "", "admin API base URL (default: the context's, else http://127.0.0.1:8080)")
	cmd.PersistentFlags().StringVar(&f.tokenFile, "admin-token-file", "",
		"file with a personal access token (default: the context's, else $PULSE_ADMIN_TOKEN)")
}

type admin struct {
	base  string
	token string
	hc    *http.Client
}

func (f *adminFlags) client() (*admin, error) {
	base, tokenFile := f.url, f.tokenFile
	if base == "" || tokenFile == "" {
		cc, err := activeContext(f.context)
		if err == nil {
			if base == "" {
				base = cc.AdminURL
			}
			if tokenFile == "" {
				tokenFile = cc.AdminTokenFile
			}
		} else if base == "" {
			return nil, err
		}
	}
	if base == "" {
		base = "http://127.0.0.1:8080"
	}
	token := os.Getenv("PULSE_ADMIN_TOKEN")
	if tokenFile != "" {
		b, err := os.ReadFile(tokenFile) //nolint:gosec // the user's own token file
		if err != nil {
			return nil, fmt.Errorf("admin token: %w", err)
		}
		token = strings.TrimSpace(string(b))
	}
	if token == "" {
		return nil, errors.New("the admin commands need a personal access token: create one in the dashboard (Account → Personal access tokens), " +
			"save it to a file and pass --admin-token-file (or `pulse context add … --admin-token-file`), or set PULSE_ADMIN_TOKEN")
	}
	return &admin{base: strings.TrimRight(base, "/") + "/api/admin/v1", token: token, hc: &http.Client{Timeout: 60 * time.Second}}, nil
}

type problem struct {
	Title  string `json:"title"`
	Detail string `json:"detail"`
	Errors []struct {
		Message  string `json:"message"`
		Location string `json:"location"`
	} `json:"errors"`
}

func (a *admin) do(ctx context.Context, method, path string, body, out any) error {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.base+path, r)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+a.token)
	resp, err := a.hc.Do(req)
	if err != nil {
		return fmt.Errorf("admin API at %s: %w (is the ssh tunnel open?)", a.base, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		var p problem
		if json.Unmarshal(data, &p) == nil && (p.Detail != "" || p.Title != "") {
			msg := p.Detail
			if msg == "" {
				msg = p.Title
			}
			for _, e := range p.Errors {
				msg += "; " + e.Location + ": " + e.Message
			}
			return fmt.Errorf("%s %s: %s", method, path, msg)
		}
		return fmt.Errorf("%s %s: %s", method, path, resp.Status)
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

func table(w io.Writer, header string, rows [][]string) error {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, header)
	for _, r := range rows {
		_, _ = fmt.Fprintln(tw, strings.Join(r, "\t"))
	}
	return tw.Flush()
}

func newStreamsCmd() *cobra.Command {
	var af adminFlags
	cmd := &cobra.Command{
		Use:   "streams",
		Short: "List streams (admin API via the ssh tunnel)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := af.client()
			if err != nil {
				return err
			}
			var ss []struct {
				Name     string   `json:"name"`
				Kind     string   `json:"kind"`
				Managed  bool     `json:"managed"`
				Subjects []string `json:"subjects"`
				MaxBytes int64    `json:"max_bytes"`
				State    struct {
					Messages uint64 `json:"msgs"`
					Bytes    uint64 `json:"bytes"`
				} `json:"state"`
			}
			if err := a.do(cmd.Context(), http.MethodGet, "/streams", nil, &ss); err != nil {
				return err
			}
			var rows [][]string
			for _, s := range ss {
				m := ""
				if s.Managed {
					m = "managed"
				}
				rows = append(rows, []string{s.Name, s.Kind, fmt.Sprint(s.State.Messages), human(int64(s.State.Bytes)), limit(s.MaxBytes), strings.Join(s.Subjects, ","), m}) //nolint:gosec // sizes fit
			}
			return table(cmd.OutOrStdout(), "NAME\tKIND\tMSGS\tSIZE\tMAX\tSUBJECTS\t", rows)
		},
	}
	af.register(cmd)
	return cmd
}

func human(n int64) string {
	const u = 1024
	if n < u {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(u), 0
	for m := n / u; m >= u; m /= u {
		div *= u
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func limit(n int64) string {
	if n < 0 {
		return "unlimited"
	}
	return human(n)
}

type clientDTO struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Status      string    `json:"status"`
	ExpiresAt   time.Time `json:"expires_at"`
	Connections int       `json:"connections"`
	ExpiresSoon bool      `json:"expires_soon"`
	Grants      []grant   `json:"grants"`
}

type grant struct {
	Action  string `json:"action"`
	Subject string `json:"subject,omitempty"`
	Stream  string `json:"stream,omitempty"`
	Name    string `json:"name,omitempty"`
}

func (g grant) String() string {
	s := g.Action
	if g.Subject != "" {
		s += ":" + g.Subject
	}
	if g.Name != "" {
		s += ":" + g.Name
	}
	return s
}

// parseGrant reads action[:subject[:name]], e.g. publish:evt.home.>, durable:evt.home.>:indexer, blobs.
func parseGrant(s string) (grant, error) {
	parts := strings.SplitN(s, ":", 3)
	g := grant{Action: parts[0]}
	if len(parts) > 1 {
		g.Subject = parts[1]
	}
	if len(parts) > 2 {
		g.Name = parts[2]
	}
	switch g.Action {
	case "publish", "subscribe", "request", "reply", "durable", "blobs":
	default:
		return g, fmt.Errorf("grant %q: action must be publish, subscribe, request, reply, durable or blobs", s)
	}
	return g, nil
}

func writeCreds(path, creds string) error {
	if path == "" || path == "-" {
		_, err := os.Stdout.WriteString(creds)
		return err
	}
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s exists; not overwriting credentials", path)
	}
	return os.WriteFile(path, []byte(creds), 0o600)
}

func newClientsCmd() *cobra.Command {
	var af adminFlags
	cmd := &cobra.Command{Use: "clients", Short: "Manage clients and their credentials (admin API via the ssh tunnel)"}
	af.register(cmd)
	find := func(ctx context.Context, a *admin, ref string) (*clientDTO, error) {
		var cs []clientDTO
		if err := a.do(ctx, http.MethodGet, "/clients", nil, &cs); err != nil {
			return nil, err
		}
		for i := range cs {
			if cs[i].ID == ref || cs[i].Name == ref {
				return &cs[i], nil
			}
		}
		return nil, fmt.Errorf("no client %q", ref)
	}
	ls := &cobra.Command{
		Use:   "ls",
		Short: "List clients",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := af.client()
			if err != nil {
				return err
			}
			var cs []clientDTO
			if err := a.do(cmd.Context(), http.MethodGet, "/clients", nil, &cs); err != nil {
				return err
			}
			var rows [][]string
			for _, c := range cs {
				exp := c.ExpiresAt.Format("2006-01-02")
				if c.ExpiresSoon {
					exp += " (soon)"
				}
				var gs []string
				for _, g := range c.Grants {
					gs = append(gs, g.String())
				}
				rows = append(rows, []string{c.Name, c.ID, c.Status, fmt.Sprint(c.Connections), exp, strings.Join(gs, " ")})
			}
			return table(cmd.OutOrStdout(), "NAME\tID\tSTATUS\tCONNS\tEXPIRES\tGRANTS", rows)
		},
	}
	var grants []string
	var days int
	var out, desc string
	create := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a client; its .creds are written once (--out, default <name>.creds)",
		Example: `  pulse clients create door-bot --grant publish:evt.home.door.> --grant subscribe:evt.home.>
  pulse clients create indexer --grant durable:evt.>:indexer --out indexer.creds`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := af.client()
			if err != nil {
				return err
			}
			body := map[string]any{"name": args[0], "description": desc, "grants": []grant{}}
			var gs []grant
			for _, s := range grants {
				g, err := parseGrant(s)
				if err != nil {
					return err
				}
				gs = append(gs, g)
			}
			if gs != nil {
				body["grants"] = gs
			}
			if days > 0 {
				body["expires_in_days"] = days
			}
			if out == "" {
				out = args[0] + ".creds"
			}
			if _, err := os.Stat(out); err == nil && out != "-" {
				return fmt.Errorf("%s exists; not overwriting credentials", out)
			}
			var issued struct {
				Client clientDTO `json:"client"`
				Creds  string    `json:"creds"`
			}
			if err := a.do(cmd.Context(), http.MethodPost, "/clients", body, &issued); err != nil {
				return err
			}
			if err := writeCreds(out, issued.Creds); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "created %s (%s); credentials written to %s — they are not shown again\n", issued.Client.Name, issued.Client.ID, out)
			return nil
		},
	}
	create.Flags().StringArrayVar(&grants, "grant", nil, "action[:subject[:name]] (repeatable)")
	create.Flags().IntVar(&days, "days", 0, "credential lifetime in days (default 365)")
	create.Flags().StringVarP(&out, "out", "o", "", "write the .creds here (- for stdout)")
	create.Flags().StringVar(&desc, "description", "", "description")
	var rotOut string
	rotate := &cobra.Command{
		Use:   "rotate <name|id>",
		Short: "Issue new credentials and revoke the old ones",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := af.client()
			if err != nil {
				return err
			}
			c, err := find(cmd.Context(), a, args[0])
			if err != nil {
				return err
			}
			if rotOut == "" {
				rotOut = c.Name + ".creds"
			}
			if _, err := os.Stat(rotOut); err == nil && rotOut != "-" {
				return fmt.Errorf("%s exists; not overwriting credentials", rotOut)
			}
			var issued struct {
				Creds string `json:"creds"`
			}
			if err := a.do(cmd.Context(), http.MethodPost, "/clients/"+url.PathEscape(c.ID)+"/rotate", map[string]any{}, &issued); err != nil {
				return err
			}
			if err := writeCreds(rotOut, issued.Creds); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "rotated %s; new credentials in %s\n", c.Name, rotOut)
			return nil
		},
	}
	rotate.Flags().StringVarP(&rotOut, "out", "o", "", "write the .creds here (- for stdout)")
	var yes bool
	revoke := &cobra.Command{
		Use:   "revoke <name|id>",
		Short: "Revoke a client (disconnects it within seconds)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := af.client()
			if err != nil {
				return err
			}
			c, err := find(cmd.Context(), a, args[0])
			if err != nil {
				return err
			}
			if !yes {
				return fmt.Errorf("revoking %s cannot be undone; re-run with --yes", c.Name)
			}
			if err := a.do(cmd.Context(), http.MethodPost, "/clients/"+url.PathEscape(c.ID)+"/revoke", nil, nil); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "revoked %s\n", c.Name)
			return nil
		},
	}
	revoke.Flags().BoolVar(&yes, "yes", false, "confirm")
	cmd.AddCommand(ls, create, rotate, revoke)
	return cmd
}

type backupRecord struct {
	ID            string    `json:"id"`
	Trigger       string    `json:"trigger"`
	Status        string    `json:"status"`
	StartedAt     time.Time `json:"started_at"`
	DurationMs    int64     `json:"duration_ms"`
	Error         string    `json:"error"`
	LogicalBytes  int64     `json:"logical_bytes"`
	UploadedBytes int64     `json:"uploaded_bytes"`
	Verified      bool      `json:"verified"`
	Pruned        bool      `json:"pruned"`
}

type streamPlan struct {
	Name     string `json:"name"`
	Msgs     uint64 `json:"msgs"`
	FirstSeq uint64 `json:"first_seq"`
	LastSeq  uint64 `json:"last_seq"`
	Exists   bool   `json:"exists"`
	Action   string `json:"action"`
	Result   string `json:"result"`
}

func newBackupCmd() *cobra.Command {
	var af adminFlags
	cmd := &cobra.Command{Use: "backup", Short: "Backups: list, run, restore streams (admin API via the ssh tunnel)"}
	af.register(cmd)
	ls := &cobra.Command{
		Use:   "ls",
		Short: "List backups this server ran",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := af.client()
			if err != nil {
				return err
			}
			var ov struct {
				Enabled bool           `json:"enabled"`
				Records []backupRecord `json:"records"`
			}
			if err := a.do(cmd.Context(), http.MethodGet, "/backups", nil, &ov); err != nil {
				return err
			}
			if !ov.Enabled {
				return errors.New("backups are not configured on this server")
			}
			var rows [][]string
			for _, r := range ov.Records {
				st := r.Status
				if r.Status == "ok" && !r.Verified {
					st = "ok (unverified)"
				}
				if r.Pruned {
					st += " pruned"
				}
				rows = append(rows, []string{r.ID, r.StartedAt.Local().Format("2006-01-02 15:04"), st, (time.Duration(r.DurationMs) * time.Millisecond).String(),
					human(r.LogicalBytes), human(r.UploadedBytes), r.Error})
			}
			return table(cmd.OutOrStdout(), "ID\tSTARTED\tSTATUS\tDURATION\tSIZE\tUPLOADED\tERROR", rows)
		},
	}
	run := &cobra.Command{
		Use:   "run",
		Short: "Back up now (runs on the server in the background)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := af.client()
			if err != nil {
				return err
			}
			if err := a.do(cmd.Context(), http.MethodPost, "/backups/run", nil, nil); err != nil {
				return err
			}
			_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "backup started; `pulse backup ls` shows the result")
			return nil
		},
	}
	var identity string
	var streams []string
	var replace, dryRun bool
	restore := &cobra.Command{
		Use:   "restore <backup-id>",
		Short: "Restore streams from a backup with your age identity (it is sent for this request only)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if identity == "" {
				return errors.New("--identity is required (the age identity file)")
			}
			idb, err := os.ReadFile(identity) //nolint:gosec // operator-supplied path
			if err != nil {
				return err
			}
			a, err := af.client()
			if err != nil {
				return err
			}
			id := args[0]
			body := map[string]any{"identity": string(idb), "streams": streams, "replace": replace}
			var plan struct {
				Streams []streamPlan `json:"streams"`
			}
			if err := a.do(cmd.Context(), http.MethodPost, "/backups/"+url.PathEscape(id)+"/plan", body, &plan); err != nil {
				return err
			}
			printPlan := func(ps []streamPlan) error {
				var rows [][]string
				for _, p := range ps {
					rows = append(rows, []string{p.Name, fmt.Sprint(p.Msgs), fmt.Sprintf("%d–%d", p.FirstSeq, p.LastSeq), fmt.Sprint(p.Exists), p.Action, p.Result})
				}
				return table(cmd.OutOrStdout(), "STREAM\tMSGS\tSEQ\tEXISTS\tACTION\tRESULT", rows)
			}
			if dryRun {
				return printPlan(plan.Streams)
			}
			q := ""
			if replace {
				q = "?confirm=" + url.QueryEscape(id)
			}
			if err := a.do(cmd.Context(), http.MethodPost, "/backups/"+url.PathEscape(id)+"/restore"+q, body, nil); err != nil {
				return err
			}
			for {
				var ov struct {
					LastRestore *struct {
						Status  string       `json:"status"`
						Error   string       `json:"error"`
						Streams []streamPlan `json:"streams"`
					} `json:"last_restore"`
				}
				if err := a.do(cmd.Context(), http.MethodGet, "/backups", nil, &ov); err != nil {
					return err
				}
				if lr := ov.LastRestore; lr != nil && lr.Status != "running" {
					if err := printPlan(lr.Streams); err != nil {
						return err
					}
					if lr.Status == "failed" {
						return errors.New(lr.Error)
					}
					return nil
				}
				select {
				case <-cmd.Context().Done():
					return cmd.Context().Err()
				case <-time.After(time.Second):
				}
			}
		},
	}
	restore.Flags().StringVar(&identity, "identity", "", "age identity file")
	restore.Flags().StringSliceVar(&streams, "streams", nil, "streams to restore (default: all in the backup)")
	restore.Flags().BoolVar(&replace, "replace", false, "delete existing streams first")
	restore.Flags().BoolVar(&dryRun, "dry-run", false, "show what would be restored")
	cmd.AddCommand(ls, run, restore)
	return cmd
}
