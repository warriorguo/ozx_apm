package processor

import (
	"testing"
	"time"
)

func TestNewAggregator(t *testing.T) {
	a := NewAggregator()
	if a == nil {
		t.Fatal("expected non-nil aggregator")
	}
	defer a.Stop()

	if a.stats == nil {
		t.Error("expected non-nil stats")
	}
	if a.stats.CrashCounts == nil {
		t.Error("expected non-nil CrashCounts map")
	}
	if a.stats.ExceptionCounts == nil {
		t.Error("expected non-nil ExceptionCounts map")
	}
	if a.stats.JankCounts == nil {
		t.Error("expected non-nil JankCounts map")
	}
}

func TestAggregator_RecordCrash(t *testing.T) {
	a := NewAggregator()
	defer a.Stop()

	// Record crashes
	a.RecordCrash("1.0.0", "session1")
	a.RecordCrash("1.0.0", "session2")
	a.RecordCrash("1.0.0", "session1") // Same session

	count, sessions := a.GetCrashRate("1.0.0")
	if count != 3 {
		t.Errorf("expected 3 crashes, got %d", count)
	}
	if sessions != 2 {
		t.Errorf("expected 2 unique sessions, got %d", sessions)
	}

	// Different version
	a.RecordCrash("2.0.0", "session3")
	count, sessions = a.GetCrashRate("2.0.0")
	if count != 1 {
		t.Errorf("expected 1 crash for 2.0.0, got %d", count)
	}
	if sessions != 1 {
		t.Errorf("expected 1 session for 2.0.0, got %d", sessions)
	}
}

func TestAggregator_RecordException(t *testing.T) {
	a := NewAggregator()
	defer a.Stop()

	// Record exceptions
	a.RecordException("1.0.0", "session1")
	a.RecordException("1.0.0", "session2")
	a.RecordException("1.0.0", "session1")

	count, sessions := a.GetExceptionRate("1.0.0")
	if count != 3 {
		t.Errorf("expected 3 exceptions, got %d", count)
	}
	if sessions != 2 {
		t.Errorf("expected 2 unique sessions, got %d", sessions)
	}
}

func TestAggregator_RecordJank(t *testing.T) {
	a := NewAggregator()
	defer a.Stop()

	// Record janks
	a.RecordJank("1.0.0", "session1")
	a.RecordJank("1.0.0", "session2")
	a.RecordJank("1.0.0", "session1")

	count, sessions := a.GetJankRate("1.0.0")
	if count != 3 {
		t.Errorf("expected 3 janks, got %d", count)
	}
	if sessions != 2 {
		t.Errorf("expected 2 unique sessions, got %d", sessions)
	}
}

func TestAggregator_GetRates_NonExistent(t *testing.T) {
	a := NewAggregator()
	defer a.Stop()

	// Get rates for non-existent version
	count, sessions := a.GetCrashRate("nonexistent")
	if count != 0 || sessions != 0 {
		t.Errorf("expected 0,0 for nonexistent version, got %d,%d", count, sessions)
	}

	count, sessions = a.GetExceptionRate("nonexistent")
	if count != 0 || sessions != 0 {
		t.Errorf("expected 0,0 for nonexistent version, got %d,%d", count, sessions)
	}

	count, sessions = a.GetJankRate("nonexistent")
	if count != 0 || sessions != 0 {
		t.Errorf("expected 0,0 for nonexistent version, got %d,%d", count, sessions)
	}
}

func TestAggregator_Cleanup(t *testing.T) {
	a := NewAggregator()
	// Don't start the cleanup loop for this test
	a.stopCh = make(chan struct{})
	defer close(a.stopCh)

	// Set a very short window for testing
	a.windowSize = 10 * time.Millisecond

	// Record events
	a.RecordCrash("1.0.0", "session1")
	a.RecordException("1.0.0", "session1")
	a.RecordJank("1.0.0", "session1")

	// Verify they exist
	count, _ := a.GetCrashRate("1.0.0")
	if count != 1 {
		t.Errorf("expected 1 crash before cleanup, got %d", count)
	}

	// Wait for data to expire
	time.Sleep(20 * time.Millisecond)

	// Run cleanup
	a.cleanup()

	// Verify they're gone
	count, _ = a.GetCrashRate("1.0.0")
	if count != 0 {
		t.Errorf("expected 0 crashes after cleanup, got %d", count)
	}

	count, _ = a.GetExceptionRate("1.0.0")
	if count != 0 {
		t.Errorf("expected 0 exceptions after cleanup, got %d", count)
	}

	count, _ = a.GetJankRate("1.0.0")
	if count != 0 {
		t.Errorf("expected 0 janks after cleanup, got %d", count)
	}
}

func TestAggregator_Stop(t *testing.T) {
	a := NewAggregator()

	// Stop should not panic
	a.Stop()

	// Calling methods after stop should not panic
	a.RecordCrash("1.0.0", "session1")
}

func TestAggregator_ConcurrentAccess(t *testing.T) {
	a := NewAggregator()
	defer a.Stop()

	done := make(chan bool)

	// Multiple goroutines recording events
	for i := 0; i < 10; i++ {
		go func(id int) {
			for j := 0; j < 100; j++ {
				a.RecordCrash("1.0.0", "session1")
				a.RecordException("1.0.0", "session2")
				a.RecordJank("1.0.0", "session3")
			}
			done <- true
		}(i)
	}

	// Multiple goroutines reading
	for i := 0; i < 5; i++ {
		go func() {
			for j := 0; j < 100; j++ {
				a.GetCrashRate("1.0.0")
				a.GetExceptionRate("1.0.0")
				a.GetJankRate("1.0.0")
			}
			done <- true
		}()
	}

	// Wait for all goroutines
	for i := 0; i < 15; i++ {
		<-done
	}

	// Verify counts
	count, _ := a.GetCrashRate("1.0.0")
	if count != 1000 { // 10 goroutines * 100 iterations
		t.Errorf("expected 1000 crashes, got %d", count)
	}
}
