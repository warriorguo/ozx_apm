// Package s3test is an in-memory S3 for tests: the subset of the S3 API
// objstore uses (path-style), with Signature Version 4 checked on every
// request as the server sees it, the payload hash verified, ranged GETs,
// ListObjectsV2 and multipart uploads (parts below 5 MiB refused except the
// last, as S3 does). Faults can be injected per operation.
package s3test

import (
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// MinPartSize is S3's minimum size of a multipart part (all but the last).
const MinPartSize = 5 << 20

// Server is a fake S3 bucket.
type Server struct {
	// URL is the endpoint to configure; requests reach the fake through
	// HTTPClient (in process: no port is opened).
	URL       string
	Bucket    string
	AccessKey string
	SecretKey string

	mu       sync.Mutex
	objects  map[string]object
	uploads  map[string]*mpu
	nextID   int
	faults   map[string]int // op -> remaining failures (HTTP 500)
	Requests map[string]int // op -> count
	// Now is the clock of LastModified.
	Now func() time.Time
}

type object struct {
	data     []byte
	modified time.Time
}

type mpu struct {
	key   string
	parts map[int][]byte
	at    time.Time
}

// OpenMultipart starts a multipart upload directly (as a crashed client leaves it).
func (s *Server) OpenMultipart(key string, at time.Time) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	id := fmt.Sprintf("mpu-%d", s.nextID)
	s.uploads[id] = &mpu{key: key, parts: map[int][]byte{}, at: at}
	return id
}

// New starts a fake bucket.
func New(bucket string) *Server {
	s := &Server{Bucket: bucket, AccessKey: "test-access", SecretKey: "test-secret",
		objects: map[string]object{}, uploads: map[string]*mpu{}, faults: map[string]int{},
		Requests: map[string]int{}, Now: time.Now}
	s.URL = "http://fake-s3.test"
	return s
}

// Close is a no-op (nothing to stop).
func (s *Server) Close() {}

// Handler serves the fake (for a real listener).
func (s *Server) Handler() http.Handler { return http.HandlerFunc(s.serve) }

// HTTPClient returns a client whose requests are served in process.
func (s *Server) HTTPClient() *http.Client {
	return &http.Client{Transport: roundTripper{s}}
}

type roundTripper struct{ s *Server }

func (rt roundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := req.Context().Err(); err != nil {
		return nil, err
	}
	in := req.Clone(req.Context())
	if in.Host == "" {
		in.Host = req.URL.Host
	}
	in.RequestURI = req.URL.RequestURI()
	if req.Body == nil {
		in.Body = http.NoBody
	}
	rec := httptest.NewRecorder()
	rt.s.serve(rec, in)
	resp := rec.Result()
	resp.Request = req
	if cl := rec.Header().Get("Content-Length"); cl != "" {
		resp.ContentLength, _ = strconv.ParseInt(cl, 10, 64)
	}
	return resp, nil
}

// Fail makes the next n requests of op answer 500.
func (s *Server) Fail(op string, n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.faults[op] = n
}

// Object returns a stored object.
func (s *Server) Object(key string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.objects[key]
	return o.data, ok
}

// SetModified changes an object's LastModified.
func (s *Server) SetModified(key string, t time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if o, ok := s.objects[key]; ok {
		o.modified = t
		s.objects[key] = o
	}
}

// Keys returns the stored keys with the prefix, sorted.
func (s *Server) Keys(prefix string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var keys []string
	for k := range s.objects {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

// OpenUploads is the number of multipart uploads neither completed nor aborted.
func (s *Server) OpenUploads() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.uploads)
}

// Put stores an object directly.
func (s *Server) Put(key string, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[key] = object{data: append([]byte(nil), data...), modified: s.Now()}
}

func xmlError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	fmt.Fprintf(w, "<Error><Code>%s</Code><Message>%s</Message></Error>", code, msg)
}

func (s *Server) op(r *http.Request, key string) string {
	q := r.URL.Query()
	switch r.Method {
	case http.MethodPut:
		if key == "" {
			return "create-bucket"
		}
		if q.Has("partNumber") {
			return "upload-part"
		}
		return "put"
	case http.MethodGet:
		if key == "" {
			if q.Has("uploads") {
				return "list-multipart"
			}
			return "list"
		}
		if r.Header.Get("Range") != "" {
			return "get-range"
		}
		return "get"
	case http.MethodHead:
		return "head"
	case http.MethodDelete:
		if q.Has("uploadId") {
			return "abort-multipart"
		}
		return "delete"
	case http.MethodPost:
		if q.Has("uploads") {
			return "create-multipart"
		}
		return "complete-multipart"
	}
	return "unknown"
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/")
	bucket, key, _ := strings.Cut(path, "/")
	if bucket != s.Bucket {
		xmlError(w, http.StatusNotFound, "NoSuchBucket", "no such bucket")
		return
	}
	body, _ := io.ReadAll(r.Body)
	if msg := s.checkSignature(r, body); msg != "" {
		xmlError(w, http.StatusForbidden, "SignatureDoesNotMatch", msg)
		return
	}
	op := s.op(r, key)
	s.mu.Lock()
	s.Requests[op]++
	if s.faults[op] > 0 {
		s.faults[op]--
		s.mu.Unlock()
		xmlError(w, http.StatusInternalServerError, "InternalError", "injected")
		return
	}
	s.mu.Unlock()

	q := r.URL.Query()
	switch op {
	case "create-bucket":
		w.WriteHeader(http.StatusOK)
	case "put":
		s.Put(key, body)
		w.Header().Set("ETag", etag(body))
		w.WriteHeader(http.StatusOK)
	case "get", "get-range", "head":
		s.mu.Lock()
		o, ok := s.objects[key]
		s.mu.Unlock()
		if !ok {
			if op == "head" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			xmlError(w, http.StatusNotFound, "NoSuchKey", "no such key")
			return
		}
		data := o.data
		status := http.StatusOK
		if rg := r.Header.Get("Range"); rg != "" {
			var a, b int64
			if _, err := fmt.Sscanf(rg, "bytes=%d-%d", &a, &b); err != nil || a < 0 || a >= int64(len(data)) {
				xmlError(w, http.StatusRequestedRangeNotSatisfiable, "InvalidRange", "bad range")
				return
			}
			if b >= int64(len(data)) {
				b = int64(len(data)) - 1
			}
			data = data[a : b+1]
			status = http.StatusPartialContent
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		w.WriteHeader(status)
		if op != "head" {
			w.Write(data)
		}
	case "delete":
		s.mu.Lock()
		delete(s.objects, key)
		s.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	case "list":
		s.list(w, q)
	case "list-multipart":
		s.mu.Lock()
		var b strings.Builder
		b.WriteString("<ListMultipartUploadsResult>")
		ids := make([]string, 0, len(s.uploads))
		for id := range s.uploads {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			u := s.uploads[id]
			if strings.HasPrefix(u.key, q.Get("prefix")) {
				fmt.Fprintf(&b, "<Upload><Key>%s</Key><UploadId>%s</UploadId><Initiated>%s</Initiated></Upload>",
					u.key, id, u.at.UTC().Format(time.RFC3339Nano))
			}
		}
		b.WriteString("</ListMultipartUploadsResult>")
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/xml")
		w.Write([]byte(b.String()))
	case "create-multipart":
		s.mu.Lock()
		s.nextID++
		id := fmt.Sprintf("mpu-%d", s.nextID)
		s.uploads[id] = &mpu{key: key, parts: map[int][]byte{}, at: s.Now()}
		s.mu.Unlock()
		fmt.Fprintf(w, "<InitiateMultipartUploadResult><Bucket>%s</Bucket><Key>%s</Key><UploadId>%s</UploadId></InitiateMultipartUploadResult>", s.Bucket, key, id)
	case "upload-part":
		n, _ := strconv.Atoi(q.Get("partNumber"))
		s.mu.Lock()
		u, ok := s.uploads[q.Get("uploadId")]
		if ok && u.key == key && n >= 1 && n <= 10000 {
			u.parts[n] = body
		}
		s.mu.Unlock()
		if !ok || u.key != key {
			xmlError(w, http.StatusNotFound, "NoSuchUpload", "no such upload")
			return
		}
		w.Header().Set("ETag", etag(body))
		w.WriteHeader(http.StatusOK)
	case "complete-multipart":
		s.complete(w, key, q.Get("uploadId"), body)
	case "abort-multipart":
		s.mu.Lock()
		_, ok := s.uploads[q.Get("uploadId")]
		delete(s.uploads, q.Get("uploadId"))
		s.mu.Unlock()
		if !ok {
			xmlError(w, http.StatusNotFound, "NoSuchUpload", "no such upload")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		xmlError(w, http.StatusNotImplemented, "NotImplemented", op)
	}
}

func etag(b []byte) string {
	sum := md5.Sum(b)
	return `"` + hex.EncodeToString(sum[:]) + `"`
}

func (s *Server) list(w http.ResponseWriter, q url.Values) {
	prefix, after := q.Get("prefix"), q.Get("start-after")
	max, _ := strconv.Atoi(q.Get("max-keys"))
	if max <= 0 {
		max = 1000
	}
	keys := s.Keys(prefix)
	type content struct {
		Key          string
		Size         int
		LastModified string
	}
	var out struct {
		XMLName     xml.Name `xml:"ListBucketResult"`
		IsTruncated bool
		Contents    []content
	}
	s.mu.Lock()
	for _, k := range keys {
		if after != "" && k <= after {
			continue
		}
		if len(out.Contents) == max {
			out.IsTruncated = true
			break
		}
		o := s.objects[k]
		out.Contents = append(out.Contents, content{Key: k, Size: len(o.data), LastModified: o.modified.UTC().Format(time.RFC3339Nano)})
	}
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/xml")
	xml.NewEncoder(w).Encode(out)
}

func (s *Server) complete(w http.ResponseWriter, key, id string, body []byte) {
	var req struct {
		Parts []struct {
			PartNumber int
			ETag       string
		} `xml:"Part"`
	}
	if err := xml.Unmarshal(body, &req); err != nil || len(req.Parts) == 0 {
		xmlError(w, http.StatusBadRequest, "MalformedXML", "bad body")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.uploads[id]
	if !ok || u.key != key {
		xmlError(w, http.StatusNotFound, "NoSuchUpload", "no such upload")
		return
	}
	var data []byte
	for i, p := range req.Parts {
		part, ok := u.parts[p.PartNumber]
		if !ok || etag(part) != p.ETag || (i > 0 && p.PartNumber <= req.Parts[i-1].PartNumber) {
			xmlError(w, http.StatusBadRequest, "InvalidPart", "bad part")
			return
		}
		if i < len(req.Parts)-1 && len(part) < MinPartSize {
			xmlError(w, http.StatusBadRequest, "EntityTooSmall", "part too small")
			return
		}
		data = append(data, part...)
	}
	delete(s.uploads, id)
	s.objects[key] = object{data: data, modified: s.Now()}
	fmt.Fprintf(w, "<CompleteMultipartUploadResult><Key>%s</Key></CompleteMultipartUploadResult>", key)
}

// checkSignature recomputes the request's SigV4 signature from what was
// received (independently of objstore's signer) and checks the payload hash.
func (s *Server) checkSignature(r *http.Request, body []byte) string {
	auth := r.Header.Get("Authorization")
	const prefix = "AWS4-HMAC-SHA256 Credential="
	if !strings.HasPrefix(auth, prefix) {
		return "no SigV4 authorization"
	}
	fields := map[string]string{}
	for _, part := range strings.Split(strings.TrimPrefix(auth, "AWS4-HMAC-SHA256 "), ", ") {
		k, v, _ := strings.Cut(part, "=")
		fields[k] = v
	}
	cred := strings.Split(fields["Credential"], "/")
	if len(cred) != 5 || cred[0] != s.AccessKey || cred[3] != "s3" || cred[4] != "aws4_request" {
		return "bad credential"
	}
	payload := r.Header.Get("X-Amz-Content-Sha256")
	sum := sha256.Sum256(body)
	if payload != hex.EncodeToString(sum[:]) {
		return "payload hash mismatch"
	}
	amzDate := r.Header.Get("X-Amz-Date")
	if len(amzDate) != 16 || amzDate[:8] != cred[1] {
		return "bad date"
	}
	signed := strings.Split(fields["SignedHeaders"], ";")
	var ch strings.Builder
	for _, h := range signed {
		v := r.Header.Get(h)
		if h == "host" {
			v = r.Host
		}
		ch.WriteString(h + ":" + strings.TrimSpace(v) + "\n")
	}
	// Canonical query from the raw query, as received.
	var pairs []string
	for k, vs := range r.URL.Query() {
		for _, v := range vs {
			pairs = append(pairs, enc(k)+"="+enc(v))
		}
	}
	sort.Strings(pairs)
	canonical := r.Method + "\n" + r.URL.EscapedPath() + "\n" + strings.Join(pairs, "&") + "\n" + ch.String() + "\n" +
		fields["SignedHeaders"] + "\n" + payload
	h := sha256.Sum256([]byte(canonical))
	scope := strings.Join(cred[1:], "/")
	sts := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hex.EncodeToString(h[:])
	key := mac([]byte("AWS4"+s.SecretKey), cred[1])
	key = mac(key, cred[2])
	key = mac(key, "s3")
	key = mac(key, "aws4_request")
	if hex.EncodeToString(mac(key, sts)) != fields["Signature"] {
		return "signature mismatch"
	}
	return ""
}

func mac(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}

func enc(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}
