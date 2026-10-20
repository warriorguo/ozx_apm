package objstore_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/warriorguo/ozx_apm/server/internal/objstore"
	"github.com/warriorguo/ozx_apm/server/internal/objstore/s3test"
)

func newClient(t *testing.T, s *s3test.Server, secret string) *objstore.Client {
	t.Helper()
	c, err := objstore.New(objstore.Config{Endpoint: s.URL, Bucket: s.Bucket, AccessKey: s.AccessKey, SecretKey: secret}, s.HTTPClient())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestClient_ObjectRoundTrip(t *testing.T) {
	s := s3test.New("bucket-a")
	defer s.Close()
	c := newClient(t, s, s.SecretKey)
	ctx := context.Background()

	key := "session-files/tmp/0123/000001 $+ü"
	data := []byte("hello, world")
	if err := c.PutObject(ctx, key, data, "application/octet-stream"); err != nil {
		t.Fatal(err)
	}
	if got, ok := s.Object(key); !ok || !bytes.Equal(got, data) {
		t.Fatalf("stored %q %v (keys %v)", got, ok, s.Keys(""))
	}
	rc, n, err := c.GetObject(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if n != int64(len(data)) || !bytes.Equal(got, data) {
		t.Fatalf("get %q (%d)", got, n)
	}
	part, err := c.GetRange(ctx, key, 7, 100)
	if err != nil || string(part) != "world" {
		t.Fatalf("range %q %v", part, err)
	}
	if size, err := c.HeadObject(ctx, key); err != nil || size != int64(len(data)) {
		t.Fatalf("head %d %v", size, err)
	}
	if err := c.DeleteObject(ctx, key); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteObject(ctx, key); err != nil {
		t.Fatalf("deleting a missing object: %v", err)
	}
	if _, _, err := c.GetObject(ctx, key); !objstore.IsNotFound(err) {
		t.Fatalf("get after delete: %v", err)
	}
	if _, err := c.HeadObject(ctx, key); !objstore.IsNotFound(err) {
		t.Fatalf("head after delete: %v", err)
	}
}

func TestClient_WrongSecretIsRefused(t *testing.T) {
	s := s3test.New("bucket-a")
	defer s.Close()
	c := newClient(t, s, "wrong")
	err := c.PutObject(context.Background(), "k", []byte("x"), "")
	if err == nil || !strings.Contains(err.Error(), "SignatureDoesNotMatch") {
		t.Fatalf("err %v", err)
	}
	if strings.Contains(err.Error(), "wrong") {
		t.Fatalf("the secret leaked into the error: %v", err)
	}
}

func TestClient_ListPaginates(t *testing.T) {
	s := s3test.New("b")
	defer s.Close()
	c := newClient(t, s, s.SecretKey)
	ctx := context.Background()
	for i := 0; i < 25; i++ {
		s.Put(fmt.Sprintf("p/%03d", i), []byte{byte(i)})
	}
	s.Put("q/other", []byte("x"))
	objs, more, err := c.ListObjects(ctx, "p/", "", 10)
	if err != nil || len(objs) != 10 || !more || objs[0].Key != "p/000" || objs[0].Size != 1 || objs[0].LastModified.IsZero() {
		t.Fatalf("page 1: %v %v %v", objs, more, err)
	}
	objs, more, err = c.ListObjects(ctx, "p/", "p/019", 10)
	if err != nil || len(objs) != 5 || more || objs[0].Key != "p/020" {
		t.Fatalf("last page: %v %v %v", objs, more, err)
	}
	all, err := c.ListAll(ctx, "p/", 1000)
	if err != nil || len(all) != 25 {
		t.Fatalf("all: %d %v", len(all), err)
	}
}

func TestClient_Multipart(t *testing.T) {
	s := s3test.New("b")
	defer s.Close()
	c := newClient(t, s, s.SecretKey)
	ctx := context.Background()
	p1 := bytes.Repeat([]byte("a"), s3test.MinPartSize)
	p2 := []byte("tail")
	id, err := c.CreateMultipartUpload(ctx, "big.zip", "application/zip")
	if err != nil {
		t.Fatal(err)
	}
	e1, err := c.UploadPart(ctx, "big.zip", id, 1, p1)
	if err != nil {
		t.Fatal(err)
	}
	e2, err := c.UploadPart(ctx, "big.zip", id, 2, p2)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.CompleteMultipartUpload(ctx, "big.zip", id, []objstore.Part{{1, e1}, {2, e2}}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Object("big.zip")
	if !bytes.Equal(got, append(append([]byte{}, p1...), p2...)) || s.OpenUploads() != 0 {
		t.Fatalf("assembled %d bytes, open %d", len(got), s.OpenUploads())
	}

	// Abort discards; aborting again is not an error.
	id, _ = c.CreateMultipartUpload(ctx, "x.zip", "")
	if err := c.AbortMultipartUpload(ctx, "x.zip", id); err != nil {
		t.Fatal(err)
	}
	if err := c.AbortMultipartUpload(ctx, "x.zip", id); err != nil {
		t.Fatal(err)
	}
	// A too small first part is refused by the store (S3's rule).
	id, _ = c.CreateMultipartUpload(ctx, "y.zip", "")
	a, _ := c.UploadPart(ctx, "y.zip", id, 1, []byte("small"))
	b, _ := c.UploadPart(ctx, "y.zip", id, 2, []byte("small"))
	if err := c.CompleteMultipartUpload(ctx, "y.zip", id, []objstore.Part{{1, a}, {2, b}}); err == nil {
		t.Fatal("small parts accepted")
	}
}

func TestClient_RetriesTransientErrors(t *testing.T) {
	s := s3test.New("b")
	defer s.Close()
	c := newClient(t, s, s.SecretKey)
	ctx := context.Background()
	s.Fail("put", 2)
	if err := c.PutObject(ctx, "k", []byte("v"), ""); err != nil {
		t.Fatalf("two failures then success: %v", err)
	}
	s.Fail("put", 3)
	if err := c.PutObject(ctx, "k2", []byte("v"), ""); err == nil {
		t.Fatal("three failures: want an error")
	}
	// Not retried: starting a multipart upload twice would leave one open.
	s.Fail("create-multipart", 1)
	if _, err := c.CreateMultipartUpload(ctx, "m", ""); err == nil || s.Requests["create-multipart"] != 1 {
		t.Fatalf("create-multipart: %v, %d requests", err, s.Requests["create-multipart"])
	}
}

func TestClient_ContextCancel(t *testing.T) {
	s := s3test.New("b")
	defer s.Close()
	c := newClient(t, s, s.SecretKey)
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	time.Sleep(time.Millisecond)
	if err := c.PutObject(ctx, "k", []byte("v"), ""); err == nil {
		t.Fatal("cancelled context: want an error")
	}
}

func TestNew_Validates(t *testing.T) {
	bad := []objstore.Config{
		{Bucket: "b", AccessKey: "a", SecretKey: "s"},
		{Endpoint: "http://h", AccessKey: "a", SecretKey: "s"},
		{Endpoint: "http://h", Bucket: "b"},
		{Endpoint: "ftp://h", Bucket: "b", AccessKey: "a", SecretKey: "s"},
		{Endpoint: "http://h", Bucket: "b/c", AccessKey: "a", SecretKey: "s"},
	}
	for _, cfg := range bad {
		if _, err := objstore.New(cfg, nil); err == nil {
			t.Errorf("accepted %+v", cfg)
		} else if cfg.SecretKey != "" && strings.Contains(err.Error(), cfg.SecretKey+"x") {
			t.Errorf("secret in error")
		}
	}
}

// A store that ignores Range (answers 200 with the whole object): GetRange
// still returns the requested bytes (the archive check reads the end of an
// object this way).
func TestClient_GetRangeOnAStoreIgnoringRange(t *testing.T) {
	s := s3test.New("b")
	defer s.Close()
	c := newClient(t, s, s.SecretKey)
	data := bytes.Repeat([]byte("0123456789"), 100)
	s.Put("k", data)
	s.IgnoreRange = true
	for _, tc := range []struct{ off, n int64 }{{0, 5}, {3, 4}, {500, 20}, {995, 50}} {
		got, err := c.GetRange(context.Background(), "k", tc.off, tc.n)
		end := tc.off + tc.n
		if end > int64(len(data)) {
			end = int64(len(data))
		}
		if err != nil || !bytes.Equal(got, data[tc.off:end]) {
			t.Errorf("off %d n %d: %q %v", tc.off, tc.n, got, err)
		}
	}
}

// A streamed GetObject is not cut by the client's per-request timeout;
// the other calls read their bodies within it.
func TestClient_StreamOutlivesTheTimeout(t *testing.T) {
	s := s3test.New("b")
	defer s.Close()
	c, err := objstore.New(objstore.Config{Endpoint: s.URL, Bucket: s.Bucket, AccessKey: s.AccessKey, SecretKey: s.SecretKey,
		Timeout: 50 * time.Millisecond}, s.HTTPClient())
	if err != nil {
		t.Fatal(err)
	}
	s.Put("k", []byte("hello"))
	rc, _, err := c.GetObject(context.Background(), "k")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(120 * time.Millisecond)
	got, err := io.ReadAll(rc)
	rc.Close()
	if err != nil || string(got) != "hello" {
		t.Fatalf("%q %v", got, err)
	}
	if err := c.PutObject(context.Background(), "k2", []byte("x"), ""); err != nil {
		t.Fatal(err)
	}
	time.Sleep(120 * time.Millisecond)
	objs, _, err := c.ListObjects(context.Background(), "", "", 10)
	if err != nil || len(objs) != 2 {
		t.Fatalf("%v %v", objs, err)
	}
}
