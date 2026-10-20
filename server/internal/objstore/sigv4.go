package objstore

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// AWS Signature Version 4 for S3 (header-based authorization), as documented
// in "Authenticating Requests: Using the Authorization Header (AWS Signature
// Version 4)". Only what this client sends is supported: the payload hash is
// always given (the client signs the SHA-256 of every body it sends, so the
// object store verifies each body's integrity), the signed headers are host,
// range, content-md5, content-type, date and every x-amz-* header.
// Checked against the document's example signatures (sigv4_test.go).

const (
	sigAlgorithm = "AWS4-HMAC-SHA256"
	sigService   = "s3"
	amzDateFmt   = "20060102T150405Z"
	// EmptySHA256 is the hex SHA-256 of an empty body.
	EmptySHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
)

// signedHeader reports whether a header takes part in the signature.
func signedHeader(lower string) bool {
	switch lower {
	case "host", "range", "content-md5", "content-type", "date":
		return true
	}
	return strings.HasPrefix(lower, "x-amz-")
}

// signV4 adds x-amz-date, x-amz-content-sha256 and Authorization to req.
// payloadHash is the hex SHA-256 of the body (EmptySHA256 for none).
func signV4(req *http.Request, accessKey, secretKey, region, payloadHash string, now time.Time) {
	now = now.UTC()
	amzDate := now.Format(amzDateFmt)
	day := amzDate[:8]
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)

	host := req.Host
	if host == "" {
		host = req.URL.Host
	}
	canonical, signedNames := canonicalRequest(req.Method, req.URL, host, req.Header, payloadHash)
	scope := day + "/" + region + "/" + sigService + "/aws4_request"
	sum := sha256.Sum256([]byte(canonical))
	stringToSign := sigAlgorithm + "\n" + amzDate + "\n" + scope + "\n" + hex.EncodeToString(sum[:])
	key := signingKey(secretKey, day, region)
	signature := hex.EncodeToString(hmacSHA256(key, []byte(stringToSign)))
	req.Header.Set("Authorization", sigAlgorithm+" Credential="+accessKey+"/"+scope+
		", SignedHeaders="+signedNames+", Signature="+signature)
}

// canonicalRequest returns the canonical request and the signed header list.
func canonicalRequest(method string, u *url.URL, host string, header http.Header, payloadHash string) (string, string) {
	type kv struct{ k, v string }
	headers := []kv{{"host", strings.TrimSpace(host)}}
	for name, values := range header {
		lower := strings.ToLower(name)
		if lower == "host" || !signedHeader(lower) {
			continue
		}
		trimmed := make([]string, len(values))
		for i, v := range values {
			trimmed[i] = strings.Join(strings.Fields(v), " ")
		}
		headers = append(headers, kv{lower, strings.Join(trimmed, ",")})
	}
	sort.Slice(headers, func(i, j int) bool { return headers[i].k < headers[j].k })
	var ch strings.Builder
	names := make([]string, len(headers))
	for i, h := range headers {
		ch.WriteString(h.k + ":" + h.v + "\n")
		names[i] = h.k
	}
	signed := strings.Join(names, ";")

	// The client sends the path in exactly this encoding (URL.RawPath).
	path := encodePath(u.Path)
	if path == "" {
		path = "/"
	}
	return method + "\n" + path + "\n" + canonicalQuery(u.Query()) + "\n" + ch.String() + "\n" + signed + "\n" + payloadHash, signed
}

// canonicalQuery: keys and values URI-encoded, sorted by key, then value.
func canonicalQuery(q url.Values) string {
	if len(q) == 0 {
		return ""
	}
	type kv struct{ k, v string }
	pairs := make([]kv, 0, len(q))
	for k, vs := range q {
		ek := uriEncode(k, true)
		for _, v := range vs {
			pairs = append(pairs, kv{ek, uriEncode(v, true)})
		}
	}
	// By encoded key, then value (not the joined "k=v": "a-b" < "a=" would differ).
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].k != pairs[j].k {
			return pairs[i].k < pairs[j].k
		}
		return pairs[i].v < pairs[j].v
	})
	out := make([]string, len(pairs))
	for i, p := range pairs {
		out[i] = p.k + "=" + p.v
	}
	return strings.Join(out, "&")
}

// encodePath URI-encodes every segment of an object path ('/' kept).
func encodePath(p string) string {
	return uriEncode(p, false)
}

// uriEncode is S3's UriEncode: every byte except A-Z a-z 0-9 - _ . ~ is
// percent-encoded (upper-case hex); '/' too when encodeSlash.
func uriEncode(s string, encodeSlash bool) string {
	const hexDigits = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case 'A' <= c && c <= 'Z', 'a' <= c && c <= 'z', '0' <= c && c <= '9', c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		case c == '/' && !encodeSlash:
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(hexDigits[c>>4])
			b.WriteByte(hexDigits[c&15])
		}
	}
	return b.String()
}

func signingKey(secret, day, region string) []byte {
	k := hmacSHA256([]byte("AWS4"+secret), []byte(day))
	k = hmacSHA256(k, []byte(region))
	k = hmacSHA256(k, []byte(sigService))
	return hmacSHA256(k, []byte("aws4_request"))
}

func hmacSHA256(key, data []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(data)
	return m.Sum(nil)
}

// SHA256Hex returns the hex SHA-256 of data.
func SHA256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
