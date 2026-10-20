package main

import (
	"strings"
	"testing"
)

func TestConfigFromEnv(t *testing.T) {
	env := map[string]string{"MB_URL": " https://mb.example.com ", "MB_USER": "a@b.c", "MB_PASSWORD": "p", "CH_HOST": "ch", "CH_PASSWORD": "x", "CH_SSL": "true"}
	cfg, err := configFromEnv(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.URL != "https://mb.example.com" || cfg.CHDatabase != "ozx_apm" || cfg.CHUser != "ozx_ops_ro" || !cfg.CHSSL || cfg.UpdateConnection || cfg.CHPort != 0 {
		t.Errorf("%+v", cfg)
	}
	env["CH_PORT"] = "8443"
	env["MB_UPDATE_CONNECTION"] = "1"
	if cfg, err = configFromEnv(func(k string) string { return env[k] }); err != nil || cfg.CHPort != 8443 || !cfg.UpdateConnection {
		t.Errorf("%+v %v", cfg, err)
	}
	env["CH_PORT"] = "http"
	if _, err = configFromEnv(func(k string) string { return env[k] }); err == nil || !strings.Contains(err.Error(), "CH_PORT") {
		t.Errorf("bad port: %v", err)
	}
	if _, err = configFromEnv(func(string) string { return "" }); err == nil {
		t.Error("no MB_URL accepted")
	}
}
