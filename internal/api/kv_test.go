package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/aurimasniekis/go-pulse/internal/streams"
)

func TestKVAndObjects(t *testing.T) {
	h := newHarness(t)
	base := h.admin.URL + AdminPrefix
	csrf := csrfH
	if resp, body := do(t, http.MethodPost, base+"/kv", `{"name":"settings","history":5}`, csrf); resp.StatusCode != http.StatusCreated {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if resp, _ := do(t, http.MethodPost, base+"/kv", `{"name":"settings"}`, csrf); resp.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate bucket: %d", resp.StatusCode)
	}
	for _, v := range []string{"night", "day"} {
		if resp, body := do(t, http.MethodPut, base+"/kv/settings/entry?key=ui.mode", `{"value":"`+v+`"}`, csrf); resp.StatusCode != http.StatusOK {
			t.Fatalf("%d %s", resp.StatusCode, body)
		}
	}
	_, _ = do(t, http.MethodPut, base+"/kv/settings/entry?key=bin", `{"value":"AAEC/w==","encoding":"base64"}`, csrf)
	resp, body := do(t, http.MethodGet, base+"/kv/settings/keys", "", nil)
	var keys KVKeys
	_ = json.Unmarshal([]byte(body), &keys)
	if resp.StatusCode != http.StatusOK || len(keys.Keys) != 2 {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	_, body = do(t, http.MethodGet, base+"/kv/settings/entry?key=ui.mode", "", nil)
	var e KVEntry
	_ = json.Unmarshal([]byte(body), &e)
	if e.Value != "day" || e.Encoding != "utf8" || len(e.History) != 2 || e.History[0].Revision <= e.History[1].Revision {
		t.Fatalf("%s", body)
	}
	_, body = do(t, http.MethodGet, base+"/kv/settings/entry?key=bin", "", nil)
	if !strings.Contains(body, `"encoding":"base64"`) || !strings.Contains(body, `"AAEC/w=="`) {
		t.Fatalf("%s", body)
	}
	if resp, _ := do(t, http.MethodDelete, base+"/kv/settings/entry?key=ui.mode", "", csrf); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete key: %d", resp.StatusCode)
	}
	_, body = do(t, http.MethodGet, base+"/kv/settings/keys", "", nil)
	if strings.Contains(body, "ui.mode") {
		t.Fatalf("deleted key listed: %s", body)
	}
	if resp, _ := do(t, http.MethodGet, base+"/kv/settings/entry?key=nope", "", nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing key: %d", resp.StatusCode)
	}

	// Objects.
	if resp, body := do(t, http.MethodPost, base+"/objects", `{"name":"files"}`, csrf); resp.StatusCode != http.StatusCreated {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	req, _ := http.NewRequest(http.MethodPut, base+"/objects/files/upload?name=docs/readme.txt", strings.NewReader("hello object"))
	req.Header.Set("Content-Type", "application/octet-stream")
	up, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	upBody, _ := io.ReadAll(up.Body)
	up.Body.Close()
	if up.StatusCode != http.StatusOK || !strings.Contains(string(upBody), `"size":12`) {
		t.Fatalf("upload %d %s", up.StatusCode, upBody)
	}
	_, body = do(t, http.MethodGet, base+"/objects/files", "", nil)
	if !strings.Contains(body, "docs/readme.txt") {
		t.Fatal(body)
	}
	dl, err := http.Get(base + "/objects/files/download?name=docs/readme.txt")
	if err != nil {
		t.Fatal(err)
	}
	content, _ := io.ReadAll(dl.Body)
	dl.Body.Close()
	if string(content) != "hello object" || !strings.Contains(dl.Header.Get("Content-Disposition"), "attachment") || dl.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("download %q %v", content, dl.Header)
	}
	if resp, _ := do(t, http.MethodDelete, base+"/objects/files/object?name=docs/readme.txt", "", csrf); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete object: %d", resp.StatusCode)
	}
	_, body = do(t, http.MethodGet, base+"/kv", "", nil)
	if !strings.Contains(body, `"name":"settings"`) || !strings.Contains(body, `"name":"files"`) {
		t.Fatal(body)
	}
	if resp, _ := do(t, http.MethodDelete, base+"/kv/settings", "", csrf); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("delete without confirm: %d", resp.StatusCode)
	}
	if resp, _ := do(t, http.MethodDelete, base+"/kv/settings?confirm=settings", "", csrf); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete bucket: %d", resp.StatusCode)
	}
}

func TestBlobsAreReadOnly(t *testing.T) {
	h := newHarness(t)
	base := h.admin.URL + AdminPrefix
	csrf := csrfH
	if err := ensureBlobs(h); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ method, path string }{
		{http.MethodDelete, "/objects/blobs?confirm=blobs"},
		{http.MethodDelete, "/objects/blobs/object?name=x"},
	} {
		if resp, _ := do(t, c.method, base+c.path, "", csrf); resp.StatusCode != http.StatusConflict {
			t.Errorf("%s %s: %d", c.method, c.path, resp.StatusCode)
		}
	}
	req, _ := http.NewRequest(http.MethodPut, base+"/objects/blobs/upload?name=x", strings.NewReader("data"))
	req.Header.Set("Content-Type", "application/octet-stream")
	up, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	up.Body.Close()
	if up.StatusCode != http.StatusConflict {
		t.Errorf("upload to blobs: %d", up.StatusCode)
	}
	_, body := do(t, http.MethodGet, base+"/kv", "", nil)
	if !strings.Contains(body, `"managed":true`) {
		t.Fatal(body)
	}
}

func ensureBlobs(h *harness) error {
	return streams.EnsureBlobs(context.Background(), h.deps.JS)
}

func TestBucketConfig(t *testing.T) {
	h := newHarness(t)
	base := h.admin.URL + AdminPrefix
	decode := func(body string, v any) {
		t.Helper()
		if err := json.Unmarshal([]byte(body), v); err != nil {
			t.Fatalf("%v: %s", err, body)
		}
	}

	// KV: create, read, update, read back.
	resp, body := do(t, http.MethodPost, base+"/kv", `{"name":"cfg","description":"app settings","history":2}`, csrfH)
	var kb KVBucket
	decode(body, &kb)
	if resp.StatusCode != http.StatusCreated || kb.Description != "app settings" || kb.History != 2 || kb.MaxBytes != -1 {
		t.Fatalf("create kv: %d %s", resp.StatusCode, body)
	}
	resp, body = do(t, http.MethodGet, base+"/kv/cfg/config", "", nil)
	kb = KVBucket{}
	decode(body, &kb)
	if resp.StatusCode != http.StatusOK || kb.Name != "cfg" || kb.Description != "app settings" || kb.History != 2 || kb.TTLSeconds != 0 || kb.MaxBytes != -1 {
		t.Fatalf("get kv: %d %s", resp.StatusCode, body)
	}
	resp, body = do(t, http.MethodPut, base+"/kv/cfg/config", `{"description":"settings v2","history":5,"ttl_seconds":3600,"max_bytes":1048576}`, csrfH)
	kb = KVBucket{}
	decode(body, &kb)
	want := KVBucket{Name: "cfg", Description: "settings v2", History: 5, TTLSeconds: 3600, MaxBytes: 1 << 20}
	if resp.StatusCode != http.StatusOK || kb != want {
		t.Fatalf("update kv: %d %s", resp.StatusCode, body)
	}
	_, body = do(t, http.MethodGet, base+"/kv/cfg/config", "", nil)
	kb = KVBucket{}
	decode(body, &kb)
	if kb != want {
		t.Fatalf("kv after update: %s", body)
	}
	_, body = do(t, http.MethodGet, base+"/kv", "", nil)
	if !strings.Contains(body, `"description":"settings v2"`) || !strings.Contains(body, `"max_bytes":1048576`) {
		t.Fatalf("list-buckets: %s", body)
	}
	// Omitted fields reset to their defaults (max_bytes 0 means unlimited).
	_, body = do(t, http.MethodPut, base+"/kv/cfg/config", `{}`, csrfH)
	kb = KVBucket{}
	decode(body, &kb)
	if kb != (KVBucket{Name: "cfg", History: 1, MaxBytes: -1}) {
		t.Fatalf("kv reset: %s", body)
	}
	for _, c := range []struct {
		method, path, body string
		status             int
	}{
		{http.MethodGet, "/kv/nope/config", "", http.StatusNotFound},
		{http.MethodPut, "/kv/nope/config", `{}`, http.StatusNotFound},
		{http.MethodPut, "/kv/cfg/config", `{"history":65}`, http.StatusUnprocessableEntity},
		{http.MethodPut, "/kv/cfg/config", `{"max_bytes":-2}`, http.StatusUnprocessableEntity},
	} {
		if resp, body := do(t, c.method, base+c.path, c.body, csrfH); resp.StatusCode != c.status {
			t.Errorf("%s %s %s: %d %s", c.method, c.path, c.body, resp.StatusCode, body)
		}
	}

	// Object stores.
	resp, body = do(t, http.MethodPost, base+"/objects", `{"name":"files","description":"uploads","max_bytes":4096}`, csrfH)
	var ob ObjectBucket
	decode(body, &ob)
	if resp.StatusCode != http.StatusCreated || ob.Description != "uploads" || ob.MaxBytes != 4096 {
		t.Fatalf("create object store: %d %s", resp.StatusCode, body)
	}
	resp, body = do(t, http.MethodGet, base+"/objects/files/config", "", nil)
	ob = ObjectBucket{}
	decode(body, &ob)
	if resp.StatusCode != http.StatusOK || ob.Name != "files" || ob.Description != "uploads" || ob.MaxBytes != 4096 || ob.TTLSeconds != 0 || ob.Managed {
		t.Fatalf("get object store: %d %s", resp.StatusCode, body)
	}
	resp, body = do(t, http.MethodPut, base+"/objects/files/config", `{"description":"uploads v2","ttl_seconds":86400,"max_bytes":8192}`, csrfH)
	ob = ObjectBucket{}
	decode(body, &ob)
	if resp.StatusCode != http.StatusOK || ob.Description != "uploads v2" || ob.TTLSeconds != 86400 || ob.MaxBytes != 8192 {
		t.Fatalf("update object store: %d %s", resp.StatusCode, body)
	}
	_, body = do(t, http.MethodGet, base+"/objects/files/config", "", nil)
	ob = ObjectBucket{}
	decode(body, &ob)
	if ob.Description != "uploads v2" || ob.TTLSeconds != 86400 || ob.MaxBytes != 8192 {
		t.Fatalf("object store after update: %s", body)
	}
	if info, err := h.deps.JS.Stream(context.Background(), "OBJ_files"); err != nil || info.CachedInfo().Config.Compression != jetstream.S2Compression {
		t.Fatalf("update dropped compression: %v", err)
	}
	_, body = do(t, http.MethodGet, base+"/kv", "", nil)
	if !strings.Contains(body, `"max_bytes":8192`) {
		t.Fatalf("list-buckets objects: %s", body)
	}
	if resp, _ := do(t, http.MethodGet, base+"/objects/nope/config", "", nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing object store: %d", resp.StatusCode)
	}
	if resp, _ := do(t, http.MethodPut, base+"/objects/nope/config", `{}`, csrfH); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("update missing object store: %d", resp.StatusCode)
	}

	// The pulsed-managed blobs store can be read but not reconfigured.
	if err := ensureBlobs(h); err != nil {
		t.Fatal(err)
	}
	if resp, body := do(t, http.MethodGet, base+"/objects/blobs/config", "", nil); resp.StatusCode != http.StatusOK || !strings.Contains(body, `"managed":true`) {
		t.Fatalf("get blobs: %d %s", resp.StatusCode, body)
	}
	if resp, body := do(t, http.MethodPut, base+"/objects/blobs/config", `{"ttl_seconds":60}`, csrfH); resp.StatusCode != http.StatusConflict {
		t.Fatalf("update blobs: %d %s", resp.StatusCode, body)
	}

	if auditCount(t, h, "kv.update", "cfg") != 2 || auditCount(t, h, "object.update_bucket", "files") != 1 {
		t.Fatal("missing update audit entries")
	}
}
