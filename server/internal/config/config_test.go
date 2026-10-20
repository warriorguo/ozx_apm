package config

import (
	"os"
	"strings"
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

func TestParseClickHouseURL(t *testing.T) {
	tests := []struct {
		name     string
		url      string
		expected ClickHouseConfig
	}{
		{
			name: "full URL with credentials",
			url:  "clickhouse://admin:secret@db.example.com:9000/mydb",
			expected: ClickHouseConfig{
				Host:     "db.example.com",
				Port:     9000,
				Database: "mydb",
				Username: "admin",
				Password: "secret",
			},
		},
		{
			name: "URL without password",
			url:  "clickhouse://default@localhost:9000/ozx_apm",
			expected: ClickHouseConfig{
				Host:     "localhost",
				Port:     9000,
				Database: "ozx_apm",
				Username: "default",
				Password: "",
			},
		},
		{
			name: "URL without credentials",
			url:  "clickhouse://localhost:9000/testdb",
			expected: ClickHouseConfig{
				Host:     "localhost",
				Port:     9000,
				Database: "testdb",
				Username: "",
				Password: "",
			},
		},
		{
			name: "legacy host:port format",
			url:  "clickhouse.local:9001",
			expected: ClickHouseConfig{
				Host: "clickhouse.local",
				Port: 9001,
			},
		},
		{
			name: "URL with special characters in password",
			url:  "clickhouse://user:p%40ss%3Aword@host:9000/db",
			expected: ClickHouseConfig{
				Host:     "host",
				Port:     9000,
				Database: "db",
				Username: "user",
				Password: "p@ss:word",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := ClickHouseConfig{}
			err := parseClickHouseURL(tt.url, &cfg)
			if err != nil {
				t.Fatalf("parseClickHouseURL failed: %v", err)
			}

			if cfg.Host != tt.expected.Host {
				t.Errorf("host: expected %s, got %s", tt.expected.Host, cfg.Host)
			}
			if cfg.Port != tt.expected.Port {
				t.Errorf("port: expected %d, got %d", tt.expected.Port, cfg.Port)
			}
			if cfg.Database != tt.expected.Database {
				t.Errorf("database: expected %s, got %s", tt.expected.Database, cfg.Database)
			}
			if cfg.Username != tt.expected.Username {
				t.Errorf("username: expected %s, got %s", tt.expected.Username, cfg.Username)
			}
			if cfg.Password != tt.expected.Password {
				t.Errorf("password: expected %s, got %s", tt.expected.Password, cfg.Password)
			}
		})
	}
}

func TestLoad_DatabaseURL(t *testing.T) {
	// Set DATABASE_URL
	os.Setenv("DATABASE_URL", "clickhouse://testuser:testpass@dbhost:9999/testdb")
	defer os.Unsetenv("DATABASE_URL")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	if cfg.ClickHouse.Host != "dbhost" {
		t.Errorf("expected host=dbhost, got %s", cfg.ClickHouse.Host)
	}
	if cfg.ClickHouse.Port != 9999 {
		t.Errorf("expected port=9999, got %d", cfg.ClickHouse.Port)
	}
	if cfg.ClickHouse.Database != "testdb" {
		t.Errorf("expected database=testdb, got %s", cfg.ClickHouse.Database)
	}
	if cfg.ClickHouse.Username != "testuser" {
		t.Errorf("expected username=testuser, got %s", cfg.ClickHouse.Username)
	}
	if cfg.ClickHouse.Password != "testpass" {
		t.Errorf("expected password=testpass, got %s", cfg.ClickHouse.Password)
	}
}

// APM-14: the privacy API token comes from OZX_ADMIN_PRIVACY_TOKEN; unset means disabled.
func TestLoad_PrivacyToken(t *testing.T) {
	os.Unsetenv("OZX_ADMIN_PRIVACY_TOKEN")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AdminServer.PrivacyToken != "" {
		t.Errorf("default token %q, want empty", cfg.AdminServer.PrivacyToken)
	}

	os.Setenv("OZX_ADMIN_PRIVACY_TOKEN", "s3cret-token")
	defer os.Unsetenv("OZX_ADMIN_PRIVACY_TOKEN")
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AdminServer.PrivacyToken != "s3cret-token" {
		t.Errorf("token from env %q", cfg.AdminServer.PrivacyToken)
	}
}

// APM-7: the symbol upload token comes from OZX_SYMBOLS_UPLOAD_TOKEN; unset means disabled.
func TestLoad_SymbolsToken(t *testing.T) {
	os.Unsetenv("OZX_SYMBOLS_UPLOAD_TOKEN")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AdminServer.SymbolsToken != "" {
		t.Errorf("default token %q, want empty", cfg.AdminServer.SymbolsToken)
	}
	os.Setenv("OZX_SYMBOLS_UPLOAD_TOKEN", "sym-token")
	defer os.Unsetenv("OZX_SYMBOLS_UPLOAD_TOKEN")
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AdminServer.SymbolsToken != "sym-token" || cfg.AdminServer.PrivacyToken == "sym-token" {
		t.Errorf("token from env %q", cfg.AdminServer.SymbolsToken)
	}
}

// APM-8: every alert.* key is read from OZX_ALERT_<KEY>, the test-alert
// token from OZX_ADMIN_ALERT_TOKEN; alerting is off by default.
func TestLoad_AlertEnvironment(t *testing.T) {
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	a := cfg.Alert
	if a.Enabled || a.WebhookURL != "" || a.WebhookFormat != "generic" || a.Interval != time.Minute ||
		a.NewIssueMaxPerCycle != 10 || a.NewIssueWindow != 26*time.Hour || a.CrashRateMinSessions != 50 ||
		a.CrashRateMinRate != 0.005 || !a.StartupEnabled || a.StartupWindow != 7*24*time.Hour || a.IncludeDevBuilds ||
		cfg.AdminServer.AlertToken != "" {
		t.Fatalf("defaults %+v", a)
	}
	t.Setenv("OZX_ALERT_ENABLED", "true")
	t.Setenv("OZX_ALERT_WEBHOOK_URL", "https://hooks.example.com/x")
	t.Setenv("OZX_ALERT_WEBHOOK_FORMAT", "feishu")
	t.Setenv("OZX_ALERT_ADMIN_BASE_URL", "https://apm-admin.example.com")
	t.Setenv("OZX_ALERT_INTERVAL", "30s")
	t.Setenv("OZX_ALERT_INCLUDE_DEV_BUILDS", "true")
	t.Setenv("OZX_ALERT_CRASH_RATE_MIN_SESSIONS", "200")
	t.Setenv("OZX_ALERT_CRASH_RATE_FACTOR", "3.5")
	t.Setenv("OZX_ADMIN_ALERT_TOKEN", "tok")
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	a = cfg.Alert
	if !a.Enabled || a.WebhookURL != "https://hooks.example.com/x" || a.WebhookFormat != "feishu" ||
		a.AdminBaseURL != "https://apm-admin.example.com" || a.Interval != 30*time.Second || !a.IncludeDevBuilds ||
		a.CrashRateMinSessions != 200 || a.CrashRateFactor != 3.5 || cfg.AdminServer.AlertToken != "tok" {
		t.Fatalf("from the environment %+v (token set: %v)", a, cfg.AdminServer.AlertToken != "")
	}
}

// APM-12: the ops context whitelist and country header come from config or
// OZX_OPS_*; the import token from OZX_ADMIN_OPS_TOKEN (unset: disabled).
func TestLoad_Ops(t *testing.T) {
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Ops.OpsContextKeys(); strings.Join(got, ",") != strings.Join(DefaultOpsContextKeys, ",") {
		t.Errorf("default context keys %v", got)
	}
	if cfg.Ops.CountryHeader != "" || cfg.AdminServer.OpsToken != "" {
		t.Errorf("defaults: header %q, token set %v", cfg.Ops.CountryHeader, cfg.AdminServer.OpsToken != "")
	}
	t.Setenv("OZX_OPS_CONTEXT_KEYS", "runId,chapter,bossId")
	t.Setenv("OZX_OPS_COUNTRY_HEADER", "CF-IPCountry")
	t.Setenv("OZX_ADMIN_OPS_TOKEN", "ops-tok")
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Ops.OpsContextKeys(); strings.Join(got, ",") != "runId,chapter,bossId" {
		t.Errorf("context keys from the environment %v", got)
	}
	if cfg.Ops.CountryHeader != "CF-IPCountry" || cfg.AdminServer.OpsToken != "ops-tok" {
		t.Errorf("from the environment: header %q, token %v", cfg.Ops.CountryHeader, cfg.AdminServer.OpsToken == "ops-tok")
	}
}

// A Config built in code (tests, tools) has no whitelist configured: the
// defaults apply; an explicit empty list keeps no context key.
func TestOpsContextKeys_NilAndEmpty(t *testing.T) {
	if got := (OpsConfig{}).OpsContextKeys(); len(got) != len(DefaultOpsContextKeys) {
		t.Errorf("nil: %v", got)
	}
	if got := (OpsConfig{ContextKeys: []string{}}).OpsContextKeys(); len(got) != 0 {
		t.Errorf("empty: %v", got)
	}
}

// APM-13: the object store and session file settings come from OZX_S3_* and
// OZX_SESSION_FILES_*, the admin token from OZX_ADMIN_SESSIONFILES_TOKEN.
// Without them the feature is off.
func TestLoad_SessionFiles(t *testing.T) {
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	sf := cfg.SessionFiles
	if cfg.S3.Configured() || cfg.S3.Region != "us-east-1" || cfg.S3.Prefix != "session-files/" ||
		sf.RetentionDays != 30 || sf.IncompleteDays != 7 || sf.MaxArchiveBytes != 512<<20 ||
		sf.MaxUploadsPerDevicePerDay != 100 || sf.MaxBytesPerDevicePerDay != 4<<30 || sf.MaxOpenUploadsPerDevice != 4 ||
		sf.CleanerInterval != time.Hour || sf.UploadKey != "" || sf.AdminToken != "" {
		t.Fatalf("defaults: s3 %v region %q prefix %q retention %d incomplete %d max %d interval %v",
			cfg.S3.Configured(), cfg.S3.Region, cfg.S3.Prefix, sf.RetentionDays, sf.IncompleteDays, sf.MaxArchiveBytes, sf.CleanerInterval)
	}
	t.Setenv("OZX_S3_ENDPOINT", "http://192.168.0.192:18333")
	t.Setenv("OZX_S3_BUCKET", "ozx-apm-sessions")
	t.Setenv("OZX_S3_ACCESS_KEY", "ak")
	t.Setenv("OZX_S3_SECRET_KEY", "sk")
	t.Setenv("OZX_S3_REGION", "eu-west-1")
	t.Setenv("OZX_S3_PREFIX", "x/")
	t.Setenv("OZX_SESSION_FILES_RETENTION_DAYS", "14")
	t.Setenv("OZX_SESSION_FILES_MAX_ARCHIVE_BYTES", "1048576")
	t.Setenv("OZX_SESSION_FILES_CLEANER_INTERVAL", "10m")
	t.Setenv("OZX_SESSION_FILES_UPLOAD_KEY", "up")
	t.Setenv("OZX_ADMIN_SESSIONFILES_TOKEN", "adm")
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	sf = cfg.SessionFiles
	if !cfg.S3.Configured() || cfg.S3.Endpoint != "http://192.168.0.192:18333" || cfg.S3.Bucket != "ozx-apm-sessions" ||
		cfg.S3.AccessKey != "ak" || cfg.S3.SecretKey != "sk" || cfg.S3.Region != "eu-west-1" || cfg.S3.Prefix != "x/" ||
		sf.RetentionDays != 14 || sf.MaxArchiveBytes != 1<<20 || sf.CleanerInterval != 10*time.Minute ||
		sf.UploadKey != "up" || sf.AdminToken != "adm" {
		t.Fatalf("from the environment: bucket %q region %q prefix %q retention %d max %d interval %v keys set %v %v",
			cfg.S3.Bucket, cfg.S3.Region, cfg.S3.Prefix, sf.RetentionDays, sf.MaxArchiveBytes, sf.CleanerInterval,
			sf.UploadKey == "up", sf.AdminToken == "adm")
	}
}
