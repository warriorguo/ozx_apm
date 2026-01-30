package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"go.uber.org/zap"

	"github.com/warriorguo/ozx_apm/server/internal/config"
)

type ClickHouseClient struct {
	conn   driver.Conn
	logger *zap.Logger
}

func NewClickHouseClient(cfg *config.ClickHouseConfig, logger *zap.Logger) (*ClickHouseClient, error) {
	conn, err := clickhouse.Open(&clickhouse.Options{
		Addr: []string{fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)},
		Auth: clickhouse.Auth{
			Database: cfg.Database,
			Username: cfg.Username,
			Password: cfg.Password,
		},
		Settings: clickhouse.Settings{
			"max_execution_time": 60,
		},
		DialTimeout:     10 * time.Second,
		MaxOpenConns:    10,
		MaxIdleConns:    5,
		ConnMaxLifetime: time.Hour,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to connect to clickhouse: %w", err)
	}

	// Test connection with retry
	maxRetries := 5
	retryDelay := 3 * time.Second
	var lastErr error
	for i := 0; i < maxRetries; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := conn.Ping(ctx)
		cancel()
		if err == nil {
			logger.Info("connected to ClickHouse",
				zap.String("host", cfg.Host),
				zap.Int("port", cfg.Port),
				zap.String("database", cfg.Database),
			)
			return &ClickHouseClient{
				conn:   conn,
				logger: logger,
			}, nil
		}
		lastErr = err
		logger.Warn("failed to ping ClickHouse, retrying...",
			zap.Int("attempt", i+1),
			zap.Int("max_retries", maxRetries),
			zap.Error(err),
		)
		if i < maxRetries-1 {
			time.Sleep(retryDelay)
		}
	}

	return nil, fmt.Errorf("failed to ping clickhouse after %d retries: %w", maxRetries, lastErr)
}

func (c *ClickHouseClient) Conn() driver.Conn {
	return c.conn
}

func (c *ClickHouseClient) Ping(ctx context.Context) error {
	return c.conn.Ping(ctx)
}

func (c *ClickHouseClient) Close() error {
	return c.conn.Close()
}

func (c *ClickHouseClient) Exec(ctx context.Context, query string, args ...interface{}) error {
	return c.conn.Exec(ctx, query, args...)
}
