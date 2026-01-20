package unit

import (
	"testing"
	"time"

	"github.com/warriorguo/ozx_apm/server/internal/processor"
)

func TestAggregator_RecordCrash(t *testing.T) {
	a := processor.NewAggregator()
	defer a.Stop()

	// Record some crashes
	a.RecordCrash("1.0.0", "session1")
	a.RecordCrash("1.0.0", "session1")
	a.RecordCrash("1.0.0", "session2")

	count, sessions := a.GetCrashRate("1.0.0")

	if count != 3 {
		t.Errorf("GetCrashRate() count = %d, want 3", count)
	}
	if sessions != 2 {
		t.Errorf("GetCrashRate() sessions = %d, want 2", sessions)
	}
}

func TestAggregator_RecordException(t *testing.T) {
	a := processor.NewAggregator()
	defer a.Stop()

	// Record some exceptions
	a.RecordException("1.0.0", "session1")
	a.RecordException("1.0.0", "session1")
	a.RecordException("2.0.0", "session3")

	count1, sessions1 := a.GetExceptionRate("1.0.0")
	count2, sessions2 := a.GetExceptionRate("2.0.0")

	if count1 != 2 {
		t.Errorf("GetExceptionRate(1.0.0) count = %d, want 2", count1)
	}
	if sessions1 != 1 {
		t.Errorf("GetExceptionRate(1.0.0) sessions = %d, want 1", sessions1)
	}
	if count2 != 1 {
		t.Errorf("GetExceptionRate(2.0.0) count = %d, want 1", count2)
	}
	if sessions2 != 1 {
		t.Errorf("GetExceptionRate(2.0.0) sessions = %d, want 1", sessions2)
	}
}

func TestAggregator_RecordJank(t *testing.T) {
	a := processor.NewAggregator()
	defer a.Stop()

	// Record some janks
	a.RecordJank("1.0.0", "session1")
	a.RecordJank("1.0.0", "session2")
	a.RecordJank("1.0.0", "session3")

	count, sessions := a.GetJankRate("1.0.0")

	if count != 3 {
		t.Errorf("GetJankRate() count = %d, want 3", count)
	}
	if sessions != 3 {
		t.Errorf("GetJankRate() sessions = %d, want 3", sessions)
	}
}

func TestAggregator_NonExistentVersion(t *testing.T) {
	a := processor.NewAggregator()
	defer a.Stop()

	count, sessions := a.GetCrashRate("nonexistent")

	if count != 0 {
		t.Errorf("GetCrashRate(nonexistent) count = %d, want 0", count)
	}
	if sessions != 0 {
		t.Errorf("GetCrashRate(nonexistent) sessions = %d, want 0", sessions)
	}
}

func TestAggregator_StopIsIdempotent(t *testing.T) {
	a := processor.NewAggregator()

	// Should not panic when called multiple times
	a.Stop()

	// Give cleanup loop time to exit
	time.Sleep(20 * time.Millisecond)
}
