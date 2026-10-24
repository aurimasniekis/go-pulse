//go:build s3

// PL-BAK-009 against a real S3 service (SeaweedFS in Docker): `make test-s3`.
package backup

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/aurimasniekis/go-pulse/internal/streams"
)

const seaweedImage = "chrislusf/seaweedfs:latest@sha256:4e61d15fd35994cb1e43e1e553dff106794841fd9a99ade2fc8c8bfce4d7872d"

func startSeaweed(t *testing.T) string {
	t.Helper()
	name := fmt.Sprintf("pulse-s3-test-%d", time.Now().UnixNano())
	out, err := exec.CommandContext(context.Background(), "docker", "run", "-d", "--name", name, "-p", "127.0.0.1::8333",
		"-v", mustAbs(t, "../../deploy/dev/seaweedfs-s3.json")+":/etc/seaweedfs/s3.json:ro",
		seaweedImage, "server", "-dir=/data", "-s3", "-s3.config=/etc/seaweedfs/s3.json", "-master.volumeSizeLimitMB=64", "-volume.max=8").CombinedOutput()
	if err != nil {
		t.Fatalf("docker run: %v\n%s", err, out)
	}
	t.Cleanup(func() { _ = exec.CommandContext(context.Background(), "docker", "rm", "-f", name).Run() })
	port, err := exec.CommandContext(context.Background(), "docker", "port", name, "8333/tcp").Output()
	if err != nil {
		t.Fatal(err)
	}
	addr := strings.TrimSpace(strings.SplitN(string(port), "\n", 2)[0])
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		if resp, err := http.Get("http://" + addr); err == nil { //nolint:noctx // test readiness probe
			resp.Body.Close()
			return addr
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatal("seaweedfs did not start")
	return ""
}

func mustAbs(t *testing.T, p string) string {
	t.Helper()
	out, err := exec.CommandContext(context.Background(), "realpath", p).Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func TestS3BackupRestore(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	addr := startSeaweed(t)
	ctx := context.Background()
	st, err := NewS3(S3Options{Endpoint: addr, Bucket: "pulse-test", Prefix: "site-a/", Region: "us-east-1", AccessKey: "pulse-dev",
		SecretKey: "pulse-dev-not-a-secret", Insecure: true, Lookup: "path", CreateBucket: true}) // gitleaks:allow (dev-only credential)
	if err != nil {
		t.Fatal(err)
	}
	var ready error
	for range 60 { // the S3 gateway answers before the filer is ready
		if ready = st.Check(ctx); ready == nil {
			break
		}
		time.Sleep(time.Second)
	}
	if ready != nil {
		t.Fatal(ready)
	}
	if _, err := st.Stat(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stat missing: %v", err)
	}
	if _, err := st.Get(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get missing: %v", err)
	}

	r := newRig(t)
	r.svc.o.Store = st
	r.publishBig(t, "evt.big.blob.put", 1500, 10<<10)
	before := r.read(t, streams.Events)
	first, err := r.svc.Backup(ctx, "manual")
	if err != nil {
		t.Fatal(err)
	}
	r.clock = r.clock.AddDate(0, 0, 1)
	second, err := r.svc.Backup(ctx, "manual")
	if err != nil {
		t.Fatal(err)
	}
	if !first.Verified || !second.Verified || second.ChunksNew != 0 {
		t.Fatalf("first %+v second %+v", first, second)
	}
	objs, err := st.List(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range objs {
		if strings.HasPrefix(o.Key, "site-a/") {
			t.Fatalf("List returned a prefixed key %s", o.Key)
		}
	}
	remote, err := r.svc.Remote(ctx)
	if err != nil || len(remote) != 2 || remote[0].ID != second.ID {
		t.Fatalf("remote %+v %v", remote, err)
	}
	if err := r.js.DeleteStream(ctx, streams.Events); err != nil {
		t.Fatal(err)
	}
	rd := &Reader{Store: st, Identities: r.reader().Identities, NameKey: r.key}
	m, err := rd.Manifest(ctx, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rd.RestoreStreams(ctx, r.nc, r.js, m, RestoreOptions{Streams: []string{streams.Events}}); err != nil {
		t.Fatal(err)
	}
	after := r.read(t, streams.Events)
	if len(after) != len(before) || after[0] != before[0] || after[len(after)-1] != before[len(before)-1] {
		t.Fatalf("restored %d of %d", len(after), len(before))
	}
	t.Logf("S3: %d objects, first backup uploaded %d B (%d chunks), second %d B", len(objs), first.UploadedBytes, first.ChunksNew, second.UploadedBytes)
}
