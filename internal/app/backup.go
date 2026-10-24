package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/aurimasniekis/go-pulse/internal/alerts"
	"github.com/aurimasniekis/go-pulse/internal/api"
	"github.com/aurimasniekis/go-pulse/internal/backup"
	"github.com/aurimasniekis/go-pulse/internal/config"
)

// backupStore builds the S3 store from configuration (shared with `pulsed backup`).
func backupStore(cfg *config.Config) (*backup.S3, error) {
	var ca []byte
	if cfg.BackupS3CAFile != "" {
		var err error
		if ca, err = os.ReadFile(cfg.BackupS3CAFile); err != nil {
			return nil, fmt.Errorf("PULSE_BACKUP_S3_CA_FILE: %w", err)
		}
	}
	return backup.NewS3(backup.S3Options{Endpoint: cfg.BackupS3Endpoint, Bucket: cfg.BackupS3Bucket, Prefix: cfg.BackupS3Prefix,
		Region: cfg.BackupS3Region, AccessKey: cfg.BackupS3AccessKey, SecretKey: cfg.BackupS3SecretKey, Insecure: cfg.BackupS3Insecure,
		Lookup: cfg.BackupS3Lookup, CAPEM: ca, CreateBucket: cfg.BackupS3CreateBucket})
}

// BackupOptions returns the service options for cfg (shared with `pulsed backup`).
func BackupOptions(cfg *config.Config) (backup.Options, error) {
	st, err := backupStore(cfg)
	if err != nil {
		return backup.Options{}, err
	}
	rs, err := backup.ParseRecipients(cfg.BackupAgeRecipients)
	if err != nil {
		return backup.Options{}, err
	}
	key, err := backup.NameKey(cfg.MasterKey)
	if err != nil {
		return backup.Options{}, err
	}
	tmp := filepath.Join(cfg.Layout.DataDir, "tmp")
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		return backup.Options{}, err
	}
	return backup.Options{Store: st, Recipients: rs, NameKey: key, At: cfg.BackupAt, KeysFile: cfg.Layout.KeysFile(), TempDir: tmp,
		Keep: backup.Keep{Daily: cfg.BackupKeepDaily, Weekly: cfg.BackupKeepWeekly, Monthly: cfg.BackupKeepMonthly}, Version: Version}, nil
}

// setupBackups starts the nightly backup (PL-BAK-001) and returns the backup_stale alert provider.
func (a *App) setupBackups(cfg *config.Config, deps *api.Deps) (func(context.Context) (alerts.BackupStatus, bool), error) {
	if !cfg.BackupEnabled {
		return nil, nil
	}
	o, err := BackupOptions(cfg)
	if err != nil {
		return nil, err
	}
	o.Log = a.Log.With("component", "backup")
	a.Backups = backup.New(a.DB, a.AppConn, a.JS, o)
	deps.Backups = a.Backups
	deps.BackupStore = api.BackupStore{Endpoint: cfg.BackupS3Endpoint, Bucket: cfg.BackupS3Bucket, Prefix: cfg.BackupS3Prefix}
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		a.Backups.Run(a.loopCtx) //nolint:contextcheck // runs until Close
	}()
	since := time.Now()
	return func(ctx context.Context) (alerts.BackupStatus, bool) {
		last, lastErr, ok := a.Backups.LastStatus(ctx)
		if !ok {
			return alerts.BackupStatus{}, false
		}
		if last.IsZero() && lastErr == "" {
			last = since // nothing ran yet on this server: measure staleness from start, not from the epoch
		}
		return alerts.BackupStatus{LastSuccess: last, LastError: lastErr}, true
	}, nil
}
