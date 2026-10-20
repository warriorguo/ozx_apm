// Command opsimport sends operations reference data to the OZX APM admin
// API (APM-12): daily exchange rates and the ad networks' daily reports, used
// by the BI dashboards to convert ad revenue to US dollars and to reconcile
// the revenue the SDK tracked with what the networks report.
//
//	opsimport fx-rates  -url https://apm-admin.example.com [-dry-run] FILE.csv
//	opsimport ad-report -url ... -network admob [-currency USD] [-map 'revenue=Estimated earnings (USD),...'] [-dry-run] FILE.csv
//
// FILE "-" reads standard input. The bearer token of the import endpoints is
// read from OZX_ADMIN_OPS_TOKEN (never from a flag, never printed). The
// server validates the whole file before storing anything and answers with
// a summary (rows, date range, currencies or networks), which is printed.
// Re-importing a file replaces its rows. File formats: docs/ops-schema.md.
package main

import (
	"bytes"
	"compress/gzip"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// TokenEnv holds the import token.
const TokenEnv = "OZX_ADMIN_OPS_TOKEN"

type options struct {
	url      string
	network  string
	currency string
	mapping  string
	source   string
	dryRun   bool
	timeout  time.Duration
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd := os.Args[1]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	var o options
	fs.StringVar(&o.url, "url", os.Getenv("OZX_ADMIN_URL"), "admin base URL, e.g. https://apm-admin.example.com or http://host:8081 (default $OZX_ADMIN_URL)")
	fs.StringVar(&o.network, "network", "", "ad-report: the network (admob, applovin, unity, ...) when the file has no network column")
	fs.StringVar(&o.currency, "currency", "", "ad-report: the revenue currency when the file has no currency column (default USD)")
	fs.StringVar(&o.mapping, "map", "", "ad-report: header names of the file, e.g. 'date=Date,country=Country,revenue=Estimated earnings (USD)'")
	fs.StringVar(&o.source, "source", "", "name recorded with the rows (default: the file name)")
	fs.BoolVar(&o.dryRun, "dry-run", false, "validate the file on the server without storing it")
	fs.DurationVar(&o.timeout, "timeout", 2*time.Minute, "request timeout")
	_ = fs.Parse(os.Args[2:])
	if cmd != "fx-rates" && cmd != "ad-report" {
		usage()
		os.Exit(2)
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "opsimport: give exactly one file (or - for standard input)")
		os.Exit(2)
	}
	out, err := run(cmd, o, fs.Arg(0), os.Getenv(TokenEnv), http.DefaultClient)
	if err != nil {
		fmt.Fprintln(os.Stderr, "opsimport:", err)
		os.Exit(1)
	}
	fmt.Println(out)
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  opsimport fx-rates  -url URL [-dry-run] FILE.csv        (date,currency,usd_per_unit)
  opsimport ad-report -url URL -network NAME [-currency CUR] [-map 'col=Header,...'] [-dry-run] FILE.csv
The token is read from `+TokenEnv+`.`)
}

// run sends the file and returns the server's summary.
func run(cmd string, o options, file, token string, client *http.Client) (string, error) {
	if strings.TrimSpace(o.url) == "" {
		return "", errors.New("-url is required (or OZX_ADMIN_URL)")
	}
	if token == "" {
		return "", errors.New(TokenEnv + " is not set")
	}
	var data []byte
	var err error
	if file == "-" {
		data, err = io.ReadAll(os.Stdin)
	} else {
		data, err = os.ReadFile(file)
		if o.source == "" {
			o.source = filepath.Base(file)
		}
	}
	if err != nil {
		return "", err
	}
	q := url.Values{}
	if o.source != "" {
		q.Set("source", o.source)
	}
	if o.dryRun {
		q.Set("dry_run", "1")
	}
	if cmd == "ad-report" {
		if o.network != "" {
			q.Set("network", o.network)
		}
		if o.currency != "" {
			q.Set("currency", o.currency)
		}
		if o.mapping != "" {
			q.Set("map", o.mapping)
		}
	}
	var body bytes.Buffer
	zw := gzip.NewWriter(&body)
	if _, err := zw.Write(data); err != nil {
		return "", err
	}
	if err := zw.Close(); err != nil {
		return "", err
	}
	target := strings.TrimRight(o.url, "/") + "/api/ops/import/" + cmd + "?" + q.Encode()
	req, err := http.NewRequest(http.MethodPost, target, &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "text/csv")
	req.Header.Set("Content-Encoding", "gzip")
	c := *client
	c.Timeout = o.timeout
	resp, err := c.Do(req)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", redact(err, token))
	}
	defer resp.Body.Close()
	answer, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(answer)))
	}
	return strings.TrimSpace(string(answer)), nil
}

// redact keeps the token out of an error message.
func redact(err error, token string) error {
	if token == "" || !strings.Contains(err.Error(), token) {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), token, "<token>"))
}
