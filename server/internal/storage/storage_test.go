package storage

import (
	"context"
	"os"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/warriorguo/ozx_apm/server/internal/config"
	"github.com/warriorguo/ozx_apm/server/internal/models"
)

// getTestConfig returns a ClickHouseConfig from DATABASE_URL or skips the test
func getTestConfig(t *testing.T) *config.ClickHouseConfig {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		t.Skip("DATABASE_URL not set, skipping storage tests")
	}

	cfg := &config.ClickHouseConfig{
		Host:     "localhost",
		Port:     9000,
		Database: "default",
		Username: "default",
		Password: "",
	}

	// Parse DATABASE_URL (reuse config parsing logic)
	fullCfg, err := config.Load()
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}
	cfg = &fullCfg.ClickHouse

	return cfg
}

func getTestLogger() *zap.Logger {
	logger, _ := zap.NewDevelopment()
	return logger
}

func TestClickHouseClient_Connect(t *testing.T) {
	cfg := getTestConfig(t)
	logger := getTestLogger()

	client, err := NewClickHouseClient(cfg, logger)
	if err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	defer client.Close()

	t.Logf("Connected to ClickHouse at %s:%d/%s", cfg.Host, cfg.Port, cfg.Database)
}

func TestClickHouseClient_Ping(t *testing.T) {
	cfg := getTestConfig(t)
	logger := getTestLogger()

	client, err := NewClickHouseClient(cfg, logger)
	if err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	defer client.Close()

	ctx := context.Background()
	if err := client.Ping(ctx); err != nil {
		t.Errorf("ping failed: %v", err)
	}
}

func TestClickHouseClient_Migrate(t *testing.T) {
	cfg := getTestConfig(t)
	logger := getTestLogger()

	client, err := NewClickHouseClient(cfg, logger)
	if err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	defer client.Close()

	ctx := context.Background()
	if err := client.Migrate(ctx); err != nil {
		t.Errorf("migrate failed: %v", err)
	}

	// Verify tables exist
	tables := []string{
		"apm_perf_samples",
		"apm_janks",
		"apm_startups",
		"apm_scene_loads",
		"apm_exceptions",
		"apm_crashes",
	}

	for _, table := range tables {
		rows, err := client.conn.Query(ctx, "SELECT 1 FROM "+table+" LIMIT 1")
		if err != nil {
			t.Errorf("table %s not accessible: %v", table, err)
		} else {
			rows.Close()
			t.Logf("Table %s exists", table)
		}
	}
}

func TestRepository_InsertAndQueryPerfSamples(t *testing.T) {
	cfg := getTestConfig(t)
	logger := getTestLogger()

	client, err := NewClickHouseClient(cfg, logger)
	if err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	defer client.Close()

	ctx := context.Background()
	if err := client.Migrate(ctx); err != nil {
		t.Fatalf("migrate failed: %v", err)
	}

	repo := NewRepository(client, logger)

	// Insert test data
	now := time.Now()
	testVersion := "test-1.0.0"
	samples := []models.PerfSample{
		{
			Timestamp:    now,
			AppVersion:   testVersion,
			Platform:     "Android",
			DeviceModel:  "Test Device",
			OSVersion:    "12",
			SessionID:    "test-session-1",
			DeviceID:     "test-device-1",
			Scene:        "MainMenu",
			FPS:          60.0,
			FrameTimeMs:  16.6,
			MainThreadMs: 10.0,
			GCAllocKB:    100.0,
			MemMB:        256.0,
		},
		{
			Timestamp:    now,
			AppVersion:   testVersion,
			Platform:     "Android",
			DeviceModel:  "Test Device",
			OSVersion:    "12",
			SessionID:    "test-session-2",
			DeviceID:     "test-device-2",
			Scene:        "MainMenu",
			FPS:          55.0,
			FrameTimeMs:  18.2,
			MainThreadMs: 12.0,
			GCAllocKB:    150.0,
			MemMB:        280.0,
		},
	}

	if err := repo.InsertPerfSamples(ctx, samples); err != nil {
		t.Fatalf("insert perf samples failed: %v", err)
	}
	t.Log("Inserted perf samples")

	// Query data
	filter := models.QueryFilter{
		StartTime:  now.Add(-time.Hour),
		EndTime:    now.Add(time.Hour),
		AppVersion: testVersion,
	}

	results, err := repo.QueryFPSMetrics(ctx, filter)
	if err != nil {
		t.Fatalf("query FPS metrics failed: %v", err)
	}

	if len(results) == 0 {
		t.Log("No results returned (data may not be flushed yet)")
	} else {
		for _, r := range results {
			t.Logf("FPS Metrics: version=%s, platform=%s, scene=%s, count=%d, avg_fps=%.2f",
				r.AppVersion, r.Platform, r.Scene, r.Count, r.AvgFPS)
		}
	}
}

func TestRepository_InsertAndQueryJanks(t *testing.T) {
	cfg := getTestConfig(t)
	logger := getTestLogger()

	client, err := NewClickHouseClient(cfg, logger)
	if err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	defer client.Close()

	ctx := context.Background()
	if err := client.Migrate(ctx); err != nil {
		t.Fatalf("migrate failed: %v", err)
	}

	repo := NewRepository(client, logger)

	now := time.Now()
	testVersion := "test-1.0.0"
	janks := []models.Jank{
		{
			Timestamp:       now,
			AppVersion:      testVersion,
			Platform:        "iOS",
			DeviceModel:     "iPhone 13",
			OSVersion:       "16.0",
			SessionID:       "test-session-jank-1",
			DeviceID:        "test-device-jank-1",
			Scene:           "BattleScene",
			DurationMs:      120.5,
			MaxFrameMs:      85.0,
			RecentGCCount:   3,
			RecentGCAllocKB: 512.0,
			RecentEvents:    []string{"asset_load", "scene_switch"},
		},
	}

	if err := repo.InsertJanks(ctx, janks); err != nil {
		t.Fatalf("insert janks failed: %v", err)
	}
	t.Log("Inserted janks")

	filter := models.QueryFilter{
		StartTime:  now.Add(-time.Hour),
		EndTime:    now.Add(time.Hour),
		AppVersion: testVersion,
	}

	results, err := repo.QueryJankMetrics(ctx, filter)
	if err != nil {
		t.Fatalf("query jank metrics failed: %v", err)
	}

	t.Logf("Jank query returned %d results", len(results))
}

func TestRepository_InsertAndQueryStartups(t *testing.T) {
	cfg := getTestConfig(t)
	logger := getTestLogger()

	client, err := NewClickHouseClient(cfg, logger)
	if err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	defer client.Close()

	ctx := context.Background()
	if err := client.Migrate(ctx); err != nil {
		t.Fatalf("migrate failed: %v", err)
	}

	repo := NewRepository(client, logger)

	now := time.Now()
	testVersion := "test-1.0.0"
	startups := []models.Startup{
		{
			Timestamp:   now,
			AppVersion:  testVersion,
			Platform:    "Android",
			DeviceModel: "Pixel 6",
			OSVersion:   "13",
			SessionID:   "test-session-startup-1",
			DeviceID:    "test-device-startup-1",
			Phase1Ms:    500.0,
			Phase2Ms:    1200.0,
			TTIMs:       800.0,
		},
	}

	if err := repo.InsertStartups(ctx, startups); err != nil {
		t.Fatalf("insert startups failed: %v", err)
	}
	t.Log("Inserted startups")

	filter := models.QueryFilter{
		StartTime:  now.Add(-time.Hour),
		EndTime:    now.Add(time.Hour),
		AppVersion: testVersion,
	}

	results, err := repo.QueryStartupMetrics(ctx, filter)
	if err != nil {
		t.Fatalf("query startup metrics failed: %v", err)
	}

	t.Logf("Startup query returned %d results", len(results))
}

func TestRepository_InsertAndQueryExceptions(t *testing.T) {
	cfg := getTestConfig(t)
	logger := getTestLogger()

	client, err := NewClickHouseClient(cfg, logger)
	if err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	defer client.Close()

	ctx := context.Background()
	if err := client.Migrate(ctx); err != nil {
		t.Fatalf("migrate failed: %v", err)
	}

	repo := NewRepository(client, logger)

	now := time.Now()
	testVersion := "test-1.0.0"
	exceptions := []models.Exception{
		{
			Timestamp:   now,
			AppVersion:  testVersion,
			Platform:    "Android",
			DeviceModel: "Samsung S21",
			OSVersion:   "12",
			SessionID:   "test-session-exc-1",
			DeviceID:    "test-device-exc-1",
			Scene:       "LoadingScene",
			Fingerprint: "NullReferenceException_PlayerManager_123",
			Message:     "Object reference not set to an instance of an object",
			Stack:       "at PlayerManager.Update() in PlayerManager.cs:line 42",
			Count:       1,
		},
	}

	if err := repo.InsertExceptions(ctx, exceptions); err != nil {
		t.Fatalf("insert exceptions failed: %v", err)
	}
	t.Log("Inserted exceptions")

	filter := models.QueryFilter{
		StartTime:  now.Add(-time.Hour),
		EndTime:    now.Add(time.Hour),
		AppVersion: testVersion,
	}

	results, err := repo.QueryExceptions(ctx, filter)
	if err != nil {
		t.Fatalf("query exceptions failed: %v", err)
	}

	t.Logf("Exception query returned %d results", len(results))
}

func TestRepository_InsertAndQueryCrashes(t *testing.T) {
	cfg := getTestConfig(t)
	logger := getTestLogger()

	client, err := NewClickHouseClient(cfg, logger)
	if err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	defer client.Close()

	ctx := context.Background()
	if err := client.Migrate(ctx); err != nil {
		t.Fatalf("migrate failed: %v", err)
	}

	repo := NewRepository(client, logger)

	now := time.Now()
	testVersion := "test-1.0.0"
	crashes := []models.Crash{
		{
			Timestamp:   now,
			AppVersion:  testVersion,
			Platform:    "iOS",
			DeviceModel: "iPhone 14 Pro",
			OSVersion:   "16.1",
			SessionID:   "test-session-crash-1",
			DeviceID:    "test-device-crash-1",
			Scene:       "GameplayScene",
			CrashType:   "SIGSEGV",
			Fingerprint: "SIGSEGV_TextureManager_456",
			Stack:       "0x00001234 TextureManager::LoadTexture()\n0x00005678 AssetLoader::Load()",
			Breadcrumbs: []string{"scene_load:GameplayScene", "asset_load:texture_player", "network:api_call"},
		},
	}

	if err := repo.InsertCrashes(ctx, crashes); err != nil {
		t.Fatalf("insert crashes failed: %v", err)
	}
	t.Log("Inserted crashes")

	filter := models.QueryFilter{
		StartTime:  now.Add(-time.Hour),
		EndTime:    now.Add(time.Hour),
		AppVersion: testVersion,
	}

	results, err := repo.QueryCrashes(ctx, filter)
	if err != nil {
		t.Fatalf("query crashes failed: %v", err)
	}

	t.Logf("Crash query returned %d results", len(results))
}

func TestRepository_InsertSceneLoads(t *testing.T) {
	cfg := getTestConfig(t)
	logger := getTestLogger()

	client, err := NewClickHouseClient(cfg, logger)
	if err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	defer client.Close()

	ctx := context.Background()
	if err := client.Migrate(ctx); err != nil {
		t.Fatalf("migrate failed: %v", err)
	}

	repo := NewRepository(client, logger)

	now := time.Now()
	loads := []models.SceneLoad{
		{
			Timestamp:   now,
			AppVersion:  "test-1.0.0",
			Platform:    "Android",
			DeviceModel: "Pixel 7",
			SessionID:   "test-session-scene-1",
			DeviceID:    "test-device-scene-1",
			SceneName:   "Level1",
			LoadMs:      1500.0,
			ActivateMs:  200.0,
		},
	}

	if err := repo.InsertSceneLoads(ctx, loads); err != nil {
		t.Fatalf("insert scene loads failed: %v", err)
	}
	t.Log("Inserted scene loads")
}

func TestRepository_InsertEmpty(t *testing.T) {
	cfg := getTestConfig(t)
	logger := getTestLogger()

	client, err := NewClickHouseClient(cfg, logger)
	if err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	defer client.Close()

	ctx := context.Background()
	repo := NewRepository(client, logger)

	// All these should succeed with empty slices
	if err := repo.InsertPerfSamples(ctx, nil); err != nil {
		t.Errorf("insert empty perf samples failed: %v", err)
	}
	if err := repo.InsertJanks(ctx, []models.Jank{}); err != nil {
		t.Errorf("insert empty janks failed: %v", err)
	}
	if err := repo.InsertStartups(ctx, nil); err != nil {
		t.Errorf("insert empty startups failed: %v", err)
	}
	if err := repo.InsertSceneLoads(ctx, nil); err != nil {
		t.Errorf("insert empty scene loads failed: %v", err)
	}
	if err := repo.InsertExceptions(ctx, nil); err != nil {
		t.Errorf("insert empty exceptions failed: %v", err)
	}
	if err := repo.InsertCrashes(ctx, nil); err != nil {
		t.Errorf("insert empty crashes failed: %v", err)
	}

	t.Log("All empty inserts succeeded")
}

func TestRepository_Ping(t *testing.T) {
	cfg := getTestConfig(t)
	logger := getTestLogger()

	client, err := NewClickHouseClient(cfg, logger)
	if err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	defer client.Close()

	repo := NewRepository(client, logger)

	ctx := context.Background()
	if err := repo.Ping(ctx); err != nil {
		t.Errorf("repository ping failed: %v", err)
	}
}

func TestBuildWhereClause(t *testing.T) {
	tests := []struct {
		name     string
		filter   models.QueryFilter
		wantArgs int
	}{
		{
			name: "time range only",
			filter: models.QueryFilter{
				StartTime: time.Now().Add(-time.Hour),
				EndTime:   time.Now(),
			},
			wantArgs: 2,
		},
		{
			name: "with app version",
			filter: models.QueryFilter{
				StartTime:  time.Now().Add(-time.Hour),
				EndTime:    time.Now(),
				AppVersion: "1.0.0",
			},
			wantArgs: 3,
		},
		{
			name: "with all filters",
			filter: models.QueryFilter{
				StartTime:   time.Now().Add(-time.Hour),
				EndTime:     time.Now(),
				AppVersion:  "1.0.0",
				Platform:    "Android",
				DeviceModel: "Pixel",
				Scene:       "MainMenu",
			},
			wantArgs: 6,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clause, args := buildWhereClause(tt.filter)
			if len(args) != tt.wantArgs {
				t.Errorf("expected %d args, got %d", tt.wantArgs, len(args))
			}
			if clause == "" {
				t.Error("expected non-empty clause")
			}
			t.Logf("Clause: %s, Args: %d", clause, len(args))
		})
	}
}
