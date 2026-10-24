// Package backup implements nightly encrypted backups (PL-BAK-001..009, ADR-0035): the SQLite database and keys.enc
// as files, and every JetStream stream as a snapshot whose files are stored content-addressed, so unchanged message
// blocks are uploaded once and every backup is still a complete, restorable snapshot.
package backup

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// ErrNotFound means the object does not exist.
var ErrNotFound = errors.New("object not found")

// ObjectInfo describes a stored object.
type ObjectInfo struct {
	Key     string
	Size    int64
	ModTime time.Time
}

// ObjectStore is the subset of S3 the backups use.
type ObjectStore interface {
	Put(ctx context.Context, key string, data []byte) error
	Get(ctx context.Context, key string) ([]byte, error)
	Stat(ctx context.Context, key string) (ObjectInfo, error)
	List(ctx context.Context, prefix string) ([]ObjectInfo, error)
	Delete(ctx context.Context, key string) error
}

// S3Options configure the S3 store (PL-BAK-009).
type S3Options struct {
	Endpoint     string // host[:port]
	Bucket       string
	Prefix       string // key prefix inside the bucket, e.g. "pulse/"
	Region       string
	AccessKey    string
	SecretKey    string
	Insecure     bool   // plain HTTP (dev only)
	Lookup       string // auto | path | virtual
	CAPEM        []byte // extra roots
	CreateBucket bool   // create the bucket when missing (dev)
}

// S3 stores objects with minio-go.
type S3 struct {
	c      *minio.Client
	bucket string
	prefix string
	create bool
	region string
}

// NewS3 returns a client for an S3-compatible service (no network access until used).
func NewS3(o S3Options) (*S3, error) {
	lookup := minio.BucketLookupAuto
	switch o.Lookup {
	case "path":
		lookup = minio.BucketLookupPath
	case "virtual", "dns":
		lookup = minio.BucketLookupDNS
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if len(o.CAPEM) > 0 {
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(o.CAPEM) {
			return nil, errors.New("backup: invalid S3 CA PEM")
		}
		tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	c, err := minio.New(o.Endpoint, &minio.Options{Creds: credentials.NewStaticV4(o.AccessKey, o.SecretKey, ""), Secure: !o.Insecure,
		Region: o.Region, BucketLookup: lookup, Transport: tr})
	if err != nil {
		return nil, err
	}
	return &S3{c: c, bucket: o.Bucket, prefix: o.Prefix, create: o.CreateBucket, region: o.Region}, nil
}

// Check verifies the bucket exists (creating it when allowed). Backups call it first, so a misconfigured store fails
// the backup, not pulsed's start.
func (s *S3) Check(ctx context.Context) error {
	ok, err := s.c.BucketExists(ctx, s.bucket)
	if err != nil {
		return fmt.Errorf("backup: bucket %s: %w", s.bucket, err)
	}
	if ok {
		return nil
	}
	if !s.create {
		return fmt.Errorf("backup: bucket %s does not exist", s.bucket)
	}
	if err := s.c.MakeBucket(ctx, s.bucket, minio.MakeBucketOptions{Region: s.region}); err != nil {
		return fmt.Errorf("backup: create bucket %s: %w", s.bucket, err)
	}
	return nil
}

func (s *S3) key(k string) string { return s.prefix + k }

// Put implements ObjectStore.
func (s *S3) Put(ctx context.Context, key string, data []byte) error {
	_, err := s.c.PutObject(ctx, s.bucket, s.key(key), bytes.NewReader(data), int64(len(data)),
		minio.PutObjectOptions{ContentType: "application/octet-stream"})
	return err
}

func notFound(err error) bool {
	return minio.ToErrorResponse(err).Code == "NoSuchKey" || minio.ToErrorResponse(err).StatusCode == http.StatusNotFound
}

// Get implements ObjectStore.
func (s *S3) Get(ctx context.Context, key string) ([]byte, error) {
	o, err := s.c.GetObject(ctx, s.bucket, s.key(key), minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	defer o.Close()
	b, err := io.ReadAll(o)
	if err != nil {
		if notFound(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return b, nil
}

// Stat implements ObjectStore.
func (s *S3) Stat(ctx context.Context, key string) (ObjectInfo, error) {
	st, err := s.c.StatObject(ctx, s.bucket, s.key(key), minio.StatObjectOptions{})
	if err != nil {
		if notFound(err) {
			return ObjectInfo{}, ErrNotFound
		}
		return ObjectInfo{}, err
	}
	return ObjectInfo{Key: key, Size: st.Size, ModTime: st.LastModified}, nil
}

// List implements ObjectStore (keys are returned without the store prefix).
func (s *S3) List(ctx context.Context, prefix string) ([]ObjectInfo, error) {
	var out []ObjectInfo
	for o := range s.c.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{Prefix: s.key(prefix), Recursive: true}) {
		if o.Err != nil {
			return nil, o.Err
		}
		out = append(out, ObjectInfo{Key: strings.TrimPrefix(o.Key, s.prefix), Size: o.Size, ModTime: o.LastModified})
	}
	return out, nil
}

// Delete implements ObjectStore.
func (s *S3) Delete(ctx context.Context, key string) error {
	return s.c.RemoveObject(ctx, s.bucket, s.key(key), minio.RemoveObjectOptions{})
}

// Mem is an in-memory ObjectStore for tests.
type Mem struct {
	mu   sync.Mutex
	objs map[string][]byte
	mod  map[string]time.Time
	Puts int
}

// NewMem returns an empty store.
func NewMem() *Mem { return &Mem{objs: map[string][]byte{}, mod: map[string]time.Time{}} }

// Put implements ObjectStore.
func (m *Mem) Put(_ context.Context, key string, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objs[key] = append([]byte(nil), data...)
	m.mod[key] = time.Now()
	m.Puts++
	return nil
}

// Get implements ObjectStore.
func (m *Mem) Get(_ context.Context, key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.objs[key]
	if !ok {
		return nil, ErrNotFound
	}
	return append([]byte(nil), b...), nil
}

// Stat implements ObjectStore.
func (m *Mem) Stat(_ context.Context, key string) (ObjectInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.objs[key]
	if !ok {
		return ObjectInfo{}, ErrNotFound
	}
	return ObjectInfo{Key: key, Size: int64(len(b)), ModTime: m.mod[key]}, nil
}

// List implements ObjectStore.
func (m *Mem) List(_ context.Context, prefix string) ([]ObjectInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []ObjectInfo
	for k, b := range m.objs {
		if strings.HasPrefix(k, prefix) {
			out = append(out, ObjectInfo{Key: k, Size: int64(len(b)), ModTime: m.mod[k]})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// Delete implements ObjectStore.
func (m *Mem) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.objs, key)
	delete(m.mod, key)
	return nil
}

// Corrupt flips a byte of an object (tests).
func (m *Mem) Corrupt(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if b := m.objs[key]; len(b) > 0 {
		b[len(b)/2] ^= 0xFF
	}
}
