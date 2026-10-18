package config

import (
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/viper"
)

type Config struct {
	Server      ServerConfig      `mapstructure:"server"`
	AdminServer AdminServerConfig `mapstructure:"admin_server"`
	ClickHouse  ClickHouseConfig  `mapstructure:"clickhouse"`
	Auth        AuthConfig        `mapstructure:"auth"`
	RateLimit   RateLimitConfig   `mapstructure:"ratelimit"`
	Alert       AlertConfig       `mapstructure:"alert"`
}

type ServerConfig struct {
	Enabled      bool          `mapstructure:"enabled"`
	Host         string        `mapstructure:"host"`
	Port         int           `mapstructure:"port"`
	ReadTimeout  time.Duration `mapstructure:"read_timeout"`
	WriteTimeout time.Duration `mapstructure:"write_timeout"`
}

type AdminServerConfig struct {
	Enabled        bool          `mapstructure:"enabled"`
	Host           string        `mapstructure:"host"`
	Port           int           `mapstructure:"port"`
	ReadTimeout    time.Duration `mapstructure:"read_timeout"`
	WriteTimeout   time.Duration `mapstructure:"write_timeout"`
	AllowedOrigins []string      `mapstructure:"allowed_origins"`
	// PrivacyToken enables the privacy deletion endpoints (APM-14): they
	// require "Authorization: Bearer <token>" and are disabled while it is
	// empty. Environment: OZX_ADMIN_PRIVACY_TOKEN. Never logged.
	PrivacyToken string `mapstructure:"privacy_token"`
	// AlertToken enables POST /api/alerts/test (APM-8), which makes the
	// alert evaluator post a test message to the webhook: "Authorization:
	// Bearer <token>"; disabled while empty. Environment:
	// OZX_ADMIN_ALERT_TOKEN. Never logged.
	AlertToken string `mapstructure:"alert_token"`
	// SymbolsToken enables the symbol upload endpoints (APM-7, cmd/apmsym):
	// they require "Authorization: Bearer <token>" and are disabled while it
	// is empty. Environment: OZX_SYMBOLS_UPLOAD_TOKEN. Never logged.
	SymbolsToken string `mapstructure:"symbols_token"`
}

type ClickHouseConfig struct {
	Host     string `mapstructure:"host"`
	Port     int    `mapstructure:"port"`
	Database string `mapstructure:"database"`
	Username string `mapstructure:"username"`
	Password string `mapstructure:"password"`
	// AsyncInsert sends ingest inserts with async_insert=1 and
	// wait_for_async_insert=1 so concurrent requests share parts.
	// Off by default; see storage/insert.go for measurements.
	AsyncInsert bool `mapstructure:"async_insert"`
}

type AuthConfig struct {
	Enabled bool              `mapstructure:"enabled"`
	AppKeys map[string]string `mapstructure:"app_keys"` // app_key -> app_name
}

type RateLimitConfig struct {
	Enabled        bool `mapstructure:"enabled"`
	RequestsPerMin int  `mapstructure:"requests_per_min"`
}

// AlertConfig configures the alert evaluator (APM-8). Every key can be set
// by an environment variable OZX_ALERT_<KEY> (e.g. OZX_ALERT_ENABLED,
// OZX_ALERT_WEBHOOK_URL). Off by default; it must run in one process only
// (RELEASE.md, APM-8), although two running at once send each alert once.
type AlertConfig struct {
	Enabled bool `mapstructure:"enabled"`
	// WebhookURL receives the alerts (POST, JSON). A secret: never logged.
	WebhookURL string `mapstructure:"webhook_url"`
	// WebhookFormat: generic (the documented JSON with a "text" field),
	// slack ({"text"}) or feishu ({"msg_type":"text","content":{"text"}}).
	WebhookFormat string `mapstructure:"webhook_format"`
	// AdminBaseURL is the admin UI's address, for links in alerts
	// (e.g. https://apm-admin.local.playquota.com). Empty: no links.
	AdminBaseURL string `mapstructure:"admin_base_url"`
	// Interval between evaluations.
	Interval time.Duration `mapstructure:"interval"`
	// IncludeDevBuilds: also alert on dev and editor builds (off: release
	// builds and events without a build type).
	IncludeDevBuilds bool `mapstructure:"include_dev_builds"`

	// New issues: at most NewIssueMaxPerCycle individual alerts per
	// evaluation and NewIssueMaxPerHour per hour, the rest in one digest per
	// 15 minutes. NewIssueWindow is how far back each
	// evaluation looks for rows (by event time); every SweepInterval the
	// whole retention is searched instead (late uploads).
	NewIssueMaxPerCycle int           `mapstructure:"new_issue_max_per_cycle"`
	NewIssueMaxPerHour  int           `mapstructure:"new_issue_max_per_hour"`
	NewIssueWindow      time.Duration `mapstructure:"new_issue_window"`
	SweepInterval       time.Duration `mapstructure:"sweep_interval"`

	// Crash rate spike: crashed sessions / sessions of a version over
	// CrashRateWindow, checked every CrashRateCheckInterval, against all
	// versions over the CrashRateBaselineWindow before it.
	CrashRateWindow         time.Duration `mapstructure:"crash_rate_window"`
	CrashRateBaselineWindow time.Duration `mapstructure:"crash_rate_baseline_window"`
	CrashRateCheckInterval  time.Duration `mapstructure:"crash_rate_check_interval"`
	CrashRateMinSessions    int64         `mapstructure:"crash_rate_min_sessions"`
	CrashRateMinCrashed     int64         `mapstructure:"crash_rate_min_crashed_sessions"`
	CrashRateMinRate        float64       `mapstructure:"crash_rate_min_rate"`
	CrashRateFactor         float64       `mapstructure:"crash_rate_factor"`
	CrashRateCooldown       time.Duration `mapstructure:"crash_rate_cooldown"`

	// Startup P95 regression: the newest version against the previous one,
	// over StartupWindow, checked every StartupCheckInterval.
	StartupEnabled       bool          `mapstructure:"startup_enabled"`
	StartupWindow        time.Duration `mapstructure:"startup_window"`
	StartupCheckInterval time.Duration `mapstructure:"startup_check_interval"`
	StartupMinSamples    int64         `mapstructure:"startup_min_samples"`
	StartupIncrease      float64       `mapstructure:"startup_increase"`
	StartupMinDeltaMs    float64       `mapstructure:"startup_min_delta_ms"`
}

// alertDefaults are the alert.* defaults; each key is also bound to the
// environment variable OZX_ALERT_<KEY>.
var alertDefaults = map[string]interface{}{
	"enabled":                         false,
	"webhook_url":                     "",
	"webhook_format":                  "generic",
	"admin_base_url":                  "",
	"interval":                        "60s",
	"include_dev_builds":              false,
	"new_issue_max_per_cycle":         10,
	"new_issue_max_per_hour":          30,
	"new_issue_window":                "26h",
	"sweep_interval":                  "6h",
	"crash_rate_window":               "1h",
	"crash_rate_baseline_window":      "24h",
	"crash_rate_check_interval":       "5m",
	"crash_rate_min_sessions":         50,
	"crash_rate_min_crashed_sessions": 5,
	"crash_rate_min_rate":             0.005,
	"crash_rate_factor":               2.0,
	"crash_rate_cooldown":             "6h",
	"startup_enabled":                 true,
	"startup_window":                  "168h",
	"startup_check_interval":          "1h",
	"startup_min_samples":             100,
	"startup_increase":                0.2,
	"startup_min_delta_ms":            100,
}

func Load() (*Config, error) {
	viper.SetConfigName("config")
	viper.SetConfigType("yaml")
	viper.AddConfigPath(".")
	viper.AddConfigPath("./config")
	viper.AddConfigPath("/etc/ozx-apm/")

	// Set defaults
	viper.SetDefault("server.enabled", true)
	viper.SetDefault("server.host", "0.0.0.0")
	viper.SetDefault("server.port", 8080)
	viper.SetDefault("server.read_timeout", "30s")
	viper.SetDefault("server.write_timeout", "30s")

	viper.SetDefault("admin_server.enabled", true)
	viper.SetDefault("admin_server.host", "0.0.0.0")
	viper.SetDefault("admin_server.port", 8081)
	viper.SetDefault("admin_server.read_timeout", "30s")
	viper.SetDefault("admin_server.write_timeout", "30s")
	viper.SetDefault("admin_server.allowed_origins", []string{"*"})
	viper.SetDefault("admin_server.privacy_token", "")
	_ = viper.BindEnv("admin_server.privacy_token", "OZX_ADMIN_PRIVACY_TOKEN")
	viper.SetDefault("admin_server.alert_token", "")
	_ = viper.BindEnv("admin_server.alert_token", "OZX_ADMIN_ALERT_TOKEN")
	viper.SetDefault("admin_server.symbols_token", "")
	_ = viper.BindEnv("admin_server.symbols_token", "OZX_SYMBOLS_UPLOAD_TOKEN")

	viper.SetDefault("clickhouse.host", "localhost")
	viper.SetDefault("clickhouse.port", 9000)
	viper.SetDefault("clickhouse.database", "ozx_apm")
	viper.SetDefault("clickhouse.username", "default")
	viper.SetDefault("clickhouse.password", "")
	viper.SetDefault("clickhouse.async_insert", false)

	viper.SetDefault("auth.enabled", false)
	viper.SetDefault("ratelimit.enabled", true)
	viper.SetDefault("ratelimit.requests_per_min", 1000)
	for key, value := range alertDefaults {
		viper.SetDefault("alert."+key, value)
		_ = viper.BindEnv("alert."+key, "OZX_ALERT_"+strings.ToUpper(key))
	}

	// Read environment variables
	viper.AutomaticEnv()
	viper.SetEnvPrefix("OZX")

	// Try to read config file (not required)
	_ = viper.ReadInConfig()

	var cfg Config
	if err := viper.Unmarshal(&cfg); err != nil {
		return nil, err
	}

	// Override ClickHouse config if DATABASE_URL is set
	if dbURL := os.Getenv("DATABASE_URL"); dbURL != "" {
		if err := parseClickHouseURL(dbURL, &cfg.ClickHouse); err != nil {
			return nil, err
		}
	}

	return &cfg, nil
}

// parseClickHouseURL parses a ClickHouse connection URL and populates the config.
// Supported formats:
//   - clickhouse://user:password@host:port/database
//   - clickhouse://host:port/database
//   - host:port (legacy format)
func parseClickHouseURL(rawURL string, cfg *ClickHouseConfig) error {
	// Handle legacy format: host:port
	if !strings.Contains(rawURL, "://") {
		parts := strings.Split(rawURL, ":")
		if len(parts) == 2 {
			cfg.Host = parts[0]
			if port, err := strconv.Atoi(parts[1]); err == nil {
				cfg.Port = port
			}
		}
		return nil
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		return err
	}

	// Host and port
	cfg.Host = u.Hostname()
	if portStr := u.Port(); portStr != "" {
		if port, err := strconv.Atoi(portStr); err == nil {
			cfg.Port = port
		}
	}

	// Username and password
	if u.User != nil {
		cfg.Username = u.User.Username()
		if password, ok := u.User.Password(); ok {
			cfg.Password = password
		}
	}

	// Database from path
	if u.Path != "" {
		cfg.Database = strings.TrimPrefix(u.Path, "/")
	}

	return nil
}
