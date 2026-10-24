package api

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

func getConsumer(t *testing.T, url string) ConsumerDTO {
	t.Helper()
	resp, body := do(t, http.MethodGet, url, "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get consumer: %d %s", resp.StatusCode, body)
	}
	var c ConsumerDTO
	if err := json.Unmarshal([]byte(body), &c); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestPutConsumer(t *testing.T) {
	h := newHarness(t)
	base := h.admin.URL + AdminPrefix
	if resp, body := do(t, http.MethodPost, base+"/streams", `{"name":"ORDERS","subjects":["orders.>"]}`, csrfH); resp.StatusCode != http.StatusCreated {
		t.Fatalf("create stream: %d %s", resp.StatusCode, body)
	}
	url := base + "/streams/ORDERS/consumers/abc123_worker"

	if resp, _ := do(t, http.MethodGet, url, "", nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("get missing consumer: %d", resp.StatusCode)
	}
	if resp, _ := do(t, http.MethodGet, base+"/streams/NOPE/consumers/x", "", nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("get consumer of missing stream: %d", resp.StatusCode)
	}

	// Create.
	resp, body := do(t, http.MethodPut, url, `{"description":"order worker","filter_subjects":["orders.created"],"ack_wait_seconds":60,
		"max_deliver":5,"max_ack_pending":100}`, csrfH)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create consumer: %d %s", resp.StatusCode, body)
	}
	c := getConsumer(t, url)
	if c.Name != "abc123_worker" || !c.Durable || c.Description != "order worker" || !slices.Equal(c.FilterSubjects, []string{"orders.created"}) ||
		c.DeliverPolicy != "all" || c.AckPolicy != "AckExplicit" || c.AckWaitSeconds != 60 || c.MaxDeliver != 5 || c.MaxAckPending != 100 ||
		c.OptStartSeq != 0 || c.OptStartTime != nil || c.Internal {
		t.Fatalf("created consumer %+v", c)
	}
	if strings.Contains(body, "opt_start") {
		t.Fatalf("opt_start_* should be omitted: %s", body)
	}

	// Update mutable settings, including a second filter subject.
	resp, body = do(t, http.MethodPut, url, `{"description":"order worker v2","filter_subjects":["orders.created","orders.updated"],
		"ack_wait_seconds":90,"max_deliver":7,"max_ack_pending":50}`, csrfH)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("update consumer: %d %s", resp.StatusCode, body)
	}
	c = getConsumer(t, url)
	if c.Description != "order worker v2" || !slices.Equal(c.FilterSubjects, []string{"orders.created", "orders.updated"}) ||
		c.AckWaitSeconds != 90 || c.MaxDeliver != 7 || c.MaxAckPending != 50 {
		t.Fatalf("updated consumer %+v", c)
	}
	// Defaults: unlimited deliveries, server ack wait.
	if resp, body = do(t, http.MethodPut, url, `{"filter_subjects":["orders.created"]}`, csrfH); resp.StatusCode != http.StatusOK {
		t.Fatalf("update to defaults: %d %s", resp.StatusCode, body)
	}
	if c = getConsumer(t, url); c.MaxDeliver != -1 || c.AckWaitSeconds != 30 || c.Description != "" {
		t.Fatalf("defaults %+v", c)
	}

	// An immutable change is refused by NATS with its own explanation.
	resp, body = do(t, http.MethodPut, url, `{"filter_subjects":["orders.created"],"deliver_policy":"new"}`, csrfH)
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(strings.ToLower(body), "deliver policy") {
		t.Fatalf("deliver policy change: %d %s", resp.StatusCode, body)
	}
	if c = getConsumer(t, url); c.DeliverPolicy != "all" {
		t.Fatalf("deliver policy changed: %+v", c)
	}

	// Start positions round-trip.
	resp, body = do(t, http.MethodPut, base+"/streams/ORDERS/consumers/from_seq",
		`{"filter_subjects":["orders.>"],"deliver_policy":"by_start_sequence","opt_start_seq":42}`, csrfH)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("by_start_sequence: %d %s", resp.StatusCode, body)
	}
	if c = getConsumer(t, base+"/streams/ORDERS/consumers/from_seq"); c.DeliverPolicy != "by_start_sequence" || c.OptStartSeq != 42 {
		t.Fatalf("by_start_sequence %+v", c)
	}
	resp, body = do(t, http.MethodPut, base+"/streams/ORDERS/consumers/from_time",
		`{"filter_subjects":["orders.>"],"deliver_policy":"by_start_time","opt_start_time":"2026-01-02T03:04:05Z"}`, csrfH)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("by_start_time: %d %s", resp.StatusCode, body)
	}
	c = getConsumer(t, base+"/streams/ORDERS/consumers/from_time")
	if c.DeliverPolicy != "by_start_time" || c.OptStartTime == nil || !c.OptStartTime.Equal(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)) {
		t.Fatalf("by_start_time %+v", c)
	}

	// Input validation.
	for name, in := range map[string]string{
		"no filter":             `{"filter_subjects":[]}`,
		"bad filter":            `{"filter_subjects":["orders..x"]}`,
		"wildcard in token":     `{"filter_subjects":["orders.a*"]}`,
		"> not last":            `{"filter_subjects":["orders.>.x"]}`,
		"reserved description":  `{"filter_subjects":["orders.>"],"description":"pulsed: mine"}`,
		"seq without policy":    `{"filter_subjects":["orders.>"],"opt_start_seq":3}`,
		"policy without seq":    `{"filter_subjects":["orders.>"],"deliver_policy":"by_start_sequence"}`,
		"policy without time":   `{"filter_subjects":["orders.>"],"deliver_policy":"by_start_time"}`,
		"unknown policy":        `{"filter_subjects":["orders.>"],"deliver_policy":"sometimes"}`,
		"outside stream filter": `{"filter_subjects":["shipments.>"]}`,
	} {
		if resp, body := do(t, http.MethodPut, base+"/streams/ORDERS/consumers/bad", in, csrfH); resp.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d %s", name, resp.StatusCode, body)
		}
	}

	// Consumers pulsed created are off limits.
	if _, err := h.deps.JS.CreateOrUpdateConsumer(context.Background(), "ORDERS", jetstream.ConsumerConfig{Durable: "rollup_probe",
		Description: "pulsed: rollups", AckPolicy: jetstream.AckExplicitPolicy}); err != nil {
		t.Fatal(err)
	}
	if resp, body := do(t, http.MethodPut, base+"/streams/ORDERS/consumers/rollup_probe", `{"filter_subjects":["orders.>"]}`, csrfH); resp.StatusCode != http.StatusConflict {
		t.Fatalf("internal consumer: %d %s", resp.StatusCode, body)
	}
	if c = getConsumer(t, base+"/streams/ORDERS/consumers/rollup_probe"); !c.Internal || c.Description != "pulsed: rollups" {
		t.Fatalf("internal consumer changed: %+v", c)
	}

	// KV and object store backing streams are refused; a missing stream is 404.
	if resp, body := do(t, http.MethodPost, base+"/kv", `{"name":"settings"}`, csrfH); resp.StatusCode != http.StatusCreated {
		t.Fatalf("create kv: %d %s", resp.StatusCode, body)
	}
	if resp, body := do(t, http.MethodPut, base+"/streams/KV_settings/consumers/x", `{"filter_subjects":["$KV.settings.>"]}`, csrfH); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("kv backing stream: %d %s", resp.StatusCode, body)
	}
	if resp, body := do(t, http.MethodPut, base+"/streams/NOPE/consumers/x", `{"filter_subjects":["x.>"]}`, csrfH); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing stream: %d %s", resp.StatusCode, body)
	}

	var n int
	_ = h.db.R().QueryRow("SELECT COUNT(*) FROM audit WHERE action = 'consumer.put' AND result = 'ok'").Scan(&n)
	if n != 5 {
		t.Fatalf("consumer.put audit rows %d", n)
	}
}
