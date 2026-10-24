package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aurimasniekis/go-pulse/internal/alerts"
	"github.com/aurimasniekis/go-pulse/internal/notify"
)

type okSender struct{}

func (okSender) Kind() string                               { return "discord" }
func (okSender) Send(context.Context, notify.Message) error { return nil }

func TestAlertsAPI(t *testing.T) {
	h := newHarness(t)
	n := notify.New("discord", okSender{}, notify.Options{})
	h.deps.Alerts = alerts.New(h.db, alerts.Providers{JS: h.deps.JS, Sys: h.deps.Sys, Rollup: h.deps.Rollup,
		Certs: func() []alerts.Cert { return []alerts.Cert{{Name: "origin", NotAfter: time.Now().Add(48 * time.Hour)}} }},
		alerts.Options{Notifiers: []*notify.Notifier{n}})
	if err := h.deps.Alerts.Seed(context.Background()); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(NewAdmin(h.deps))
	defer srv.Close()
	base := srv.URL + AdminPrefix
	csrf := csrfH

	resp, body := do(t, http.MethodPost, base+"/alerts/evaluate", "", csrf)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	var ov AlertsOverview
	_ = json.Unmarshal([]byte(body), &ov)
	if ov.Firing != 1 || len(ov.Notifiers) != 1 || ov.Notifiers[0].Name != "discord" {
		t.Fatalf("%s", body)
	}

	resp, body = do(t, http.MethodPost, base+"/alerts/rules", `{"name":"Quiet","enabled":true,"severity":"warning",
		"params":{"dimension":"prefix","key":"evt.home","op":"below","threshold":1,"window_minutes":30}}`, csrf)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	var rule alerts.AlertRule
	_ = json.Unmarshal([]byte(body), &rule)
	resp, body = do(t, http.MethodPost, base+"/alerts/rules", `{"name":"Bad","enabled":true,"severity":"warning",
		"params":{"dimension":"prefix","key":"evt.a.b","op":"below","threshold":1,"window_minutes":5}}`, csrf)
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "per 1h0m0s") {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	resp, body = do(t, http.MethodPut, base+"/alerts/rules/cert_expiry", `{"enabled":false,"severity":"critical","params":{"days":14}}`, csrf)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"enabled":false`) {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if resp, _ := do(t, http.MethodDelete, base+"/alerts/rules/cert_expiry", "", csrf); resp.StatusCode != http.StatusConflict {
		t.Fatalf("delete builtin: %d", resp.StatusCode)
	}
	if resp, _ := do(t, http.MethodDelete, base+"/alerts/rules/"+rule.ID, "", csrf); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete custom: %d", resp.StatusCode)
	}
	resp, body = do(t, http.MethodGet, base+"/alerts/history?rule=cert_expiry", "", nil)
	var hist []alerts.AlertHistoryEntry
	_ = json.Unmarshal([]byte(body), &hist)
	if resp.StatusCode != http.StatusOK || len(hist) != 2 || hist[0].Summary != "rule disabled" {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	resp, body = do(t, http.MethodPost, base+"/alerts/notifiers/discord/test", "", csrf)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"sent":1`) {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if resp, _ := do(t, http.MethodPost, base+"/alerts/notifiers/nope/test", "", csrf); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown notifier: %d", resp.StatusCode)
	}
}

func TestAlertsWithoutEngine(t *testing.T) {
	h := newHarness(t)
	resp, _ := do(t, http.MethodGet, h.admin.URL+AdminPrefix+"/alerts", "", nil)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("%d", resp.StatusCode)
	}
}

func TestSystemConnectionsAndRuntime(t *testing.T) {
	h := newHarness(t)
	h.deps.AccountNames = map[string]string{h.env.Keys.Pub(h.env.Keys.AppAccount): "APP"}
	resp, body := do(t, http.MethodGet, h.admin.URL+AdminPrefix+"/system/connections", "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	var cz Connections
	_ = json.Unmarshal([]byte(body), &cz)
	found := false
	for _, c := range cz.Connections {
		found = found || c.Account == "APP"
	}
	if cz.Total < 2 || !found {
		t.Fatalf("%s", body)
	}
	_, body = do(t, http.MethodGet, h.admin.URL+AdminPrefix+"/system/info", "", nil)
	var si SystemInfo
	_ = json.Unmarshal([]byte(body), &si)
	if si.Server == nil || si.Server.Connections < 2 || si.Runtime.Goroutines == 0 || si.Runtime.GoVersion == "" {
		t.Fatalf("%s", body)
	}
}
