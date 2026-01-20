package testutil

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"

	"github.com/warriorguo/ozx_apm/server/internal/config"
	"github.com/warriorguo/ozx_apm/server/internal/storage"
)

// TestClickHouseConfig returns a config for testing
func TestClickHouseConfig() *config.ClickHouseConfig {
	return &config.ClickHouseConfig{
		Host:     "localhost",
		Port:     9000,
		Database: "ozx_apm_test",
		Username: "default",
		Password: "",
	}
}

// SetupTestDatabase creates a test database and runs migrations
func SetupTestDatabase(cfg *config.ClickHouseConfig) (*storage.ClickHouseClient, error) {
	logger := zap.NewNop()

	// First connect to default database to create test database
	defaultCfg := *cfg
	defaultCfg.Database = "default"

	client, err := storage.NewClickHouseClient(&defaultCfg, logger)
	if err != nil {
		return nil, fmt.Errorf("connect to default: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Create test database
	err = client.Exec(ctx, fmt.Sprintf("CREATE DATABASE IF NOT EXISTS %s", cfg.Database))
	if err != nil {
		client.Close()
		return nil, fmt.Errorf("create database: %w", err)
	}
	client.Close()

	// Connect to test database
	testClient, err := storage.NewClickHouseClient(cfg, logger)
	if err != nil {
		return nil, fmt.Errorf("connect to test db: %w", err)
	}

	// Run migrations
	if err := testClient.Migrate(ctx); err != nil {
		testClient.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}

	return testClient, nil
}

// CleanupTestDatabase removes test data
func CleanupTestDatabase(client *storage.ClickHouseClient) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tables := []string{
		"apm_perf_samples",
		"apm_janks",
		"apm_startups",
		"apm_scene_loads",
		"apm_exceptions",
		"apm_crashes",
	}

	for _, table := range tables {
		if err := client.Exec(ctx, fmt.Sprintf("TRUNCATE TABLE IF EXISTS %s", table)); err != nil {
			return fmt.Errorf("truncate %s: %w", table, err)
		}
	}

	return nil
}
