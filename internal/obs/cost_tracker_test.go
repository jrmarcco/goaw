package obs

import (
	"sync"
	"testing"
	"time"

	"github.com/jrmarcco/goaw/internal/schema"
)

// testPhase 测试用阶段标记 ( phase 仅入日志，实现方不解释其取值 )。
const testPhase = "act"

func TestCostTrackerObserveAccumulates(t *testing.T) {
	t.Parallel()

	tracker := NewCostTracker("test-model", 0)

	tracker.Observe(schema.Usage{
		PromptTokens:        100,
		CompletionTokens:    20,
		CacheReadTokens:     60,
		CacheCreationTokens: 10,
	}, testPhase, time.Second)

	tracker.Observe(schema.Usage{
		PromptTokens:     50,
		CompletionTokens: 30,
	}, "final", 2*time.Second)

	snap := tracker.Snapshot()
	if snap.Calls != 2 {
		t.Fatalf("Calls = %d, want 2", snap.Calls)
	}
	if snap.PromptTokens != 150 || snap.CompletionTokens != 50 {
		t.Fatalf("tokens = (%d, %d), want (150, 50)", snap.PromptTokens, snap.CompletionTokens)
	}
	if snap.TotalTokens != 200 {
		t.Fatalf("TotalTokens = %d, want 200", snap.TotalTokens)
	}
	if snap.CacheReadTokens != 60 || snap.CacheCreationTokens != 10 {
		t.Fatalf("cache dims = (%d, %d), want (60, 10)", snap.CacheReadTokens, snap.CacheCreationTokens)
	}
	if snap.BusyDuration != 3*time.Second {
		t.Fatalf("BusyDuration = %v, want 3s", snap.BusyDuration)
	}
	if snap.Model != "test-model" || snap.TokenThreshold != 0 {
		t.Fatalf("labels = (%q, %d)", snap.Model, snap.TokenThreshold)
	}
}

func TestCostTrackerExceededBoundary(t *testing.T) {
	t.Parallel()

	// 阈值判定为 >=: 恰好达到即拦截。
	tracker := NewCostTracker("test-model", 100)
	if tracker.Exceeded() {
		t.Fatal("fresh tracker should not be exceeded")
	}

	tracker.Observe(schema.Usage{PromptTokens: 60, CompletionTokens: 39}, testPhase, 0)
	if tracker.Exceeded() {
		t.Fatal("99 tokens should not exceed a threshold of 100")
	}

	tracker.Observe(schema.Usage{PromptTokens: 1, CompletionTokens: 0}, testPhase, 0)
	if !tracker.Exceeded() {
		t.Fatal("100 tokens should exceed a threshold of 100")
	}

	// 阈值 ≤0 表示不设限，永不拦截。
	unlimited := NewCostTracker("test-model", 0)
	unlimited.Observe(schema.Usage{PromptTokens: 1_000_000, CompletionTokens: 1_000_000}, testPhase, 0)
	if unlimited.Exceeded() {
		t.Fatal("threshold <= 0 means unlimited and should never exceed")
	}
}

func TestCostTrackerSnapshotIsCopy(t *testing.T) {
	t.Parallel()

	tracker := NewCostTracker("test-model", 0)
	tracker.Observe(schema.Usage{PromptTokens: 10, CompletionTokens: 5}, testPhase, 0)

	snap := tracker.Snapshot()
	tracker.Observe(schema.Usage{PromptTokens: 10, CompletionTokens: 5}, testPhase, 0)

	if snap.Calls != 1 || snap.TotalTokens != 15 {
		t.Fatalf("snapshot mutated after further observes: %+v", snap)
	}
	if tracker.Snapshot().Calls != 2 {
		t.Fatal("tracker should reflect the second observe")
	}
}

func TestCostTrackerConcurrentObserve(t *testing.T) {
	t.Parallel()

	const (
		goroutines     = 8
		perRoutine     = 100
		promptEach     = 30
		completionEach = 20
	)

	tracker := NewCostTracker("test-model", 0)

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perRoutine; i++ {
				tracker.Observe(schema.Usage{
					PromptTokens:     promptEach,
					CompletionTokens: completionEach,
				}, testPhase, time.Millisecond)
			}
		}()
	}
	wg.Wait()

	snap := tracker.Snapshot()
	wantCalls := goroutines * perRoutine
	if snap.Calls != wantCalls {
		t.Fatalf("Calls = %d, want %d", snap.Calls, wantCalls)
	}
	wantTotal := wantCalls * (promptEach + completionEach)
	if snap.TotalTokens != wantTotal {
		t.Fatalf("TotalTokens = %d, want %d", snap.TotalTokens, wantTotal)
	}
}
