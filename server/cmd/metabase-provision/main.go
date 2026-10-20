// Command metabase-provision creates the operations data dashboards of OZX
// (APM-12) in a Metabase (0.47 or later, with the ClickHouse driver
// installed): the ClickHouse connection (a read-only account,
// deployments/clickhouse/users.d/ozx_ops_readonly.xml), a collection, every
// preset question (internal/opsbi) and the Players, Funnels and Ads
// dashboards. Run it again after a server upgrade: it updates what changed
// and leaves the rest (idempotent). Settings come from the environment only,
// so no secret is ever on a command line; none is printed.
//
//	MB_URL            Metabase, e.g. https://metabase.example.com
//	MB_USER           an admin's e-mail          } or MB_API_KEY (Metabase 0.49+,
//	MB_PASSWORD       its password               }   an admin group's key)
//	CH_HOST           ClickHouse host as Metabase reaches it
//	CH_PORT           its HTTP port (default 8123)
//	CH_DATABASE       default ozx_apm
//	CH_USER           default ozx_ops_ro
//	CH_PASSWORD       the read-only account's password
//	CH_SSL            true for HTTPS (default false)
//	MB_DATABASE_NAME  the connection's name in Metabase (default "OZX Ops (ClickHouse)")
//	MB_COLLECTION     the collection (default "OZX Ops")
//	MB_UPDATE_CONNECTION=1  also push CH_* to an existing connection (password rotation)
//
//	go run ./cmd/metabase-provision            # from server/
package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/warriorguo/ozx_apm/server/internal/opsbi"
)

func main() {
	cfg, err := configFromEnv(os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "metabase-provision:", err)
		os.Exit(2)
	}
	p := opsbi.NewProvisioner(cfg, nil, func(s string) { fmt.Println(s) })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if err := p.Provision(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "metabase-provision:", err)
		os.Exit(1)
	}
	fmt.Println("done")
}

func configFromEnv(get func(string) string) (opsbi.MetabaseConfig, error) {
	cfg := opsbi.MetabaseConfig{
		URL:              strings.TrimSpace(get("MB_URL")),
		User:             get("MB_USER"),
		Password:         get("MB_PASSWORD"),
		APIKey:           get("MB_API_KEY"),
		DatabaseName:     get("MB_DATABASE_NAME"),
		CollectionName:   get("MB_COLLECTION"),
		CHHost:           strings.TrimSpace(get("CH_HOST")),
		CHDatabase:       or(get("CH_DATABASE"), "ozx_apm"),
		CHUser:           or(get("CH_USER"), "ozx_ops_ro"),
		CHPassword:       get("CH_PASSWORD"),
		UpdateConnection: truthy(get("MB_UPDATE_CONNECTION")),
		CHSSL:            truthy(get("CH_SSL")),
	}
	if cfg.URL == "" {
		return cfg, fmt.Errorf("MB_URL is required")
	}
	if p := get("CH_PORT"); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n <= 0 || n > 65535 {
			return cfg, fmt.Errorf("CH_PORT %q is not a port", p)
		}
		cfg.CHPort = n
	}
	return cfg, nil
}

func or(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return strings.TrimSpace(v)
}

func truthy(v string) bool {
	v = strings.ToLower(strings.TrimSpace(v))
	return v == "1" || v == "true" || v == "yes"
}
