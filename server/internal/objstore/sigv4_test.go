package objstore

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// The examples of AWS's "Signature Calculations for the Authorization
// Header: Transferring Payload in a Single Chunk (AWS Signature Version 4)"
// page (virtual-hosted style, bucket examplebucket, us-east-1, the documented
// example credentials and time). The signatures are the documented ones.
const (
	exampleAccessKey = "AKIAIOSFODNN7EXAMPLE"
	exampleSecretKey = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
)

var exampleTime = time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC)

func exampleRequest(t *testing.T, method, rawURL string, header map[string]string) *http.Request {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	req := &http.Request{Method: method, URL: u, Host: u.Host, Header: http.Header{}}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	return req
}

func signatureOf(req *http.Request) string {
	auth := req.Header.Get("Authorization")
	i := strings.Index(auth, "Signature=")
	if i < 0 {
		return ""
	}
	return auth[i+len("Signature="):]
}

func TestSigV4_AWSExamples(t *testing.T) {
	cases := []struct {
		name, method, url string
		header            map[string]string
		payload           string
		wantSigned        string
		want              string
	}{
		{
			name: "GET object", method: "GET", url: "https://examplebucket.s3.amazonaws.com/test.txt",
			header:     map[string]string{"Range": "bytes=0-9"},
			payload:    EmptySHA256,
			wantSigned: "host;range;x-amz-content-sha256;x-amz-date",
			want:       "f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41",
		},
		{
			name: "PUT object", method: "PUT", url: "https://examplebucket.s3.amazonaws.com/test$file.text",
			header: map[string]string{
				"Date":                "Fri, 24 May 2013 00:00:00 GMT",
				"X-Amz-Storage-Class": "REDUCED_REDUNDANCY",
			},
			payload:    SHA256Hex([]byte("Welcome to Amazon S3.")),
			wantSigned: "date;host;x-amz-content-sha256;x-amz-date;x-amz-storage-class",
			want:       "98ad721746da40c64f1a55b78f14c238d841ea1380cd77a1b5971af0ece108bd",
		},
		{
			name: "GET bucket lifecycle", method: "GET", url: "https://examplebucket.s3.amazonaws.com/?lifecycle",
			payload:    EmptySHA256,
			wantSigned: "host;x-amz-content-sha256;x-amz-date",
			want:       "fea454ca298b7da1c68078a5d1bdbfbbe0d65c699e0f91ac7a200a0136783543",
		},
		{
			name: "list objects", method: "GET", url: "https://examplebucket.s3.amazonaws.com/?max-keys=2&prefix=J",
			payload:    EmptySHA256,
			wantSigned: "host;x-amz-content-sha256;x-amz-date",
			want:       "34b48302e7b5fa45bde8084f4b7868a86f0a534bc59db6670ed5711ef69dc6f7",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := exampleRequest(t, c.method, c.url, c.header)
			signV4(req, exampleAccessKey, exampleSecretKey, "us-east-1", c.payload, exampleTime)
			auth := req.Header.Get("Authorization")
			if !strings.Contains(auth, "Credential="+exampleAccessKey+"/20130524/us-east-1/s3/aws4_request") {
				t.Errorf("credential scope: %s", auth)
			}
			if !strings.Contains(auth, "SignedHeaders="+c.wantSigned+",") {
				t.Errorf("signed headers: %s", auth)
			}
			if got := signatureOf(req); got != c.want {
				t.Errorf("signature %s, want %s", got, c.want)
			}
		})
	}
}

func TestURIEncode(t *testing.T) {
	cases := map[string]string{
		"session-files/2026/10/20/ab.zip": "session-files/2026/10/20/ab.zip",
		"a b+c$d~e":                       "a%20b%2Bc%24d~e",
		"ü":                               "%C3%BC",
	}
	for in, want := range cases {
		if got := uriEncode(in, false); got != want {
			t.Errorf("uriEncode(%q) = %q, want %q", in, got, want)
		}
	}
	if got := uriEncode("a/b", true); got != "a%2Fb" {
		t.Errorf("slash: %q", got)
	}
}
