package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/aurimasniekis/go-pulse/internal/audit"
	"github.com/aurimasniekis/go-pulse/internal/backup"
)

// BackupStore describes where backups go (no credentials).
type BackupStore struct {
	Endpoint string `json:"endpoint"`
	Bucket   string `json:"bucket"`
	Prefix   string `json:"prefix"`
}

// BackupsOverview is the Backups page payload (PL-BAK-008).
type BackupsOverview struct {
	Enabled     bool                   `json:"enabled"`
	Store       BackupStore            `json:"store"`
	Settings    *backup.BackupSettings `json:"settings,omitempty"`
	Records     []backup.BackupRecord  `json:"records"`
	LastRestore *backup.RestoreJob     `json:"last_restore,omitempty"`
}

// RemoteBackup is a complete backup found in the object store.
type RemoteBackup struct {
	ID           string    `json:"id"`
	Created      time.Time `json:"created"`
	LogicalBytes int64     `json:"logical_bytes"`
	Chunks       int       `json:"chunks"`
}

// IdentityInput carries the age identity for a plan or restore. It is used for this request only: never stored or
// logged (PL-BAK-002).
type IdentityInput struct {
	Identity string `json:"identity" writeOnly:"true" minLength:"10" doc:"age identity (AGE-SECRET-KEY-1…), used for this request only"`
}

// RestoreInput selects what to restore.
type RestoreInput struct {
	IdentityInput
	Streams []string `json:"streams,omitempty" doc:"Streams to restore; empty restores every stream in the backup"`
	Replace bool     `json:"replace,omitempty" doc:"Delete existing streams first (needs ?confirm=<backup id>)"`
}

// BackupPlan is a dry-run listing (PL-BAK-006).
type BackupPlan struct {
	ID            string              `json:"id"`
	Created       time.Time           `json:"created"`
	PulsedVersion string              `json:"pulsed_version"`
	Files         []BackupFile        `json:"files"`
	Streams       []backup.StreamPlan `json:"streams"`
}

// BackupFile is a whole-file object in a backup (restored offline with `pulsed backup restore --files`).
type BackupFile struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

var errNoBackups = huma.Error503ServiceUnavailable("backups are not configured (PULSE_BACKUP_ENABLED)")

func backupErr(d *Deps, op string, err error) error {
	switch {
	case errors.Is(err, backup.ErrBusy):
		return huma.Error409Conflict(err.Error())
	case errors.Is(err, backup.ErrDecrypt), errors.Is(err, backup.ErrTampered):
		return huma.Error422UnprocessableEntity(err.Error())
	default:
		return d.internalErr(op, err)
	}
}

func registerBackups(api huma.API, d *Deps) {
	tags := []string{"backups"}
	need := func() error {
		if d.Backups == nil {
			return errNoBackups
		}
		return nil
	}
	huma.Register(api, huma.Operation{OperationID: "get-backups", Method: http.MethodGet, Path: "/backups", Summary: "Backup catalog and settings", Tags: tags},
		func(ctx context.Context, _ *struct{}) (*struct{ Body BackupsOverview }, error) {
			out := BackupsOverview{Records: []backup.BackupRecord{}, Store: d.BackupStore}
			if d.Backups == nil {
				return &struct{ Body BackupsOverview }{out}, nil
			}
			recs, err := d.Backups.List(ctx, 100)
			if err != nil {
				return nil, backupErr(d, "get-backups", err)
			}
			st := d.Backups.Settings()
			out.Enabled, out.Records, out.Settings, out.LastRestore = true, recs, &st, d.Backups.LastRestore()
			return &struct{ Body BackupsOverview }{out}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "run-backup", Method: http.MethodPost, Path: "/backups/run", Summary: "Back up now (runs in the background)",
		Tags: tags, DefaultStatus: http.StatusAccepted},
		func(ctx context.Context, _ *struct{}) (*struct{}, error) {
			if err := need(); err != nil {
				return nil, err
			}
			if err := d.Backups.StartBackup(ctx, "manual"); err != nil {
				return nil, backupErr(d, "run-backup", err)
			}
			_ = audit.Write(ctx, d.DB, d.actor(ctx), "backup.run", "", audit.OK, nil)
			return &struct{}{}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "prune-backups", Method: http.MethodPost, Path: "/backups/prune", Summary: "Apply retention now", Tags: tags},
		func(ctx context.Context, _ *struct{}) (*struct{}, error) {
			if err := need(); err != nil {
				return nil, err
			}
			err := d.Backups.Prune(ctx)
			_ = audit.Write(ctx, d.DB, d.actor(ctx), "backup.prune", "", auditResult(err), nil)
			if err != nil {
				return nil, backupErr(d, "prune-backups", err)
			}
			return &struct{}{}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "list-remote-backups", Method: http.MethodGet, Path: "/backups/remote",
		Summary: "Complete backups in the object store (also those made by an earlier server)", Tags: tags},
		func(ctx context.Context, _ *struct{}) (*struct{ Body []RemoteBackup }, error) {
			if err := need(); err != nil {
				return nil, err
			}
			refs, err := d.Backups.Remote(ctx)
			if err != nil {
				return nil, backupErr(d, "list-remote-backups", err)
			}
			out := []RemoteBackup{}
			for _, r := range refs {
				out = append(out, RemoteBackup{ID: r.ID, Created: r.Created, LogicalBytes: r.LogicalBytes, Chunks: len(r.Chunks)})
			}
			return &struct{ Body []RemoteBackup }{out}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "plan-restore", Method: http.MethodPost, Path: "/backups/{id}/plan", Summary: "Dry run: what a restore would do",
		Tags: tags},
		func(ctx context.Context, in *struct {
			ID   string `path:"id"`
			Body RestoreInput
		}) (*struct{ Body BackupPlan }, error) {
			if err := need(); err != nil {
				return nil, err
			}
			ids, err := backup.ParseIdentities(in.Body.Identity)
			if err != nil {
				return nil, huma.Error422UnprocessableEntity(err.Error())
			}
			m, plan, err := d.Backups.Plan(ctx, ids, in.ID, backup.RestoreOptions{Streams: in.Body.Streams, Replace: in.Body.Replace})
			if err != nil {
				return nil, backupErr(d, "plan-restore", err)
			}
			out := BackupPlan{ID: m.ID, Created: m.Created, PulsedVersion: m.PulsedVersion, Files: []BackupFile{}, Streams: plan}
			for _, f := range m.Files {
				out.Files = append(out.Files, BackupFile{Name: f.Name, Size: f.Size})
			}
			return &struct{ Body BackupPlan }{out}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "restore-backup", Method: http.MethodPost, Path: "/backups/{id}/restore",
		Summary: "Restore streams from a backup (runs in the background)", Tags: tags, DefaultStatus: http.StatusAccepted},
		func(ctx context.Context, in *struct {
			ID      string `path:"id"`
			Confirm string `query:"confirm" doc:"Must repeat the backup id when replace is set"`
			Body    RestoreInput
		}) (*struct{ Body *backup.RestoreJob }, error) {
			if err := need(); err != nil {
				return nil, err
			}
			if in.Body.Replace && in.Confirm != in.ID {
				return nil, huma.Error400BadRequest("replace deletes existing streams: repeat the backup id in ?confirm=")
			}
			ids, err := backup.ParseIdentities(in.Body.Identity)
			if err != nil {
				return nil, huma.Error422UnprocessableEntity(err.Error())
			}
			job, err := d.Backups.StartRestore(ctx, ids, in.ID, backup.RestoreOptions{Streams: in.Body.Streams, Replace: in.Body.Replace})
			_ = audit.Write(ctx, d.DB, d.actor(ctx), "backup.restore", in.ID, auditResult(err), map[string]any{"streams": in.Body.Streams, "replace": in.Body.Replace})
			if err != nil {
				return nil, backupErr(d, "restore-backup", err)
			}
			return &struct{ Body *backup.RestoreJob }{job}, nil
		})
}
