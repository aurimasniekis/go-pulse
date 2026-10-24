package api

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/danielgtaylor/huma/v2"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/aurimasniekis/go-pulse/internal/audit"
	"github.com/aurimasniekis/go-pulse/internal/natsauth"
)

// KVBucket is a key-value bucket in the APP account.
type KVBucket struct {
	Name       string `json:"name"`
	Values     uint64 `json:"values" doc:"Stored values, including history and delete markers"`
	Bytes      uint64 `json:"bytes"`
	History    int64  `json:"history"`
	TTLSeconds int64  `json:"ttl_seconds"`
}

// ObjectBucket is an object store bucket in the APP account.
type ObjectBucket struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Size        uint64 `json:"size"`
	TTLSeconds  int64  `json:"ttl_seconds"`
	Sealed      bool   `json:"sealed"`
	Managed     bool   `json:"managed" doc:"Owned by pulsed (event blobs): read-only here"`
}

// Buckets lists KV and object buckets.
type Buckets struct {
	KV      []KVBucket     `json:"kv"`
	Objects []ObjectBucket `json:"objects"`
}

// KVKey is one key's latest revision.
type KVKey struct {
	Key       string    `json:"key"`
	Revision  uint64    `json:"revision"`
	Created   time.Time `json:"created"`
	Size      int       `json:"size"`
	Operation string    `json:"operation" enum:"put,delete,purge"`
}

// KVKeys is a key listing.
type KVKeys struct {
	Keys      []KVKey `json:"keys"`
	Truncated bool    `json:"truncated" doc:"More keys exist than the limit"`
}

// KVRevision is one historic revision of a key.
type KVRevision struct {
	Revision  uint64    `json:"revision"`
	Created   time.Time `json:"created"`
	Operation string    `json:"operation" enum:"put,delete,purge"`
	Size      int       `json:"size"`
}

// KVEntry is a key's value and history.
type KVEntry struct {
	Key      string       `json:"key"`
	Revision uint64       `json:"revision"`
	Created  time.Time    `json:"created"`
	Value    string       `json:"value"`
	Encoding string       `json:"encoding" enum:"utf8,base64"`
	History  []KVRevision `json:"history"`
}

// ObjectEntry is one object.
type ObjectEntry struct {
	Name        string    `json:"name"`
	Size        uint64    `json:"size"`
	Chunks      uint32    `json:"chunks"`
	Modified    time.Time `json:"modified"`
	Digest      string    `json:"digest"`
	Description string    `json:"description,omitempty"`
}

func opName(o jetstream.KeyValueOp) string {
	switch o {
	case jetstream.KeyValueDelete:
		return "delete"
	case jetstream.KeyValuePurge:
		return "purge"
	default:
		return "put"
	}
}

func encodeValue(b []byte) (string, string) {
	if utf8.Valid(b) {
		return string(b), "utf8"
	}
	return base64.StdEncoding.EncodeToString(b), "base64"
}

func managedObjectStore(name string) bool { return name == natsauth.BlobsBucket }

func kvErr(d *Deps, op string, err error) error {
	switch {
	case errors.Is(err, jetstream.ErrBucketNotFound), errors.Is(err, jetstream.ErrStreamNotFound):
		return huma.Error404NotFound("bucket not found")
	case errors.Is(err, jetstream.ErrKeyNotFound), errors.Is(err, jetstream.ErrObjectNotFound):
		return huma.Error404NotFound("not found")
	case errors.Is(err, jetstream.ErrInvalidKey), errors.Is(err, jetstream.ErrInvalidBucketName), errors.Is(err, jetstream.ErrBadObjectMeta):
		return huma.Error422UnprocessableEntity(err.Error())
	case errors.Is(err, jetstream.ErrBucketExists), errors.Is(err, jetstream.ErrStreamNameAlreadyInUse):
		return huma.Error409Conflict("a bucket with this name exists")
	default:
		return d.internalErr(op, err)
	}
}

type bucketPath struct {
	Bucket string `path:"bucket" maxLength:"64"`
}

func registerKV(api huma.API, d *Deps) {
	tags := []string{"kv"}
	huma.Register(api, huma.Operation{OperationID: "list-buckets", Method: http.MethodGet, Path: "/kv", Summary: "KV and object buckets", Tags: tags},
		func(ctx context.Context, _ *struct{}) (*struct{ Body Buckets }, error) {
			out := Buckets{KV: []KVBucket{}, Objects: []ObjectBucket{}}
			kl := d.JS.KeyValueStores(ctx)
			for st := range kl.Status() {
				out.KV = append(out.KV, KVBucket{Name: st.Bucket(), Values: st.Values(), Bytes: st.Bytes(), History: st.History(), TTLSeconds: int64(st.TTL() / time.Second)})
			}
			if err := kl.Error(); err != nil {
				return nil, kvErr(d, "list-buckets", err)
			}
			ol := d.JS.ObjectStores(ctx)
			for st := range ol.Status() {
				out.Objects = append(out.Objects, ObjectBucket{Name: st.Bucket(), Description: st.Description(), Size: st.Size(),
					TTLSeconds: int64(st.TTL() / time.Second), Sealed: st.Sealed(), Managed: managedObjectStore(st.Bucket())})
			}
			if err := ol.Error(); err != nil {
				return nil, kvErr(d, "list-buckets", err)
			}
			return &struct{ Body Buckets }{out}, nil
		})

	huma.Register(api, huma.Operation{OperationID: "create-kv-bucket", Method: http.MethodPost, Path: "/kv", Summary: "Create a KV bucket", Tags: tags,
		DefaultStatus: http.StatusCreated},
		func(ctx context.Context, in *struct {
			Body struct {
				Name        string `json:"name" pattern:"^[A-Za-z0-9_-]{1,64}$"`
				Description string `json:"description,omitempty" maxLength:"256"`
				History     uint8  `json:"history,omitempty" minimum:"1" maximum:"64" default:"1"`
				TTLSeconds  int64  `json:"ttl_seconds,omitempty" minimum:"0"`
				MaxBytes    int64  `json:"max_bytes,omitempty" minimum:"-1" doc:"-1 or 0: unlimited"`
			}
		}) (*struct{ Body KVBucket }, error) {
			b := in.Body
			maxB := b.MaxBytes
			if maxB == 0 {
				maxB = -1
			}
			kv, err := d.JS.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: b.Name, Description: b.Description, History: max(b.History, 1),
				TTL: time.Duration(b.TTLSeconds) * time.Second, MaxBytes: maxB, Storage: jetstream.FileStorage})
			if err != nil {
				return nil, kvErr(d, "create-kv-bucket", err)
			}
			_ = audit.Write(ctx, d.DB, d.actor(ctx), "kv.create", b.Name, audit.OK, map[string]any{"history": b.History, "ttl_seconds": b.TTLSeconds})
			st, _ := kv.Status(ctx)
			out := KVBucket{Name: b.Name}
			if st != nil {
				out.History, out.TTLSeconds = st.History(), int64(st.TTL()/time.Second)
			}
			return &struct{ Body KVBucket }{out}, nil
		})

	huma.Register(api, huma.Operation{OperationID: "delete-kv-bucket", Method: http.MethodDelete, Path: "/kv/{bucket}", Summary: "Delete a KV bucket",
		Tags: tags, DefaultStatus: http.StatusNoContent},
		func(ctx context.Context, in *struct {
			Bucket  string `path:"bucket"`
			Confirm string `query:"confirm"`
		}) (*struct{}, error) {
			if in.Confirm != in.Bucket {
				return nil, huma.Error400BadRequest("repeat the bucket name in ?confirm=")
			}
			if err := d.JS.DeleteKeyValue(ctx, in.Bucket); err != nil {
				return nil, kvErr(d, "delete-kv-bucket", err)
			}
			_ = audit.Write(ctx, d.DB, d.actor(ctx), "kv.delete", in.Bucket, audit.OK, nil)
			return &struct{}{}, nil
		})

	huma.Register(api, huma.Operation{OperationID: "list-kv-keys", Method: http.MethodGet, Path: "/kv/{bucket}/keys", Summary: "Keys with their latest revision",
		Tags: tags},
		func(ctx context.Context, in *struct {
			Bucket string `path:"bucket"`
			Filter string `query:"filter" maxLength:"256" doc:"Key filter with * and > wildcards"`
			Limit  int    `query:"limit" minimum:"1" maximum:"5000" default:"1000"`
		}) (*struct{ Body KVKeys }, error) {
			kv, err := d.JS.KeyValue(ctx, in.Bucket)
			if err != nil {
				return nil, kvErr(d, "list-kv-keys", err)
			}
			filter := in.Filter
			if filter == "" {
				filter = ">"
			}
			wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			w, err := kv.Watch(wctx, filter, jetstream.MetaOnly(), jetstream.IgnoreDeletes())
			if err != nil {
				return nil, kvErr(d, "list-kv-keys", err)
			}
			defer func() { _ = w.Stop() }()
			out := KVKeys{Keys: []KVKey{}}
			for e := range w.Updates() {
				if e == nil { // initial values delivered
					break
				}
				if len(out.Keys) == in.Limit {
					out.Truncated = true
					break
				}
				out.Keys = append(out.Keys, KVKey{Key: e.Key(), Revision: e.Revision(), Created: e.Created(), Size: len(e.Value()), Operation: opName(e.Operation())})
			}
			return &struct{ Body KVKeys }{out}, nil
		})

	type keyInput struct {
		Bucket string `path:"bucket"`
		Key    string `query:"key" required:"true" maxLength:"1024"`
	}
	huma.Register(api, huma.Operation{OperationID: "get-kv-entry", Method: http.MethodGet, Path: "/kv/{bucket}/entry", Summary: "A key's value and history", Tags: tags},
		func(ctx context.Context, in *keyInput) (*struct{ Body KVEntry }, error) {
			kv, err := d.JS.KeyValue(ctx, in.Bucket)
			if err != nil {
				return nil, kvErr(d, "get-kv-entry", err)
			}
			hist, err := kv.History(ctx, in.Key)
			if err != nil {
				return nil, kvErr(d, "get-kv-entry", err)
			}
			last := hist[len(hist)-1]
			out := KVEntry{Key: in.Key, Revision: last.Revision(), Created: last.Created(), History: []KVRevision{}}
			out.Value, out.Encoding = encodeValue(last.Value())
			for i := len(hist) - 1; i >= 0; i-- {
				h := hist[i]
				out.History = append(out.History, KVRevision{Revision: h.Revision(), Created: h.Created(), Operation: opName(h.Operation()), Size: len(h.Value())})
			}
			return &struct{ Body KVEntry }{out}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "put-kv-entry", Method: http.MethodPut, Path: "/kv/{bucket}/entry", Summary: "Set a key", Tags: tags},
		func(ctx context.Context, in *struct {
			Bucket string `path:"bucket"`
			Key    string `query:"key" required:"true" maxLength:"1024"`
			Body   struct {
				Value    string `json:"value"`
				Encoding string `json:"encoding,omitempty" enum:"utf8,base64" default:"utf8"`
			}
		}) (*struct{ Body KVKey }, error) {
			val := []byte(in.Body.Value)
			if in.Body.Encoding == "base64" {
				var err error
				if val, err = base64.StdEncoding.DecodeString(in.Body.Value); err != nil {
					return nil, huma.Error422UnprocessableEntity("value is not valid base64")
				}
			}
			kv, err := d.JS.KeyValue(ctx, in.Bucket)
			if err != nil {
				return nil, kvErr(d, "put-kv-entry", err)
			}
			rev, err := kv.Put(ctx, in.Key, val)
			if err != nil {
				return nil, kvErr(d, "put-kv-entry", err)
			}
			_ = audit.Write(ctx, d.DB, d.actor(ctx), "kv.put", in.Bucket+"/"+in.Key, audit.OK, map[string]any{"revision": rev, "size": len(val)})
			return &struct{ Body KVKey }{KVKey{Key: in.Key, Revision: rev, Created: time.Now().UTC(), Size: len(val), Operation: "put"}}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "delete-kv-entry", Method: http.MethodDelete, Path: "/kv/{bucket}/entry",
		Summary: "Delete a key (keeps history unless purge)", Tags: tags, DefaultStatus: http.StatusNoContent},
		func(ctx context.Context, in *struct {
			Bucket string `path:"bucket"`
			Key    string `query:"key" required:"true" maxLength:"1024"`
			Purge  bool   `query:"purge"`
		}) (*struct{}, error) {
			kv, err := d.JS.KeyValue(ctx, in.Bucket)
			if err != nil {
				return nil, kvErr(d, "delete-kv-entry", err)
			}
			action := "kv.delete_key"
			if in.Purge {
				action, err = "kv.purge_key", kv.Purge(ctx, in.Key)
			} else {
				err = kv.Delete(ctx, in.Key)
			}
			if err != nil {
				return nil, kvErr(d, "delete-kv-entry", err)
			}
			_ = audit.Write(ctx, d.DB, d.actor(ctx), action, in.Bucket+"/"+in.Key, audit.OK, nil)
			return &struct{}{}, nil
		})

	// Object stores.
	huma.Register(api, huma.Operation{OperationID: "create-object-bucket", Method: http.MethodPost, Path: "/objects", Summary: "Create an object store",
		Tags: tags, DefaultStatus: http.StatusCreated},
		func(ctx context.Context, in *struct {
			Body struct {
				Name        string `json:"name" pattern:"^[A-Za-z0-9_-]{1,64}$"`
				Description string `json:"description,omitempty" maxLength:"256"`
				TTLSeconds  int64  `json:"ttl_seconds,omitempty" minimum:"0"`
				MaxBytes    int64  `json:"max_bytes,omitempty" minimum:"-1"`
			}
		}) (*struct{ Body ObjectBucket }, error) {
			b := in.Body
			maxB := b.MaxBytes
			if maxB == 0 {
				maxB = -1
			}
			if _, err := d.JS.CreateObjectStore(ctx, jetstream.ObjectStoreConfig{Bucket: b.Name, Description: b.Description,
				TTL: time.Duration(b.TTLSeconds) * time.Second, MaxBytes: maxB, Storage: jetstream.FileStorage, Compression: true}); err != nil {
				return nil, kvErr(d, "create-object-bucket", err)
			}
			_ = audit.Write(ctx, d.DB, d.actor(ctx), "object.create_bucket", b.Name, audit.OK, nil)
			return &struct{ Body ObjectBucket }{ObjectBucket{Name: b.Name, Description: b.Description, TTLSeconds: b.TTLSeconds}}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "delete-object-bucket", Method: http.MethodDelete, Path: "/objects/{bucket}", Summary: "Delete an object store",
		Tags: tags, DefaultStatus: http.StatusNoContent},
		func(ctx context.Context, in *struct {
			Bucket  string `path:"bucket"`
			Confirm string `query:"confirm"`
		}) (*struct{}, error) {
			if managedObjectStore(in.Bucket) {
				return nil, huma.Error409Conflict(in.Bucket + " is managed by pulsed")
			}
			if in.Confirm != in.Bucket {
				return nil, huma.Error400BadRequest("repeat the bucket name in ?confirm=")
			}
			if err := d.JS.DeleteObjectStore(ctx, in.Bucket); err != nil {
				return nil, kvErr(d, "delete-object-bucket", err)
			}
			_ = audit.Write(ctx, d.DB, d.actor(ctx), "object.delete_bucket", in.Bucket, audit.OK, nil)
			return &struct{}{}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "list-objects", Method: http.MethodGet, Path: "/objects/{bucket}", Summary: "Objects in a store", Tags: tags},
		func(ctx context.Context, in *bucketPath) (*struct{ Body []ObjectEntry }, error) {
			obs, err := d.JS.ObjectStore(ctx, in.Bucket)
			if err != nil {
				return nil, kvErr(d, "list-objects", err)
			}
			infos, err := obs.List(ctx)
			out := []ObjectEntry{}
			if err != nil && !errors.Is(err, jetstream.ErrNoObjectsFound) {
				return nil, kvErr(d, "list-objects", err)
			}
			for _, o := range infos {
				out = append(out, ObjectEntry{Name: o.Name, Size: o.Size, Chunks: o.Chunks, Modified: o.ModTime, Digest: o.Digest, Description: o.Description})
			}
			return &struct{ Body []ObjectEntry }{out}, nil
		})
	type objInput struct {
		Bucket string `path:"bucket"`
		Name   string `query:"name" required:"true" maxLength:"1024"`
	}
	huma.Register(api, huma.Operation{OperationID: "download-object", Method: http.MethodGet, Path: "/objects/{bucket}/download", Summary: "Download an object",
		Tags: tags, Responses: map[string]*huma.Response{"200": {Description: "Object content", Content: map[string]*huma.MediaType{"application/octet-stream": {}}}}},
		func(ctx context.Context, in *objInput) (*huma.StreamResponse, error) {
			obs, err := d.JS.ObjectStore(ctx, in.Bucket)
			if err != nil {
				return nil, kvErr(d, "download-object", err)
			}
			res, err := obs.Get(ctx, in.Name)
			if err != nil {
				return nil, kvErr(d, "download-object", err)
			}
			info, err := res.Info()
			if err != nil {
				_ = res.Close()
				return nil, kvErr(d, "download-object", err)
			}
			return &huma.StreamResponse{Body: func(hc huma.Context) {
				defer res.Close()
				base := in.Name[strings.LastIndex(in.Name, "/")+1:]
				hc.SetHeader("Content-Type", "application/octet-stream")
				hc.SetHeader("Content-Disposition", fmt.Sprintf("attachment; filename*=UTF-8''%s", url.PathEscape(base)))
				hc.SetHeader("Content-Length", fmt.Sprint(info.Size))
				hc.SetHeader("X-Content-Type-Options", "nosniff")
				hc.SetStatus(http.StatusOK)
				_, _ = io.Copy(hc.BodyWriter(), res)
			}}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "upload-object", Method: http.MethodPut, Path: "/objects/{bucket}/upload", Summary: "Upload an object (≤ 64 MiB)",
		Tags: tags, MaxBodyBytes: 64 << 20},
		func(ctx context.Context, in *struct {
			Bucket  string `path:"bucket"`
			Name    string `query:"name" required:"true" maxLength:"1024"`
			RawBody []byte `contentType:"application/octet-stream"`
		}) (*struct{ Body ObjectEntry }, error) {
			if managedObjectStore(in.Bucket) {
				return nil, huma.Error409Conflict(in.Bucket + " is managed by pulsed; publish blobs through the SDK")
			}
			obs, err := d.JS.ObjectStore(ctx, in.Bucket)
			if err != nil {
				return nil, kvErr(d, "upload-object", err)
			}
			info, err := obs.PutBytes(ctx, in.Name, in.RawBody)
			if err != nil {
				return nil, kvErr(d, "upload-object", err)
			}
			_ = audit.Write(ctx, d.DB, d.actor(ctx), "object.put", in.Bucket+"/"+in.Name, audit.OK, map[string]any{"size": info.Size})
			return &struct{ Body ObjectEntry }{ObjectEntry{Name: info.Name, Size: info.Size, Chunks: info.Chunks, Modified: info.ModTime, Digest: info.Digest}}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "delete-object", Method: http.MethodDelete, Path: "/objects/{bucket}/object", Summary: "Delete an object",
		Tags: tags, DefaultStatus: http.StatusNoContent},
		func(ctx context.Context, in *objInput) (*struct{}, error) {
			if managedObjectStore(in.Bucket) {
				return nil, huma.Error409Conflict(in.Bucket + " is managed by pulsed; events may reference its objects")
			}
			obs, err := d.JS.ObjectStore(ctx, in.Bucket)
			if err != nil {
				return nil, kvErr(d, "delete-object", err)
			}
			if err := obs.Delete(ctx, in.Name); err != nil {
				return nil, kvErr(d, "delete-object", err)
			}
			_ = audit.Write(ctx, d.DB, d.actor(ctx), "object.delete", in.Bucket+"/"+in.Name, audit.OK, nil)
			return &struct{}{}, nil
		})
}
