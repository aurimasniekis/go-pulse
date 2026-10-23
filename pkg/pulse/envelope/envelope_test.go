package envelope

import (
	"encoding/base64"
	"encoding/json"
	"maps"
	"os"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

type vecEvent struct {
	Subject     string              `json:"subject"`
	ID          string              `json:"id"`
	Type        string              `json:"type"`
	Source      string              `json:"source"`
	Time        *string             `json:"time"`
	ContentType string              `json:"content_type"`
	DataB64     string              `json:"data_b64"`
	Blob        *BlobRef            `json:"blob"`
	Untyped     bool                `json:"untyped"`
	Extra       map[string][]string `json:"extra"`
}

type vectors struct {
	Subjects struct{ Valid, Invalid []string } `json:"subjects"`
	Filters  struct{ Valid, Invalid []string } `json:"filters"`
	Encode   []struct {
		Name    string            `json:"name"`
		Event   vecEvent          `json:"event"`
		Headers map[string]string `json:"headers"`
	} `json:"encode"`
	EncodeErrors []struct {
		Name  string   `json:"name"`
		Event vecEvent `json:"event"`
	} `json:"encode_errors"`
	Decode []struct {
		Name    string            `json:"name"`
		Subject string            `json:"subject"`
		Headers map[string]string `json:"headers"`
		DataB64 string            `json:"data_b64"`
		Event   vecEvent          `json:"event"`
	} `json:"decode"`
}

func load(t *testing.T) vectors {
	t.Helper()
	b, err := os.ReadFile("../../../sdk/conformance/vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var v vectors
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func toEvent(t *testing.T, v vecEvent) Event {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString(v.DataB64)
	if err != nil {
		t.Fatal(err)
	}
	e := Event{Subject: v.Subject, ID: v.ID, Type: v.Type, Source: v.Source, ContentType: v.ContentType, Data: data, Blob: v.Blob}
	if v.Time != nil {
		e.Time, err = time.Parse(time.RFC3339Nano, *v.Time)
		if err != nil {
			t.Fatal(err)
		}
	}
	return e
}

func TestConformanceSubjects(t *testing.T) {
	v := load(t)
	for _, s := range v.Subjects.Valid {
		if err := ValidateSubject(s); err != nil {
			t.Errorf("valid %q: %v", s, err)
		}
	}
	for _, s := range v.Subjects.Invalid {
		if ValidateSubject(s) == nil {
			t.Errorf("invalid %q accepted", s)
		}
	}
	for _, s := range v.Filters.Valid {
		if err := ValidateFilter(s); err != nil {
			t.Errorf("valid filter %q: %v", s, err)
		}
	}
	for _, s := range v.Filters.Invalid {
		if ValidateFilter(s) == nil {
			t.Errorf("invalid filter %q accepted", s)
		}
	}
}

func TestConformanceEncode(t *testing.T) {
	v := load(t)
	for _, c := range v.Encode {
		m, err := Encode(toEvent(t, c.Event))
		if err != nil {
			t.Fatalf("%s: %v", c.Name, err)
		}
		got := map[string]string{}
		for k := range m.Header {
			got[k] = m.Header.Get(k)
		}
		if !maps.Equal(got, c.Headers) {
			t.Errorf("%s: headers\n got  %v\n want %v", c.Name, got, c.Headers)
		}
	}
	for _, c := range v.EncodeErrors {
		if _, err := Encode(toEvent(t, c.Event)); err == nil {
			t.Errorf("%s: encoded", c.Name)
		}
	}
}

func TestConformanceDecode(t *testing.T) {
	v := load(t)
	for _, c := range v.Decode {
		h := nats.Header{}
		for k, val := range c.Headers {
			h.Set(k, val)
		}
		data, _ := base64.StdEncoding.DecodeString(c.DataB64)
		e := Decode(c.Subject, h, data)
		w := c.Event
		var wantTime time.Time
		if w.Time != nil {
			wantTime, _ = time.Parse(time.RFC3339Nano, *w.Time)
		}
		extra := map[string][]string(e.Extra)
		if extra == nil {
			extra = map[string][]string{}
		}
		if e.Subject != w.Subject || e.ID != w.ID || e.Type != w.Type || e.Source != w.Source || e.ContentType != w.ContentType ||
			e.Untyped() != w.Untyped || !e.Time.Equal(wantTime) || base64.StdEncoding.EncodeToString(e.Data) != w.DataB64 ||
			len(extra) != len(w.Extra) {
			t.Errorf("%s: got %+v want %+v", c.Name, e, w)
		}
		for k, vs := range w.Extra {
			if len(extra[k]) != len(vs) || extra[k][0] != vs[0] {
				t.Errorf("%s: extra %s = %v want %v", c.Name, k, extra[k], vs)
			}
		}
	}
}

func TestRoundTripFillsIDAndTime(t *testing.T) {
	m, err := Encode(Event{Subject: "evt.a.b.c", Type: "t", Data: []byte("x"), Extra: nats.Header{"X-Trace": {"1"}}})
	if err != nil {
		t.Fatal(err)
	}
	e := DecodeMsg(m)
	if len(e.ID) != 26 || e.Time.IsZero() || e.Extra.Get("X-Trace") != "1" || string(e.Data) != "x" {
		t.Fatalf("%+v", e)
	}
}
