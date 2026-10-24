package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

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
