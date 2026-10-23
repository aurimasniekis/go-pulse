// Command pulse-spike is the M2 gate spike (PL-TST-006): it fills EVENTS with a large synthetic data set and measures
// NATS memory and replay latency. It is a development tool and is not shipped in the image. See docs/SIZING.md.
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/aurimasniekis/go-pulse/internal/bootstrap"
	"github.com/aurimasniekis/go-pulse/internal/certs"
	"github.com/aurimasniekis/go-pulse/internal/natsconn"
	"github.com/aurimasniekis/go-pulse/internal/secretbox"
	"github.com/aurimasniekis/go-pulse/internal/streams"
	"github.com/aurimasniekis/go-pulse/internal/sysapi"
	"github.com/aurimasniekis/go-pulse/pkg/pulse/envelope"
)

type result struct {
	Phase   string         `json:"phase"`
	Metrics map[string]any `json:"metrics"`
}

var (
	results []result
	peakMem atomic.Int64
	lastMem atomic.Int64
)

func record(phase string, m map[string]any) {
	m["nats_rss_now_mib"] = lastMem.Load() >> 20
	m["nats_rss_peak_mib"] = peakMem.Load() >> 20
	results = append(results, result{phase, m})
	b, _ := json.Marshal(result{phase, m})
	fmt.Println(string(b))
}

func main() {
	dataDir := flag.String("data-dir", "/data/pulsed", "pulsed data dir (keys.enc)")
	keyFile := flag.String("master-key-file", "/run/secrets/pulse_master_key", "master key file")
	url := flag.String("url", "tls://nats:4222", "NATS URL")
	tlsName := flag.String("tls-name", "nats", "TLS server name")
	target := flag.Int64("target-bytes", 10<<30, "logical bytes to publish")
	subjects := flag.Int("subjects", 5000, "distinct subjects")
	msgSize := flag.Int("msg-size", 1024, "payload size in bytes")
	skipPublish := flag.Bool("skip-publish", false, "only run the replay measurements")
	pauseFile := flag.String("pause-file", "", "after publishing, wait until this file disappears (lets the operator restart nats)")
	out := flag.String("out", "", "write results JSON here")
	marksFile := flag.String("marks", "spike-marks.json", "where publish time marks are stored between runs")
	flag.Parse()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ctx := context.Background()

	mkb, err := os.ReadFile(*keyFile)
	must(err)
	master, err := secretbox.ParseMasterKey(string(mkb))
	must(err)
	ks, err := bootstrap.LoadKeySet(bootstrap.Layout{DataDir: *dataDir}.KeysFile(), master)
	must(err)
	tlsConf, err := certs.ClientTLS(*tlsName, []byte(ks.CACert))
	must(err)
	sysJWT, sysSeed, _ := ks.SysUser()
	appJWT, appSeed, _ := ks.AppUser()
	sysNC, err := natsconn.Connect(ctx, natsconn.Options{URL: *url, TLS: tlsConf, JWT: sysJWT, Seed: sysSeed, Name: "spike-sys", Logger: log})
	must(err)
	appNC, err := natsconn.Connect(ctx, natsconn.Options{URL: *url, TLS: tlsConf, JWT: appJWT, Seed: appSeed, Name: "spike-app", Logger: log})
	must(err)
	sys := sysapi.New(sysNC)
	go sampleMem(ctx, sys)

	js, err := jetstream.New(appNC)
	must(err)
	ens, err := streams.EnsureEvents(ctx, js, 4*(*target), false, log)
	must(err)
	log.Info("stream", "created", ens.Created, "msgs", ens.Info.State.Msgs, "bytes", ens.Info.State.Bytes)

	marks := map[string]time.Time{}
	if !*skipPublish {
		marks = publish(ctx, js, *target, *subjects, *msgSize, log)
		mb, _ := json.Marshal(marks)
		_ = os.WriteFile(*marksFile, mb, 0o600)
	} else if b, err := os.ReadFile(*marksFile); err == nil {
		_ = json.Unmarshal(b, &marks)
	}
	if *pauseFile != "" {
		_ = os.WriteFile(*pauseFile, nil, 0o600)
		log.Info("paused: restart nats now, then delete the pause file", "file", *pauseFile)
		for {
			if _, err := os.Stat(*pauseFile); os.IsNotExist(err) {
				break
			}
			time.Sleep(time.Second)
		}
		start := time.Now()
		for !appNC.IsConnected() {
			time.Sleep(100 * time.Millisecond)
		}
		for {
			if _, err := js.Stream(ctx, streams.Events); err == nil {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		peakMem.Store(0)
		record("restart", map[string]any{"stream_available_after_s": time.Since(start).Seconds()})
	}
	replay(ctx, js, marks, *subjects, log)
	if *out != "" {
		b, _ := json.MarshalIndent(results, "", "  ")
		must(os.WriteFile(*out, b, 0o600))
	}
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "spike:", err)
		os.Exit(1)
	}
}

func sampleMem(ctx context.Context, sys *sysapi.Client) {
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		v, err := sys.Varz(ctx)
		if err != nil {
			continue
		}
		lastMem.Store(v.Mem)
		if p := peakMem.Load(); v.Mem > p {
			peakMem.Store(v.Mem) // single writer
		}
	}
}

func payload(size int) []byte {
	raw := make([]byte, size*3/4)
	_, _ = rand.Read(raw)
	return []byte(base64.StdEncoding.EncodeToString(raw))[:size]
}

func publish(ctx context.Context, js jetstream.JetStream, target int64, subjects, size int, log *slog.Logger) map[string]time.Time {
	marks := map[string]time.Time{}
	var sent int64
	var n int64
	start := time.Now()
	pool := make([][]byte, 256)
	for i := range pool {
		pool[i] = payload(size)
	}
	nextMark := []float64{0.1, 0.5, 0.9}
	for sent < target {
		subj := fmt.Sprintf("evt.spike.e%04d.updated", n%int64(subjects))
		m, err := envelope.Encode(envelope.Event{Subject: subj, Type: "spike.entity.updated.v1", Source: "spike",
			ContentType: envelope.ContentText, Data: pool[n%int64(len(pool))]})
		must(err)
		if _, err := js.PublishMsgAsync(m); err != nil {
			<-js.PublishAsyncComplete()
			continue
		}
		sent += int64(len(m.Data) + len(subj) + 150)
		n++
		if len(nextMark) > 0 && float64(sent) >= nextMark[0]*float64(target) {
			<-js.PublishAsyncComplete()
			marks[fmt.Sprintf("p%02d", int(nextMark[0]*100))] = time.Now()
			nextMark = nextMark[1:]
		}
		if n%500_000 == 0 {
			el := time.Since(start).Seconds()
			log.Info("publishing", "msgs", n, "gib", float64(sent)/(1<<30), "msg_per_s", int(float64(n)/el), "rss_mib", lastMem.Load()>>20)
		}
	}
	select {
	case <-js.PublishAsyncComplete():
	case <-time.After(time.Minute):
	}
	el := time.Since(start)
	info, err := js.Stream(ctx, streams.Events)
	must(err)
	st := info.CachedInfo().State
	record("publish", map[string]any{
		"msgs": st.Msgs, "logical_bytes_gib": float64(st.Bytes) / (1 << 30), "subjects": st.NumSubjects,
		"seconds": el.Seconds(), "msg_per_s": float64(n) / el.Seconds(),
	})
	return marks
}

func firstMessage(ctx context.Context, js jetstream.JetStream, cfg jetstream.OrderedConsumerConfig, count int) (time.Duration, time.Duration, error) {
	start := time.Now()
	cons, err := js.OrderedConsumer(ctx, streams.Events, cfg)
	if err != nil {
		return 0, 0, err
	}
	var first time.Duration
	got := 0
	var mu sync.Mutex
	done := make(chan struct{})
	cc, err := cons.Consume(func(m jetstream.Msg) {
		mu.Lock()
		defer mu.Unlock()
		if got == 0 {
			first = time.Since(start)
		}
		got++
		if got == count {
			close(done)
		}
	})
	if err != nil {
		return 0, 0, err
	}
	defer cc.Stop()
	select {
	case <-done:
	case <-time.After(2 * time.Minute):
		return first, 0, fmt.Errorf("timeout after %d msgs", got)
	}
	return first, time.Since(start), nil
}

func replay(ctx context.Context, js jetstream.JetStream, marks map[string]time.Time, subjects int, log *slog.Logger) {
	for _, mark := range []string{"p10", "p50", "p90"} {
		t, ok := marks[mark]
		if !ok {
			continue
		}
		first, all, err := firstMessage(ctx, js, jetstream.OrderedConsumerConfig{
			FilterSubjects: []string{"evt.>"}, DeliverPolicy: jetstream.DeliverByStartTimePolicy, OptStartTime: &t,
		}, 100_000)
		record("replay_all_by_time_"+mark, map[string]any{"first_msg_ms": ms(first), "100k_msgs_s": all.Seconds(), "err": errStr(err)})
		subj := fmt.Sprintf("evt.spike.e%04d.updated", subjects/2)
		first, all, err = firstMessage(ctx, js, jetstream.OrderedConsumerConfig{
			FilterSubjects: []string{subj}, DeliverPolicy: jetstream.DeliverByStartTimePolicy, OptStartTime: &t,
		}, 100)
		record("replay_one_subject_by_time_"+mark, map[string]any{"first_msg_ms": ms(first), "100_msgs_s": all.Seconds(), "err": errStr(err)})
	}
	first, all, err := firstMessage(ctx, js, jetstream.OrderedConsumerConfig{
		FilterSubjects: []string{"evt.spike.e0007.updated"}, DeliverPolicy: jetstream.DeliverAllPolicy,
	}, 1000)
	record("replay_one_subject_from_start", map[string]any{"first_msg_ms": ms(first), "1000_msgs_s": all.Seconds(), "err": errStr(err)})

	s, err := js.Stream(ctx, streams.Events)
	must(err)
	var total time.Duration
	const gets = 200
	for i := range gets {
		start := time.Now()
		_, err := s.GetLastMsgForSubject(ctx, fmt.Sprintf("evt.spike.e%04d.updated", (i*37)%subjects))
		total += time.Since(start)
		if err != nil {
			log.Warn("direct get", "err", err)
		}
	}
	record("direct_get_last_by_subject", map[string]any{"avg_ms": ms(total / gets)})
	_ = nats.ErrTimeout
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

func errStr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
