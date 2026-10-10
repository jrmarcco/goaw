package tools

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jrmarcco/goaw/internal/schema"
)

// stubApprover 可编程的人工审核替身。
// 支持编排决定、是否记忆、阻塞行为，并统计被调用次数。
type stubApprover struct {
	decision ApprovalDecision
	reason   string
	remember bool

	// delay 决定前的等待时长; 等待期间尊重 ctx 取消 ( 模拟真实交互 )。
	delay time.Duration

	calls atomic.Int32
}

// 审核测试的固定工具名: 只读工具走策略放行，写/执行类工具走人工审核。
const (
	readOnlyToolName = "file_reader"
	writeToolName    = "bash"
)

func (s *stubApprover) RequestApproval(ctx context.Context, _ schema.ToolCall) ApprovalResult {
	s.calls.Add(1)

	if s.delay > 0 {
		select {
		case <-ctx.Done():
			return ApprovalResult{Decision: DecisionRejected, Reason: "人工交互被取消"}
		case <-time.After(s.delay):
		}
	}

	return ApprovalResult{
		Decision: s.decision,
		Reason:   s.reason,
		Remember: s.remember,
	}
}

func TestApprovePolicyAutoAllowReadOnly(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		toolName string
	}{
		{name: "file reader", toolName: readOnlyToolName},
		{name: "skill reader", toolName: "skill_reader"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			approver := &stubApprover{decision: DecisionRejected}
			manager := NewApprovalManager(ApprovalManagerWithApprover(approver))

			result := manager.Approve(context.Background(), schema.ToolCall{ID: stubCallID, Name: tt.toolName})

			if result.Decision != DecisionApproved {
				t.Fatalf("Decision = %q, want %q", result.Decision, DecisionApproved)
			}
			if result.Source != SourcePolicy {
				t.Fatalf("Source = %q, want %q", result.Source, SourcePolicy)
			}
			if got := approver.calls.Load(); got != 0 {
				t.Fatalf("只读工具不应进入人工审核, approver 被调用了 %d 次", got)
			}
		})
	}
}

func TestApproveManualDecision(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string

		approverDecision ApprovalDecision
		approverReason   string

		wantDecision ApprovalDecision
		wantSource   ApprovalSource
	}{
		{
			name:             "human approves",
			approverDecision: DecisionApproved,
			wantDecision:     DecisionApproved,
			wantSource:       SourceManual,
		},
		{
			name:             "human rejects with reason passthrough",
			approverDecision: DecisionRejected,
			approverReason:   "命令具有破坏性",
			wantDecision:     DecisionRejected,
			wantSource:       SourceManual,
		},
		{
			name:             "illegal empty decision fails safe as rejected",
			approverDecision: "",
			wantDecision:     DecisionRejected,
			wantSource:       SourceManual,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			approver := &stubApprover{decision: tt.approverDecision, reason: tt.approverReason}
			manager := NewApprovalManager(ApprovalManagerWithApprover(approver))

			result := manager.Approve(context.Background(), schema.ToolCall{ID: stubCallID, Name: writeToolName})

			if result.Decision != tt.wantDecision {
				t.Fatalf("Decision = %q, want %q", result.Decision, tt.wantDecision)
			}
			if result.Source != tt.wantSource {
				t.Fatalf("Source = %q, want %q", result.Source, tt.wantSource)
			}
			if tt.approverReason != "" && result.Reason != tt.approverReason {
				t.Fatalf("Reason = %q, want %q", result.Reason, tt.approverReason)
			}
			if got := approver.calls.Load(); got != 1 {
				t.Fatalf("approver 调用次数 = %d, want 1", got)
			}
		})
	}
}

func TestApproveRememberCache(t *testing.T) {
	t.Parallel()

	approver := &stubApprover{decision: DecisionApproved, remember: true}
	manager := NewApprovalManager(ApprovalManagerWithApprover(approver))

	first := manager.Approve(context.Background(), schema.ToolCall{ID: stubCallID, Name: writeToolName})
	if first.Decision != DecisionApproved || first.Source != SourceManual {
		t.Fatalf("首次审核 = (%q, %q), want (%q, %q)", first.Decision, first.Source, DecisionApproved, SourceManual)
	}

	second := manager.Approve(context.Background(), schema.ToolCall{ID: stubCallID, Name: writeToolName})
	if second.Decision != DecisionApproved || second.Source != SourceCache {
		t.Fatalf("二次审核 = (%q, %q), want (%q, %q)", second.Decision, second.Source, DecisionApproved, SourceCache)
	}
	if got := approver.calls.Load(); got != 1 {
		t.Fatalf("记忆命中的审核不应再次打扰人工, approver 调用次数 = %d, want 1", got)
	}

	// 缓存按工具名隔离: 另一个写类工具仍需人工审核。
	manager.Approve(context.Background(), schema.ToolCall{ID: stubCallID, Name: "file_writer"})
	if got := approver.calls.Load(); got != 2 {
		t.Fatalf("其他工具应走人工审核, approver 调用次数 = %d, want 2", got)
	}

	// Reset 清空记忆后重新进入人工审核。
	manager.Reset()
	manager.Approve(context.Background(), schema.ToolCall{ID: stubCallID, Name: writeToolName})
	if got := approver.calls.Load(); got != 3 {
		t.Fatalf("Reset 后应重新走人工审核, approver 调用次数 = %d, want 3", got)
	}
}

func TestApproveWithoutApproverFailsSafe(t *testing.T) {
	t.Parallel()

	manager := NewApprovalManager()

	result := manager.Approve(context.Background(), schema.ToolCall{ID: stubCallID, Name: writeToolName})

	if result.Decision != DecisionRejected {
		t.Fatalf("未配置审核器时应兜底拒绝, got %q", result.Decision)
	}
	if result.Reason == "" {
		t.Fatal("兜底拒绝应携带可回传模型的原因文案")
	}
}

func TestApproveTimeout(t *testing.T) {
	t.Parallel()

	// 审核器阻塞 100ms，超时 10ms 后由 Manager 兜底拒绝。
	approver := &stubApprover{decision: DecisionApproved, delay: 100 * time.Millisecond}
	manager := NewApprovalManager(
		ApprovalManagerWithApprover(approver),
		ApprovalManagerWithTimeout(10*time.Millisecond),
	)

	result := manager.Approve(context.Background(), schema.ToolCall{ID: stubCallID, Name: writeToolName})

	if result.Decision != DecisionRejected {
		t.Fatalf("审核超时应拒绝, got %q", result.Decision)
	}
	if result.Source != SourceTimeout {
		t.Fatalf("Source = %q, want %q", result.Source, SourceTimeout)
	}
}

func TestApproveParentContextCanceled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	approver := &stubApprover{decision: DecisionApproved, delay: 100 * time.Millisecond}
	manager := NewApprovalManager(
		ApprovalManagerWithApprover(approver),
		ApprovalManagerWithTimeout(time.Minute),
	)

	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	result := manager.Approve(ctx, schema.ToolCall{ID: stubCallID, Name: writeToolName})

	if result.Decision != DecisionRejected {
		t.Fatalf("父 ctx 取消时应拒绝, got %q", result.Decision)
	}
}

func TestApproveCustomPolicy(t *testing.T) {
	t.Parallel()

	t.Run("all tools need approval", func(t *testing.T) {
		t.Parallel()

		approver := &stubApprover{decision: DecisionApproved}
		manager := NewApprovalManager(
			ApprovalManagerWithPolicy(ApprovalPolicyFunc(func(_ schema.ToolCall) bool { return true })),
			ApprovalManagerWithApprover(approver),
		)

		result := manager.Approve(context.Background(), schema.ToolCall{ID: stubCallID, Name: readOnlyToolName})

		if result.Source != SourceManual {
			t.Fatalf("自定义全量审核策略下 Source = %q, want %q", result.Source, SourceManual)
		}
	})

	t.Run("no tool needs approval", func(t *testing.T) {
		t.Parallel()

		approver := &stubApprover{decision: DecisionRejected}
		manager := NewApprovalManager(
			ApprovalManagerWithPolicy(ApprovalPolicyFunc(func(_ schema.ToolCall) bool { return false })),
			ApprovalManagerWithApprover(approver),
		)

		result := manager.Approve(context.Background(), schema.ToolCall{ID: stubCallID, Name: writeToolName})

		if result.Source != SourcePolicy {
			t.Fatalf("自定义免审核策略下 Source = %q, want %q", result.Source, SourcePolicy)
		}
	})
}

func TestApprovalMiddleware(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string

		approverDecision ApprovalDecision

		wantAllowed bool
		wantReason  string
	}{
		{
			name:             "approved middleware allows",
			approverDecision: DecisionApproved,
			wantAllowed:      true,
		},
		{
			name:             "rejected middleware intercepts with reason",
			approverDecision: DecisionRejected,
			wantReason:       "风险操作",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			manager := NewApprovalManager(
				ApprovalManagerWithApprover(&stubApprover{decision: tt.approverDecision, reason: tt.wantReason}),
			)

			allowed, reason := manager.Middleware()(context.Background(), schema.ToolCall{ID: stubCallID, Name: writeToolName})

			if allowed != tt.wantAllowed {
				t.Fatalf("allowed = %v, want %v", allowed, tt.wantAllowed)
			}
			if reason != tt.wantReason {
				t.Fatalf("reason = %q, want %q", reason, tt.wantReason)
			}
		})
	}
}

// TestApproveConcurrentRemember 并发调用同一写类工具，
// 主要在 -race 下检验记忆缓存的锁纪律。
func TestApproveConcurrentRemember(t *testing.T) {
	t.Parallel()

	approver := &stubApprover{decision: DecisionApproved, remember: true}
	manager := NewApprovalManager(ApprovalManagerWithApprover(approver))

	const concurrency = 10
	results := make([]ApprovalResult, concurrency)

	var wg sync.WaitGroup
	for i := range concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = manager.Approve(context.Background(), schema.ToolCall{ID: stubCallID, Name: writeToolName})
		}()
	}
	wg.Wait()

	for idx, result := range results {
		if result.Decision != DecisionApproved {
			t.Fatalf("results[%d].Decision = %q, want %q", idx, result.Decision, DecisionApproved)
		}
	}
}
