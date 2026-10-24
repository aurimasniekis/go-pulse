package backup

import (
	"context"
	"sync"
	"time"

	"filippo.io/age"
)

// RestoreJob is the state of the last restore started from the dashboard or API.
type RestoreJob struct {
	BackupID   string       `json:"backup_id"`
	Status     string       `json:"status" enum:"running,ok,failed"`
	StartedAt  time.Time    `json:"started_at"`
	FinishedAt time.Time    `json:"finished_at,omitzero"`
	Replace    bool         `json:"replace"`
	Streams    []StreamPlan `json:"streams"`
	Error      string       `json:"error,omitempty"`
}

type jobs struct {
	mu      sync.Mutex
	restore *RestoreJob
}

// Reader returns a reader for this service's store with the given identities.
func (s *Service) Reader(ids []age.Identity) *Reader {
	return &Reader{Store: s.o.Store, Identities: ids, NameKey: s.o.NameKey}
}

// StartBackup runs a backup in the background (the dashboard's "Back up now").
func (s *Service) StartBackup(ctx context.Context, trigger string) error {
	if !s.mu.TryLock() {
		return ErrBusy
	}
	s.mu.Unlock() // Backup takes the lock itself; a racing start just gets ErrBusy there and is logged
	go func() {
		if _, err := s.Backup(context.WithoutCancel(ctx), trigger); err != nil {
			s.o.Log.Error("backup failed", "err", err)
		}
	}()
	return nil
}

// Plan is a dry run: what restoring backupID with o would do.
func (s *Service) Plan(ctx context.Context, ids []age.Identity, backupID string, o RestoreOptions) (*Manifest, []StreamPlan, error) {
	r := s.Reader(ids)
	m, err := r.Manifest(ctx, backupID)
	if err != nil {
		return nil, nil, err
	}
	o.DryRun = true
	plan, err := r.RestoreStreams(ctx, s.nc, s.js, m, o)
	return m, plan, err
}

// StartRestore restores streams in the background. The identities live only in this job's goroutine.
func (s *Service) StartRestore(ctx context.Context, ids []age.Identity, backupID string, o RestoreOptions) (*RestoreJob, error) {
	r := s.Reader(ids)
	m, err := r.Manifest(ctx, backupID)
	if err != nil {
		return nil, err
	}
	if err := s.lock("restore"); err != nil {
		return nil, err
	}
	job := &RestoreJob{BackupID: backupID, Status: "running", StartedAt: s.o.Now().UTC(), Replace: o.Replace, Streams: []StreamPlan{}}
	s.jobs.mu.Lock()
	s.jobs.restore = job
	s.jobs.mu.Unlock()
	go func() {
		defer s.unlock()
		res, err := r.RestoreStreams(context.WithoutCancel(ctx), s.nc, s.js, m, o)
		s.jobs.mu.Lock()
		defer s.jobs.mu.Unlock()
		job.FinishedAt, job.Streams = s.o.Now().UTC(), res
		if job.Streams == nil {
			job.Streams = []StreamPlan{}
		}
		if err != nil {
			job.Status, job.Error = "failed", err.Error()
			s.o.Log.Error("restore failed", "backup", backupID, "err", err)
			return
		}
		job.Status = "ok"
		s.o.Log.Info("restore done", "backup", backupID, "streams", len(res))
	}()
	return s.LastRestore(), nil
}

// LastRestore returns a copy of the last restore job (nil when none ran).
func (s *Service) LastRestore() *RestoreJob {
	s.jobs.mu.Lock()
	defer s.jobs.mu.Unlock()
	if s.jobs.restore == nil {
		return nil
	}
	j := *s.jobs.restore
	j.Streams = append([]StreamPlan(nil), j.Streams...)
	return &j
}

// BackupSettings describes the configuration for the Backups page.
type BackupSettings struct {
	At      string    `json:"at" doc:"Daily schedule, HH:MM UTC"`
	NextRun time.Time `json:"next_run"`
	Keep    struct {
		Daily   int `json:"daily"`
		Weekly  int `json:"weekly"`
		Monthly int `json:"monthly"`
	} `json:"keep"`
	Recipients []string `json:"recipients" doc:"age recipients backups are encrypted to"`
	Running    string   `json:"running,omitempty" doc:"backup, prune or restore while one runs"`
}

// Settings returns the schedule and retention.
func (s *Service) Settings() BackupSettings {
	var st BackupSettings
	st.At, st.NextRun = s.o.At, nextRun(s.o.Now(), s.o.At)
	st.Keep.Daily, st.Keep.Weekly, st.Keep.Monthly = s.o.Keep.Daily, s.o.Keep.Weekly, s.o.Keep.Monthly
	st.Recipients = []string{}
	for _, r := range s.o.Recipients {
		if x, ok := r.(*age.X25519Recipient); ok {
			st.Recipients = append(st.Recipients, x.String())
		}
	}
	st.Running = s.Running()
	return st
}
