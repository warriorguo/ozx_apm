// Package objstore is a small S3 client for the session files of APM-13:
// path-style requests to an S3-compatible store (SeaweedFS on n192, AWS S3,
// MinIO), signed with AWS Signature Version 4, standard library only.
//
// Why not minio-go or the AWS SDK: the server needs nine operations (put,
// get, ranged get, head, delete, list, and the four multipart calls), the
// module is on Go 1.21 with a Go 1.22 builder image (current minio-go and
// AWS SDK v2 releases require newer Go), and both would add a dozen
// transitive modules to a service whose dependency set has stayed small.
// The signature is checked against AWS's documented example signatures
// (sigv4_test.go) and the client against a real S3 implementation by the
// integration tests (tests/integration/apm13_test.go, OZX_TEST_S3_ENDPOINT).
//
// Every body the client sends is in memory and signed with its SHA-256
// (x-amz-content-sha256), so the store verifies its integrity. Idempotent
// calls are retried on transport errors and 5xx (3 attempts).
package objstore

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Config locates the bucket. The secret key is never logged or returned.
type Config struct {
	// Endpoint, e.g. http://192.168.0.192:18333 (path-style addressing:
	// http://<endpoint>/<bucket>/<key>).
	Endpoint  string
	Bucket    string
	Region    string // default us-east-1
	AccessKey string
	SecretKey string
	// Timeout of one request (default 60 s; a body is at most 8 MiB), except
	// GetObject's body, which is streamed for as long as the caller's context
	// allows (a download to a slow browser); its headers must still arrive
	// within it.
	Timeout time.Duration
}

// Client talks to one bucket.
type Client struct {
	base      *url.URL
	bucket    string
	region    string
	accessKey string
	secretKey string
	http      *http.Client
	timeout   time.Duration
	now       func() time.Time
	// retryDelay between attempts (tests shorten it).
	retryDelay time.Duration
}

// Error is an S3 error response (or an unexpected status).
type Error struct {
	Op      string
	Key     string
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string {
	s := fmt.Sprintf("s3 %s %s: HTTP %d", e.Op, e.Key, e.Status)
	if e.Code != "" {
		s += " " + e.Code
	}
	if e.Message != "" {
		s += ": " + e.Message
	}
	return s
}

// IsNotFound reports a missing object, upload or bucket.
func IsNotFound(err error) bool {
	var e *Error
	return errors.As(err, &e) && (e.Status == http.StatusNotFound || e.Code == "NoSuchKey" || e.Code == "NoSuchUpload")
}

// New validates the configuration. httpClient nil: a client with cfg.Timeout.
func New(cfg Config, httpClient *http.Client) (*Client, error) {
	if cfg.Endpoint == "" || cfg.Bucket == "" {
		return nil, errors.New("objstore: endpoint and bucket are required")
	}
	if cfg.AccessKey == "" || cfg.SecretKey == "" {
		return nil, errors.New("objstore: access key and secret key are required")
	}
	u, err := url.Parse(strings.TrimRight(cfg.Endpoint, "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("objstore: endpoint must be http(s)://host[:port], got %q", cfg.Endpoint)
	}
	if strings.ContainsAny(cfg.Bucket, "/?#") {
		return nil, fmt.Errorf("objstore: invalid bucket %q", cfg.Bucket)
	}
	region := cfg.Region
	if region == "" {
		region = "us-east-1"
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	if httpClient == nil {
		// No client-wide Timeout: it would also cut a streamed download.
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.ResponseHeaderTimeout = timeout
		httpClient = &http.Client{Transport: transport}
	}
	return &Client{base: u, bucket: cfg.Bucket, region: region, accessKey: cfg.AccessKey, secretKey: cfg.SecretKey,
		http: httpClient, timeout: timeout, now: time.Now, retryDelay: 300 * time.Millisecond}, nil
}

// Bucket returns the bucket name.
func (c *Client) Bucket() string { return c.bucket }

// Endpoint returns the endpoint (no credentials in it).
func (c *Client) Endpoint() string { return c.base.String() }

// request is one S3 call.
type request struct {
	op      string
	method  string
	key     string // "" for the bucket itself
	query   url.Values
	header  http.Header
	body    []byte
	retry   bool // idempotent: retried on transport errors and 5xx
	okCodes []int
	stream  bool // the body is read by the caller for as long as its context allows
}

func (c *Client) url(key string, query url.Values) *url.URL {
	u := *c.base
	p := strings.TrimRight(u.Path, "/") + "/" + c.bucket
	if key != "" {
		p += "/" + key
	}
	u.Path = p
	u.RawPath = encodePath(p)
	u.RawQuery = canonicalQuery(query)
	return &u
}

// do sends the request and returns the response (body open) for a 2xx or
// one of okCodes; anything else is an *Error (body closed).
func (c *Client) do(ctx context.Context, r request) (*http.Response, error) {
	attempts := 1
	if r.retry {
		attempts = 3
	}
	payloadHash := EmptySHA256
	if len(r.body) > 0 {
		payloadHash = SHA256Hex(r.body)
	}
	var lastErr error
	if !r.stream {
		// The whole exchange, body included, within the timeout: the (small)
		// body is read here, before the deadline is released.
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.timeout*time.Duration(attempts))
		defer cancel()
	}
	for i := 0; i < attempts; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(c.retryDelay * time.Duration(i)):
			}
		}
		u := c.url(r.key, r.query)
		req, err := http.NewRequestWithContext(ctx, r.method, u.String(), bytes.NewReader(r.body))
		if err != nil {
			return nil, err
		}
		req.URL = u
		req.ContentLength = int64(len(r.body))
		if len(r.body) == 0 {
			req.Body = http.NoBody
		}
		for k, vs := range r.header {
			for _, v := range vs {
				req.Header.Add(k, v)
			}
		}
		signV4(req, c.accessKey, c.secretKey, c.region, payloadHash, c.now())
		resp, err := c.http.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			lastErr = fmt.Errorf("s3 %s %s: %w", r.op, r.key, err)
			continue
		}
		if resp.StatusCode/100 == 2 || containsInt(r.okCodes, resp.StatusCode) {
			if !r.stream {
				body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
				resp.Body.Close()
				if err != nil {
					lastErr = fmt.Errorf("s3 %s %s: %w", r.op, r.key, err)
					if r.retry {
						continue
					}
					return nil, lastErr
				}
				resp.Body = io.NopCloser(bytes.NewReader(body))
			}
			return resp, nil
		}
		e := readError(resp, r.op, r.key)
		resp.Body.Close()
		if resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests {
			lastErr = e
			continue
		}
		return nil, e
	}
	return nil, lastErr
}

func containsInt(list []int, v int) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func readError(resp *http.Response, op, key string) *Error {
	e := &Error{Op: op, Key: key, Status: resp.StatusCode}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	var x struct {
		Code    string `xml:"Code"`
		Message string `xml:"Message"`
	}
	if xml.Unmarshal(body, &x) == nil {
		e.Code, e.Message = x.Code, x.Message
	}
	if len(e.Message) > 300 {
		e.Message = e.Message[:300]
	}
	return e
}

func drain(resp *http.Response) {
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
}

// PutObject stores data under key.
func (c *Client) PutObject(ctx context.Context, key string, data []byte, contentType string) error {
	h := http.Header{}
	if contentType != "" {
		h.Set("Content-Type", contentType)
	}
	resp, err := c.do(ctx, request{op: "put", method: http.MethodPut, key: key, header: h, body: data, retry: true})
	if err != nil {
		return err
	}
	drain(resp)
	return nil
}

// GetObject opens the object; the caller closes the reader.
func (c *Client) GetObject(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	resp, err := c.do(ctx, request{op: "get", method: http.MethodGet, key: key, retry: true, stream: true})
	if err != nil {
		return nil, 0, err
	}
	return resp.Body, resp.ContentLength, nil
}

// GetRange reads n bytes at off (fewer at the end of the object).
func (c *Client) GetRange(ctx context.Context, key string, off, n int64) ([]byte, error) {
	if n <= 0 {
		return nil, nil
	}
	h := http.Header{}
	h.Set("Range", "bytes="+strconv.FormatInt(off, 10)+"-"+strconv.FormatInt(off+n-1, 10))
	resp, err := c.do(ctx, request{op: "get-range", method: http.MethodGet, key: key, header: h, retry: true})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, n+1))
	if err != nil {
		return nil, fmt.Errorf("s3 get-range %s: %w", key, err)
	}
	if resp.StatusCode == http.StatusOK && off > 0 {
		// A store that ignored the range sends the whole object: skip to off.
		resp.Body.Close()
		resp, err = c.do(ctx, request{op: "get-range", method: http.MethodGet, key: key, retry: true})
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if _, err := io.CopyN(io.Discard, resp.Body, off); err != nil {
			return nil, fmt.Errorf("s3 get-range %s: the object is shorter than %d bytes: %w", key, off, err)
		}
		data, err = io.ReadAll(io.LimitReader(resp.Body, n+1))
		if err != nil {
			return nil, fmt.Errorf("s3 get-range %s: %w", key, err)
		}
	}
	if int64(len(data)) > n {
		data = data[:n]
	}
	return data, nil
}

// HeadObject returns the object's size.
func (c *Client) HeadObject(ctx context.Context, key string) (int64, error) {
	resp, err := c.do(ctx, request{op: "head", method: http.MethodHead, key: key, retry: true})
	if err != nil {
		return 0, err
	}
	drain(resp)
	return resp.ContentLength, nil
}

// DeleteObject deletes the object; a missing object is not an error.
func (c *Client) DeleteObject(ctx context.Context, key string) error {
	resp, err := c.do(ctx, request{op: "delete", method: http.MethodDelete, key: key, retry: true, okCodes: []int{http.StatusNotFound}})
	if err != nil {
		return err
	}
	drain(resp)
	return nil
}

// Object is one entry of a listing.
type Object struct {
	Key          string
	Size         int64
	LastModified time.Time
}

// ListObjects lists up to max keys with the prefix after startAfter (ListObjectsV2, in key order).
func (c *Client) ListObjects(ctx context.Context, prefix, startAfter string, max int) ([]Object, bool, error) {
	if max <= 0 || max > 1000 {
		max = 1000
	}
	q := url.Values{}
	q.Set("list-type", "2")
	q.Set("prefix", prefix)
	q.Set("max-keys", strconv.Itoa(max))
	if startAfter != "" {
		q.Set("start-after", startAfter)
	}
	resp, err := c.do(ctx, request{op: "list", method: http.MethodGet, query: q, retry: true})
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	var out struct {
		IsTruncated bool `xml:"IsTruncated"`
		Contents    []struct {
			Key          string `xml:"Key"`
			Size         int64  `xml:"Size"`
			LastModified string `xml:"LastModified"`
		} `xml:"Contents"`
	}
	if err := xml.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&out); err != nil {
		return nil, false, fmt.Errorf("s3 list %s: %w", prefix, err)
	}
	objs := make([]Object, 0, len(out.Contents))
	for _, o := range out.Contents {
		t, _ := time.Parse(time.RFC3339Nano, o.LastModified)
		objs = append(objs, Object{Key: o.Key, Size: o.Size, LastModified: t})
	}
	return objs, out.IsTruncated && len(objs) > 0, nil
}

// ListAll lists every key with the prefix (bounded by limit).
func (c *Client) ListAll(ctx context.Context, prefix string, limit int) ([]Object, error) {
	var all []Object
	after := ""
	for {
		objs, more, err := c.ListObjects(ctx, prefix, after, 1000)
		if err != nil {
			return nil, err
		}
		all = append(all, objs...)
		if !more || len(all) >= limit {
			return all, nil
		}
		after = objs[len(objs)-1].Key
	}
}

// Part is a completed part of a multipart upload.
type Part struct {
	Number int
	ETag   string
}

// CreateMultipartUpload starts a multipart upload and returns its id.
func (c *Client) CreateMultipartUpload(ctx context.Context, key, contentType string) (string, error) {
	h := http.Header{}
	if contentType != "" {
		h.Set("Content-Type", contentType)
	}
	q := url.Values{"uploads": {""}}
	resp, err := c.do(ctx, request{op: "create-multipart", method: http.MethodPost, key: key, query: q, header: h})
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		UploadID string `xml:"UploadId"`
	}
	if err := xml.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil || out.UploadID == "" {
		return "", fmt.Errorf("s3 create-multipart %s: no upload id (%v)", key, err)
	}
	return out.UploadID, nil
}

// UploadPart sends part n (1-based) and returns its ETag.
func (c *Client) UploadPart(ctx context.Context, key, uploadID string, n int, data []byte) (string, error) {
	q := url.Values{"partNumber": {strconv.Itoa(n)}, "uploadId": {uploadID}}
	resp, err := c.do(ctx, request{op: "upload-part", method: http.MethodPut, key: key, query: q, body: data, retry: true})
	if err != nil {
		return "", err
	}
	drain(resp)
	etag := resp.Header.Get("ETag")
	if etag == "" {
		return "", fmt.Errorf("s3 upload-part %s #%d: no ETag", key, n)
	}
	return etag, nil
}

// CompleteMultipartUpload joins the parts into the object.
func (c *Client) CompleteMultipartUpload(ctx context.Context, key, uploadID string, parts []Part) error {
	var b strings.Builder
	b.WriteString("<CompleteMultipartUpload>")
	for _, p := range parts {
		b.WriteString("<Part><PartNumber>" + strconv.Itoa(p.Number) + "</PartNumber><ETag>")
		xml.EscapeText(&b, []byte(p.ETag))
		b.WriteString("</ETag></Part>")
	}
	b.WriteString("</CompleteMultipartUpload>")
	q := url.Values{"uploadId": {uploadID}}
	h := http.Header{}
	h.Set("Content-Type", "application/xml")
	resp, err := c.do(ctx, request{op: "complete-multipart", method: http.MethodPost, key: key, query: q, header: h, body: []byte(b.String())})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// S3 may answer 200 with an error document.
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if bytes.Contains(body, []byte("<Error>")) {
		var x struct {
			Code    string `xml:"Code"`
			Message string `xml:"Message"`
		}
		xml.Unmarshal(body, &x)
		return &Error{Op: "complete-multipart", Key: key, Status: resp.StatusCode, Code: x.Code, Message: x.Message}
	}
	return nil
}

// AbortMultipartUpload discards a multipart upload; a missing one is not an error.
func (c *Client) AbortMultipartUpload(ctx context.Context, key, uploadID string) error {
	q := url.Values{"uploadId": {uploadID}}
	resp, err := c.do(ctx, request{op: "abort-multipart", method: http.MethodDelete, key: key, query: q, retry: true, okCodes: []int{http.StatusNotFound}})
	if err != nil {
		return err
	}
	drain(resp)
	return nil
}

// MultipartUpload is a multipart upload neither completed nor aborted.
type MultipartUpload struct {
	Key       string
	UploadID  string
	Initiated time.Time
}

// ListMultipartUploads lists open multipart uploads under the prefix (up
// to 1000; the cleaner aborts those a crashed assembly left behind).
func (c *Client) ListMultipartUploads(ctx context.Context, prefix string) ([]MultipartUpload, error) {
	q := url.Values{"uploads": {""}, "prefix": {prefix}}
	resp, err := c.do(ctx, request{op: "list-multipart", method: http.MethodGet, query: q, retry: true})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out struct {
		Uploads []struct {
			Key       string `xml:"Key"`
			UploadID  string `xml:"UploadId"`
			Initiated string `xml:"Initiated"`
		} `xml:"Upload"`
	}
	if err := xml.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&out); err != nil {
		return nil, fmt.Errorf("s3 list-multipart %s: %w", prefix, err)
	}
	ups := make([]MultipartUpload, 0, len(out.Uploads))
	for _, u := range out.Uploads {
		t, _ := time.Parse(time.RFC3339Nano, u.Initiated)
		ups = append(ups, MultipartUpload{Key: u.Key, UploadID: u.UploadID, Initiated: t})
	}
	return ups, nil
}

// CreateBucket creates the bucket (tests and local setups; production
// buckets are created by an admin). An existing bucket owned by the caller
// is not an error.
func (c *Client) CreateBucket(ctx context.Context) error {
	resp, err := c.do(ctx, request{op: "create-bucket", method: http.MethodPut, okCodes: []int{http.StatusConflict}})
	if err != nil {
		return err
	}
	drain(resp)
	return nil
}
