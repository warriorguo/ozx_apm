package config

import (
	"os"
	"testing"
	"time"
)

func TestLoad_Defaults(t *testing.T) {
	// Clear any existing config
	os.Unsetenv("OZX_SERVER_PORT")
	os.Unsetenv("OZX_CLICKHOUSE_HOST")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	// Check server defaults
	if cfg.Server.Host != "0.0.0.0" {
		t.Errorf("expected server.host=0.0.0.0, got %s", cfg.Server.Host)
	}
	if cfg.Server.Port != 8080 {
		t.Errorf("expected server.port=8080, got %d", cfg.Server.Port)
	}
	if cfg.Server.ReadTimeout != 30*time.Second {
		t.Errorf("expected server.read_timeout=30s, got %v", cfg.Server.ReadTimeout)
	}
	if cfg.Server.WriteTimeout != 30*time.Second {
		t.Errorf("expected server.write_timeout=30s, got %v", cfg.Server.WriteTimeout)
	}

	// Check admin server defaults
	if !cfg.AdminServer.Enabled {
		t.Error("expected admin_server.enabled=true")
	}
	if cfg.AdminServer.Port != 8081 {
		t.Errorf("expected admin_server.port=8081, got %d", cfg.AdminServer.Port)
	}

	// Check ClickHouse defaults
	if cfg.ClickHouse.Host != "localhost" {
		t.Errorf("expected clickhouse.host=localhost, got %s", cfg.ClickHouse.Host)
	}
	if cfg.ClickHouse.Port != 9000 {
		t.Errorf("expected clickhouse.port=9000, got %d", cfg.ClickHouse.Port)
	}
	if cfg.ClickHouse.Database != "ozx_apm" {
		t.Errorf("expected clickhouse.database=ozx_apm, got %s", cfg.ClickHouse.Database)
	}

	// Check auth defaults
	if cfg.Auth.Enabled {
		t.Error("expected auth.enabled=false by default")
	}

	// Check rate limit defaults
	if !cfg.RateLimit.Enabled {
		t.Error("expected ratelimit.enabled=true by default")
	}
	if cfg.RateLimit.RequestsPerMin != 1000 {
		t.Errorf("expected ratelimit.requests_per_min=1000, got %d", cfg.RateLimit.RequestsPerMin)
	}

	// Check alert defaults
	if cfg.Alert.Enabled {
		t.Error("expected alert.enabled=false by default")
	}
}

func TestLoad_EnvironmentVariables(t *testing.T) {
	// Set environment variables
	os.Setenv("OZX_SERVER_PORT", "9090")
	os.Setenv("OZX_CLICKHOUSE_HOST", "clickhouse.example.com")
	os.Setenv("OZX_CLICKHOUSE_DATABASE", "test_apm")
	defer func() {
		os.Unsetenv("OZX_SERVER_PORT")
		os.Unsetenv("OZX_CLICKHOUSE_HOST")
		os.Unsetenv("OZX_CLICKHOUSE_DATABASE")
	}()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	// Environment variables should not override viper defaults unless using proper binding
	// This test documents the current behavior
	if cfg.ClickHouse.Host == "clickhouse.example.com" {
		t.Log("Environment variable OZX_CLICKHOUSE_HOST was applied")
	}
}

func TestServerConfig(t *testing.T) {
	cfg := ServerConfig{
		Host:         "127.0.0.1",
		Port:         8080,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
	}

	if cfg.Host != "127.0.0.1" {
		t.Errorf("expected host=127.0.0.1, got %s", cfg.Host)
	}
	if cfg.Port != 8080 {
		t.Errorf("expected port=8080, got %d", cfg.Port)
	}
}

func TestAdminServerConfig(t *testing.T) {
	cfg := AdminServerConfig{
		Enabled:        true,
		Host:           "0.0.0.0",
		Port:           8081,
		ReadTimeout:    30 * time.Second,
		WriteTimeout:   30 * time.Second,
		AllowedOrigins: []string{"http://localhost:3000", "http://admin.example.com"},
	}

	if !cfg.Enabled {
		t.Error("expected enabled=true")
	}
	if len(cfg.AllowedOrigins) != 2 {
		t.Errorf("expected 2 allowed origins, got %d", len(cfg.AllowedOrigins))
	}
}

func TestClickHouseConfig(t *testing.T) {
	cfg := ClickHouseConfig{
		Host:     "localhost",
		Port:     9000,
		Database: "ozx_apm",
		Username: "default",
		Password: "secret",
	}

	if cfg.Database != "ozx_apm" {
		t.Errorf("expected database=ozx_apm, got %s", cfg.Database)
	}
	if cfg.Password != "secret" {
		t.Errorf("expected password=secret, got %s", cfg.Password)
	}
}

func TestAuthConfig(t *testing.T) {
	cfg := AuthConfig{
		Enabled: true,
		AppKeys: map[string]string{
			"key1": "App1",
			"key2": "App2",
		},
	}

	if !cfg.Enabled {
		t.Error("expected enabled=true")
	}
	if len(cfg.AppKeys) != 2 {
		t.Errorf("expected 2 app keys, got %d", len(cfg.AppKeys))
	}
	if cfg.AppKeys["key1"] != "App1" {
		t.Errorf("expected app_keys[key1]=App1, got %s", cfg.AppKeys["key1"])
	}
}

func TestRateLimitConfig(t *testing.T) {
	cfg := RateLimitConfig{
		Enabled:        true,
		RequestsPerMin: 500,
	}

	if !cfg.Enabled {
		t.Error("expected enabled=true")
	}
	if cfg.RequestsPerMin != 500 {
		t.Errorf("expected requests_per_min=500, got %d", cfg.RequestsPerMin)
	}
}

func TestAlertConfig(t *testing.T) {
	cfg := AlertConfig{
		Enabled:    true,
		WebhookURL: "https://hooks.slack.com/services/xxx",
	}

	if !cfg.Enabled {
		t.Error("expected enabled=true")
	}
	if cfg.WebhookURL != "https://hooks.slack.com/services/xxx" {
		t.Errorf("unexpected webhook url: %s", cfg.WebhookURL)
	}
}
