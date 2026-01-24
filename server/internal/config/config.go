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
}

type ClickHouseConfig struct {
	Host     string `mapstructure:"host"`
	Port     int    `mapstructure:"port"`
	Database string `mapstructure:"database"`
	Username string `mapstructure:"username"`
	Password string `mapstructure:"password"`
}

type AuthConfig struct {
	Enabled bool              `mapstructure:"enabled"`
	AppKeys map[string]string `mapstructure:"app_keys"` // app_key -> app_name
}

type RateLimitConfig struct {
	Enabled        bool `mapstructure:"enabled"`
	RequestsPerMin int  `mapstructure:"requests_per_min"`
}

type AlertConfig struct {
	Enabled    bool   `mapstructure:"enabled"`
	WebhookURL string `mapstructure:"webhook_url"`
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

	viper.SetDefault("clickhouse.host", "localhost")
	viper.SetDefault("clickhouse.port", 9000)
	viper.SetDefault("clickhouse.database", "ozx_apm")
	viper.SetDefault("clickhouse.username", "default")
	viper.SetDefault("clickhouse.password", "")

	viper.SetDefault("auth.enabled", false)
	viper.SetDefault("ratelimit.enabled", true)
	viper.SetDefault("ratelimit.requests_per_min", 1000)
	viper.SetDefault("alert.enabled", false)

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
