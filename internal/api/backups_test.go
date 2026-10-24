package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"filippo.io/age"

	"github.com/aurimasniekis/go-pulse/internal/backup"
)

func TestBackupsAPI(t *testing.T) {
	h := newHarness(t)
	base := h.admin.URL + AdminPrefix
	csrf := csrfH
	// Disabled: the page says so, actions return 503.
	_, body := do(t, http.MethodGet, base+"/backups", "", nil)
	if !strings.Contains(body, `"enabled":false`) {
		t.Fatal(body)
	}
	if resp, _ := do(t, http.MethodPost, base+"/backups/run", "", csrf); resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("run while disabled: %d", resp.StatusCode)
	}

	id, _ := age.GenerateX25519Identity()
	key, _ := backup.NameKey(bytes.Repeat([]byte{1}, 32))
	h.deps.Backups = backup.New(h.db, h.deps.NC, h.deps.JS, backup.Options{Store: backup.NewMem(), Recipients: []age.Recipient{id.Recipient()},
		NameKey: key, TempDir: t.TempDir()})
	srv := httptest.NewServer(NewAdmin(h.deps))
	defer srv.Close()
	base = srv.URL + AdminPrefix
	if resp, body := do(t, http.MethodPost, base+"/backups/run", "", csrf); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	var ov BackupsOverview
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		_, body = do(t, http.MethodGet, base+"/backups", "", nil)
		_ = json.Unmarshal([]byte(body), &ov)
		if len(ov.Records) == 1 && ov.Records[0].Status != "running" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if len(ov.Records) != 1 || ov.Records[0].Status != "ok" || !ov.Records[0].Verified || ov.Settings == nil || len(ov.Settings.Recipients) != 1 {
		t.Fatalf("%s", body)
	}
	bid := ov.Records[0].ID
	ident, _ := json.Marshal(map[string]any{"identity": id.String()})
	resp, body := do(t, http.MethodPost, base+"/backups/"+bid+"/plan", string(ident), csrf)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"name":"EVENTS"`) || !strings.Contains(body, `"pulse.db"`) {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	other, _ := age.GenerateX25519Identity()
	wrong, _ := json.Marshal(map[string]any{"identity": other.String()})
	if resp, _ := do(t, http.MethodPost, base+"/backups/"+bid+"/plan", string(wrong), csrf); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("wrong identity: %d", resp.StatusCode)
	}
	repl, _ := json.Marshal(map[string]any{"identity": id.String(), "replace": true, "streams": []string{"EVENTS"}})
	if resp, _ := do(t, http.MethodPost, base+"/backups/"+bid+"/restore", string(repl), csrf); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("replace without confirm: %d", resp.StatusCode)
	}
	if strings.Contains(body, id.String()) {
		t.Fatal("identity echoed")
	}
	_, body = do(t, http.MethodGet, base+"/backups/remote", "", nil)
	if !strings.Contains(body, bid) {
		t.Fatal(body)
	}
}
