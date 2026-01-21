package models

import (
	"encoding/json"
	"testing"
	"time"
)

func TestDashboardSummary_JSON(t *testing.T) {
	summary := DashboardSummary{
		TimeRange: TimeRange{
			Start: time.Date(2024, 1, 14, 0, 0, 0, 0, time.UTC),
			End:   time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC),
		},
		TotalSessions:  10000,
		TotalEvents:    500000,
		CrashCount:     50,
		CrashRate:      0.005,
		ExceptionCount: 500,
		JankCount:      200,
		AvgFPS:         58.5,
		AvgStartupMs:   2500.0,
		TopVersions: []VersionStats{
			{Version: "1.2.0", SessionCount: 5000, CrashCount: 20, CrashRate: 0.004},
			{Version: "1.1.0", SessionCount: 3000, CrashCount: 20, CrashRate: 0.0067},
			{Version: "1.0.0", SessionCount: 2000, CrashCount: 10, CrashRate: 0.005},
		},
		TopPlatforms: []PlatformStats{
			{Platform: "Android", SessionCount: 6000, AvgFPS: 57.0},
			{Platform: "iOS", SessionCount: 4000, AvgFPS: 60.0},
		},
	}

	data, err := json.Marshal(summary)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var decoded DashboardSummary
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if decoded.TotalSessions != summary.TotalSessions {
		t.Errorf("expected total_sessions=%d, got %d", summary.TotalSessions, decoded.TotalSessions)
	}
	if decoded.CrashRate != summary.CrashRate {
		t.Errorf("expected crash_rate=%f, got %f", summary.CrashRate, decoded.CrashRate)
	}
	if len(decoded.TopVersions) != 3 {
		t.Errorf("expected 3 top_versions, got %d", len(decoded.TopVersions))
	}
	if len(decoded.TopPlatforms) != 2 {
		t.Errorf("expected 2 top_platforms, got %d", len(decoded.TopPlatforms))
	}
}

func TestTimeSeriesPoint_JSON(t *testing.T) {
	point := TimeSeriesPoint{
		Timestamp: time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC),
		Value:     59.5,
	}

	data, err := json.Marshal(point)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var decoded TimeSeriesPoint
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if decoded.Value != point.Value {
		t.Errorf("expected value=%f, got %f", point.Value, decoded.Value)
	}
}

func TestTimeSeriesResponse_JSON(t *testing.T) {
	resp := TimeSeriesResponse{
		Metric: "fps",
		Data: []TimeSeriesPoint{
			{Timestamp: time.Date(2024, 1, 15, 9, 0, 0, 0, time.UTC), Value: 58.0},
			{Timestamp: time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC), Value: 59.0},
			{Timestamp: time.Date(2024, 1, 15, 11, 0, 0, 0, time.UTC), Value: 60.0},
		},
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var decoded TimeSeriesResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if decoded.Metric != "fps" {
		t.Errorf("expected metric=fps, got %s", decoded.Metric)
	}
	if len(decoded.Data) != 3 {
		t.Errorf("expected 3 data points, got %d", len(decoded.Data))
	}
}

func TestDistributionResponse_JSON(t *testing.T) {
	resp := DistributionResponse{
		Metric: "frame_time",
		Buckets: []DistributionBucket{
			{Bucket: "0-10", Count: 1000, Pct: 10.0},
			{Bucket: "10-20", Count: 5000, Pct: 50.0},
			{Bucket: "20-30", Count: 3000, Pct: 30.0},
			{Bucket: "30+", Count: 1000, Pct: 10.0},
		},
		P50: 15.0,
		P90: 28.0,
		P95: 32.0,
		P99: 45.0,
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var decoded DistributionResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if decoded.Metric != "frame_time" {
		t.Errorf("expected metric=frame_time, got %s", decoded.Metric)
	}
	if len(decoded.Buckets) != 4 {
		t.Errorf("expected 4 buckets, got %d", len(decoded.Buckets))
	}
	if decoded.P50 != 15.0 {
		t.Errorf("expected p50=15.0, got %f", decoded.P50)
	}
	if decoded.P99 != 45.0 {
		t.Errorf("expected p99=45.0, got %f", decoded.P99)
	}
}

func TestCrashGroup_JSON(t *testing.T) {
	group := CrashGroup{
		Fingerprint:      "crash-123",
		CrashType:        "SIGSEGV",
		SampleMessage:    "Segmentation fault in libunity.so",
		Count:            100,
		SessionCount:     50,
		FirstSeen:        time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		LastSeen:         time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC),
		AffectedVersions: []string{"1.0.0", "1.1.0"},
		TopDevices:       []string{"Pixel 6", "Galaxy S21", "iPhone 14"},
	}

	data, err := json.Marshal(group)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var decoded CrashGroup
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if decoded.Fingerprint != group.Fingerprint {
		t.Errorf("expected fingerprint=%s, got %s", group.Fingerprint, decoded.Fingerprint)
	}
	if decoded.Count != group.Count {
		t.Errorf("expected count=%d, got %d", group.Count, decoded.Count)
	}
	if len(decoded.AffectedVersions) != 2 {
		t.Errorf("expected 2 affected_versions, got %d", len(decoded.AffectedVersions))
	}
}

func TestCrashListResponse_JSON(t *testing.T) {
	resp := CrashListResponse{
		Crashes: []CrashGroup{
			{Fingerprint: "crash-1", CrashType: "SIGSEGV", Count: 100},
			{Fingerprint: "crash-2", CrashType: "SIGABRT", Count: 50},
		},
		TotalCount: 150,
		Page:       1,
		PageSize:   20,
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var decoded CrashListResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if len(decoded.Crashes) != 2 {
		t.Errorf("expected 2 crashes, got %d", len(decoded.Crashes))
	}
	if decoded.TotalCount != 150 {
		t.Errorf("expected total_count=150, got %d", decoded.TotalCount)
	}
}

func TestCrashDetail_JSON(t *testing.T) {
	detail := CrashDetail{
		Fingerprint:  "crash-123",
		CrashType:    "SIGSEGV",
		Stack:        "at NativeMethod()\nat UnityEngine.Render()",
		Count:        100,
		SessionCount: 50,
		FirstSeen:    time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		LastSeen:     time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC),
		Occurrences: []CrashOccurrence{
			{
				Timestamp:   time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC),
				AppVersion:  "1.0.0",
				Platform:    "Android",
				DeviceModel: "Pixel 6",
				OSVersion:   "Android 13",
				Scene:       "GamePlay",
				Breadcrumbs: []string{"scene_load", "asset_load"},
			},
		},
		VersionDist: []VersionDist{
			{Version: "1.0.0", Count: 80},
			{Version: "1.1.0", Count: 20},
		},
		DeviceDist: []DeviceDist{
			{Device: "Pixel 6", Count: 60},
			{Device: "Galaxy S21", Count: 40},
		},
		OSDist: []OSDist{
			{OS: "Android 13", Count: 70},
			{OS: "Android 12", Count: 30},
		},
	}

	data, err := json.Marshal(detail)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var decoded CrashDetail
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if decoded.Fingerprint != detail.Fingerprint {
		t.Errorf("expected fingerprint=%s, got %s", detail.Fingerprint, decoded.Fingerprint)
	}
	if len(decoded.Occurrences) != 1 {
		t.Errorf("expected 1 occurrence, got %d", len(decoded.Occurrences))
	}
	if len(decoded.VersionDist) != 2 {
		t.Errorf("expected 2 version_distribution, got %d", len(decoded.VersionDist))
	}
}

func TestExceptionGroup_JSON(t *testing.T) {
	group := ExceptionGroup{
		Fingerprint:  "exc-123",
		Message:      "NullReferenceException: Object reference not set",
		Count:        500,
		SessionCount: 200,
		FirstSeen:    time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		LastSeen:     time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC),
	}

	data, err := json.Marshal(group)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var decoded ExceptionGroup
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if decoded.Fingerprint != group.Fingerprint {
		t.Errorf("expected fingerprint=%s, got %s", group.Fingerprint, decoded.Fingerprint)
	}
	if decoded.Count != group.Count {
		t.Errorf("expected count=%d, got %d", group.Count, decoded.Count)
	}
}

func TestExceptionListResponse_JSON(t *testing.T) {
	resp := ExceptionListResponse{
		Exceptions: []ExceptionGroup{
			{Fingerprint: "exc-1", Message: "NullReferenceException", Count: 500},
			{Fingerprint: "exc-2", Message: "IndexOutOfRangeException", Count: 200},
		},
		TotalCount: 700,
		Page:       1,
		PageSize:   20,
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var decoded ExceptionListResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if len(decoded.Exceptions) != 2 {
		t.Errorf("expected 2 exceptions, got %d", len(decoded.Exceptions))
	}
	if decoded.TotalCount != 700 {
		t.Errorf("expected total_count=700, got %d", decoded.TotalCount)
	}
}

func TestVersionStats_JSON(t *testing.T) {
	stats := VersionStats{
		Version:      "1.0.0",
		SessionCount: 5000,
		CrashCount:   25,
		CrashRate:    0.005,
	}

	data, err := json.Marshal(stats)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var decoded VersionStats
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if decoded.Version != stats.Version {
		t.Errorf("expected version=%s, got %s", stats.Version, decoded.Version)
	}
	if decoded.CrashRate != stats.CrashRate {
		t.Errorf("expected crash_rate=%f, got %f", stats.CrashRate, decoded.CrashRate)
	}
}

func TestPlatformStats_JSON(t *testing.T) {
	stats := PlatformStats{
		Platform:     "Android",
		SessionCount: 6000,
		AvgFPS:       57.5,
	}

	data, err := json.Marshal(stats)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var decoded PlatformStats
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if decoded.Platform != stats.Platform {
		t.Errorf("expected platform=%s, got %s", stats.Platform, decoded.Platform)
	}
	if decoded.AvgFPS != stats.AvgFPS {
		t.Errorf("expected avg_fps=%f, got %f", stats.AvgFPS, decoded.AvgFPS)
	}
}
