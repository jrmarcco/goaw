package reporter

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jrmarcco/goaw/internal/schema"
	"github.com/jrmarcco/goaw/internal/tools"
)

// sendStub 记录全部发送文案的离线发送替身。
type sendStub struct {
	mu     sync.Mutex
	sent   []string
	signal chan struct{}
}

func newSendStub() *sendStub {
	return &sendStub{signal: make(chan struct{}, 16)}
}

func (s *sendStub) send(_ context.Context, _, text string) error {
	s.mu.Lock()
	s.sent = append(s.sent, text)
	s.mu.Unlock()

	s.signal <- struct{}{}
	return nil
}

func (s *sendStub) snapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.sent...)
}

// waitPendingReqID 等待首个待审核请求登记并返回其编号。
func waitPendingReqID(t *testing.T, a *FeishuApprover) string {
	t.Helper()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		a.mu.Lock()
		for key := range a.pending {
			a.mu.Unlock()
			return key.reqID
		}
		a.mu.Unlock()
		time.Sleep(time.Millisecond)
	}
	t.Fatal("等待待审核请求登记超时")
	return ""
}

func TestFeishuApproverMissingChatIDFailsSafe(t *testing.T) {
	t.Parallel()

	// 非飞书入口的运行缺少 chatID，应立即兜底拒绝且不发送任何消息。
	approver := NewFeishuApprover(nil)
	result := approver.RequestApproval(context.Background(), schema.ToolCall{ID: approvalTestCallID, Name: approvalTestToolName})

	if result.Decision != tools.DecisionRejected {
		t.Fatalf("缺少 chatID 应兜底拒绝, got %q", result.Decision)
	}
	if result.Reason == "" {
		t.Fatal("兜底拒绝应携带可回传模型的原因文案")
	}
}

func TestFeishuApproverResolveFlow(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string

		cmd string

		wantDecision tools.ApprovalDecision
		wantRemember bool
	}{
		{
			name:         "approve via command",
			cmd:          "同意",
			wantDecision: tools.DecisionApproved,
		},
		{
			name:         "approve and remember via command",
			cmd:          "总是允许",
			wantDecision: tools.DecisionApproved,
			wantRemember: true,
		},
		{
			name:         "reject via command",
			cmd:          "拒绝",
			wantDecision: tools.DecisionRejected,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			sender := newSendStub()
			approver := NewFeishuApprover(nil)
			approver.send = sender.send

			ctx := withFeishuChatID(context.Background(), "chat-1")
			resCh := make(chan tools.ApprovalResult, 1)
			go func() {
				resCh <- approver.RequestApproval(ctx, schema.ToolCall{ID: approvalTestCallID, Name: approvalTestToolName})
			}()

			// 等待审核请求发出后，以口令回复决定。
			<-sender.signal
			reqID := waitPendingReqID(t, approver)

			if !approver.TryResolve(context.Background(), "chat-1", tt.cmd+" "+reqID) {
				t.Fatal("审核口令应被拦截处理")
			}

			select {
			case result := <-resCh:
				if result.Decision != tt.wantDecision {
					t.Fatalf("Decision = %q, want %q", result.Decision, tt.wantDecision)
				}
				if result.Remember != tt.wantRemember {
					t.Fatalf("Remember = %v, want %v", result.Remember, tt.wantRemember)
				}
				if result.Decision == tools.DecisionRejected && result.Reason == "" {
					t.Fatal("拒绝结果应携带原因文案")
				}
			case <-time.After(time.Second):
				t.Fatal("等待审核决定超时")
			}

			// 第二条消息是口令回执。
			if got := sender.snapshot(); len(got) != 2 {
				t.Fatalf("发送文案数 = %d, want 2 ( 审核请求 + 回执 )", len(got))
			}
		})
	}
}

func TestFeishuApproverCrossChatIsolation(t *testing.T) {
	t.Parallel()

	sender := newSendStub()
	approver := NewFeishuApprover(nil)
	approver.send = sender.send

	ctx := withFeishuChatID(context.Background(), "chat-1")
	resCh := make(chan tools.ApprovalResult, 1)
	go func() {
		resCh <- approver.RequestApproval(ctx, schema.ToolCall{ID: approvalTestCallID, Name: approvalTestToolName})
	}()

	<-sender.signal
	reqID := waitPendingReqID(t, approver)

	// 其他会话的口令不可跨会话命中等待者，但口令形态合法仍应被拦截。
	if !approver.TryResolve(context.Background(), "chat-2", "同意 "+reqID) {
		t.Fatal("口令形态合法应被拦截处理")
	}

	select {
	case result := <-resCh:
		t.Fatalf("跨会话口令不应命中等待者, 却得到结果 %v", result)
	case <-time.After(50 * time.Millisecond):
	}

	// 原会话口令仍可正常命中。
	if !approver.TryResolve(context.Background(), "chat-1", "同意 "+reqID) {
		t.Fatal("原会话口令应被拦截处理")
	}
	select {
	case result := <-resCh:
		if result.Decision != tools.DecisionApproved {
			t.Fatalf("Decision = %q, want %q", result.Decision, tools.DecisionApproved)
		}
	case <-time.After(time.Second):
		t.Fatal("等待审核决定超时")
	}
}

func TestFeishuApproverTryResolveNotCommand(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string

		text        string
		wantHandled bool
	}{
		{name: "normal prompt", text: "帮我搭一个 Web Server"},
		{name: "action without req id", text: "同意"},
		{name: "three tokens", text: "同意 0001 帮我执行"},
		{name: "action in wrong position", text: "执行 同意"},
		{name: "unknown action", text: "允许 0001"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			sender := newSendStub()
			approver := NewFeishuApprover(nil)
			approver.send = sender.send

			if got := approver.TryResolve(context.Background(), "chat-1", tt.text); got {
				t.Fatal("非口令消息不应被拦截")
			}
		})
	}
}

func TestFeishuApproverStaleCommand(t *testing.T) {
	t.Parallel()

	sender := newSendStub()
	approver := NewFeishuApprover(nil)
	approver.send = sender.send

	// 动作词合法但无匹配请求: 仍拦截，避免滞留口令被当作 prompt 交给模型。
	if !approver.TryResolve(context.Background(), "chat-1", "同意 9999") {
		t.Fatal("合法动作词的滞留口令应被拦截")
	}
	if got := len(sender.snapshot()); got != 1 {
		t.Fatalf("应发送一条忽略通知, got %d 条", got)
	}
}

func TestFeishuApproverContextCancelWhileWaiting(t *testing.T) {
	t.Parallel()

	sender := newSendStub()
	approver := NewFeishuApprover(nil)
	approver.send = sender.send

	ctx, cancel := context.WithCancel(withFeishuChatID(context.Background(), "chat-1"))
	resCh := make(chan tools.ApprovalResult, 1)
	go func() {
		resCh <- approver.RequestApproval(ctx, schema.ToolCall{ID: approvalTestCallID, Name: approvalTestToolName})
	}()

	<-sender.signal
	cancel()

	select {
	case result := <-resCh:
		if result.Decision != tools.DecisionRejected {
			t.Fatalf("等待期间 ctx 取消应拒绝, got %q", result.Decision)
		}
	case <-time.After(time.Second):
		t.Fatal("等待审核取消返回超时")
	}

	// 注销后 pending 不应残留。
	approver.mu.Lock()
	pendingCnt := len(approver.pending)
	approver.mu.Unlock()
	if pendingCnt != 0 {
		t.Fatalf("取消后 pending 应清空, 残留 %d 条", pendingCnt)
	}
}
