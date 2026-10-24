package backup

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/aurimasniekis/go-pulse/internal/store"
)

// Remote lists complete backups from the object store, newest first (works without the catalog, e.g. on a new server).
func (s *Service) Remote(ctx context.Context) ([]Refs, error) {
	objs, err := s.o.Store.List(ctx, backupPrefix)
	if err != nil {
		return nil, err
	}
	var out []Refs
	for _, o := range objs {
		if !strings.HasSuffix(o.Key, "/refs.json") {
			continue
		}
		b, err := s.o.Store.Get(ctx, o.Key)
		if err != nil {
			return nil, err
		}
		var r Refs
		if err := json.Unmarshal(b, &r); err != nil {
			return nil, fmt.Errorf("%s: %w", o.Key, err)
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out, nil
}

// keepSet applies GFS (PL-BAK-005): the newest backup of each of the last Daily days, Weekly ISO weeks and Monthly
// months; the newest backup is always kept. backups must be newest first.
func keepSet(backups []Refs, k Keep) map[string]bool {
	keep := map[string]bool{}
	if len(backups) == 0 {
		return keep
	}
	keep[backups[0].ID] = true
	pick := func(n int, bucket func(time.Time) string) {
		seen := map[string]bool{}
		for _, b := range backups {
			key := bucket(b.Created.UTC())
			if seen[key] {
				continue
			}
			if len(seen) == n {
				return
			}
			seen[key] = true
			keep[b.ID] = true
		}
	}
	pick(k.Daily, func(t time.Time) string { return t.Format("2006-01-02") })
	pick(k.Weekly, func(t time.Time) string { y, w := t.ISOWeek(); return fmt.Sprintf("%d-W%02d", y, w) })
	pick(k.Monthly, func(t time.Time) string { return t.Format("2006-01") })
	return keep
}

// Prune applies retention now.
func (s *Service) Prune(ctx context.Context) error {
	if err := s.lock("prune"); err != nil {
		return err
	}
	defer s.unlock()
	return s.prune(ctx)
}

// prune deletes backups outside the GFS set, then chunks no remaining backup references, and leftovers of runs that
// never completed (no refs.json) once they are a day old.
func (s *Service) prune(ctx context.Context) error {
	backups, err := s.Remote(ctx)
	if err != nil {
		return err
	}
	keep := keepSet(backups, s.o.Keep)
	var errs []error
	live := map[string]bool{}
	for _, b := range backups {
		if keep[b.ID] {
			for _, c := range b.Chunks {
				live[c] = true
			}
			continue
		}
		for _, k := range b.Objects {
			if err := s.o.Store.Delete(ctx, k); err != nil {
				errs = append(errs, err)
			}
		}
		if err := s.o.Store.Delete(ctx, refsKey(b.ID)); err != nil {
			errs = append(errs, err)
			continue
		}
		s.markPruned(ctx, b.ID)
		s.o.Log.Info("backup pruned", "id", b.ID)
	}
	complete := map[string]bool{}
	for _, b := range backups {
		complete[b.ID] = true
	}
	objs, err := s.o.Store.List(ctx, "")
	if err != nil {
		return err
	}
	cut := s.o.Now().Add(-24 * time.Hour)
	for _, o := range objs {
		switch {
		case strings.HasPrefix(o.Key, chunkPrefix):
			if !live[strings.TrimPrefix(o.Key, chunkPrefix)] && o.ModTime.Before(cut) {
				if err := s.o.Store.Delete(ctx, o.Key); err != nil {
					errs = append(errs, err)
				}
			}
		case strings.HasPrefix(o.Key, backupPrefix):
			id, _, _ := strings.Cut(strings.TrimPrefix(o.Key, backupPrefix), "/")
			if !complete[id] && o.ModTime.Before(cut) {
				if err := s.o.Store.Delete(ctx, o.Key); err != nil {
					errs = append(errs, err)
				}
			}
		}
	}
	return errors.Join(errs...)
}

func (s *Service) markPruned(ctx context.Context, id string) {
	_ = s.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE backups SET pruned_at = ? WHERE id = ?`, store.Ms(s.o.Now()), id)
		return err
	})
}
