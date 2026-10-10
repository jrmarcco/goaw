package reporter

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jrmarcco/goaw/internal/schema"
	"github.com/jrmarcco/goaw/internal/tools"
	lark "github.com/larksuite/oapi-sdk-go/v3"
)

var _ tools.Approver = (*FeishuApprover)(nil)

// feishuChatIDCtxKey 飞书 chatID 在 context 中的键类型。
// 独立的私有结构体类型，保证不与其他包注入的 context 值冲突。
type feishuChatIDCtxKey struct{}

// withFeishuChatID 将飞书 chatID 注入 context。
// chatID 由入口 ( FeishuBot.agentRun ) 注入，经引擎流向工具层，
// FeishuApprover 据此把审核消息路由回发起工具调用的会话。
func withFeishuChatID(ctx context.Context, chatID string) context.Context {
	return context.WithValue(ctx, feishuChatIDCtxKey{}, chatID)
}

func feishuChatIDFromContext(ctx context.Context) string {
	chatID, _ := ctx.Value(feishuChatIDCtxKey{}).(string)
	return chatID
}

// 审核口令的动作词，格式为 「动作 请求编号」( 空格分隔 )。
const (
	approvalCmdApprove = "同意"
	approvalCmdAlways  = "总是允许"
	approvalCmdReject  = "拒绝"
)

// approvalKey 待审核请求的定位键。
// chatID 参与寻址，防止其他会话的口令跨会话命中等待者。
type approvalKey struct {
	chatID string
	reqID  string
}

const (
	defaultApprovalSendTimeout = 10 * time.Second

	// approvalCmdTokenCount 口令的固定词数: 「动作 请求编号」。
	approvalCmdTokenCount = 2
)

// FeishuApprover 飞书人工审核器 ( 口令模式 ):
// RequestApproval 向发起工具调用的会话发送审核请求并阻塞等待口令回复，
// FeishuBot 的消息事件回调将口令经 TryResolve 路由回对应的等待者。
//
// 与 TerminalApprover 不同，等待期间监听 ctx 取消，可被 ApprovalManager 超时正常中断。
type FeishuApprover struct {
	mu      sync.Mutex
	pending map[approvalKey]chan tools.ApprovalResult

	sendTimeout time.Duration
	client      *lark.Client

	// send 发送 seam，默认走飞书 API，测试中替换为离线实现。
	send func(ctx context.Context, chatID, text string) error

	// seq 请求编号发号器，进程内单调递增。
	seq atomic.Uint64
}

func NewFeishuApprover(client *lark.Client) *FeishuApprover {
	a := &FeishuApprover{
		pending: make(map[approvalKey]chan tools.ApprovalResult),

		sendTimeout: defaultApprovalSendTimeout,
		client:      client,
	}
	a.send = a.sendViaLark
	return a
}

// RequestApproval 实现 tools.Approver。
// 上下文缺少 chatID ( 非飞书入口的运行 ) 时兜底拒绝。
func (a *FeishuApprover) RequestApproval(ctx context.Context, tc schema.ToolCall) tools.ApprovalResult {
	chatID := feishuChatIDFromContext(ctx)
	if chatID == "" {
		return tools.ApprovalResult{
			Decision: tools.DecisionRejected,
			Reason:   "飞书审核器无法从上下文解析当前会话，默认拒绝",
		}
	}

	key := approvalKey{
		chatID: chatID,
		reqID:  strconv.FormatUint(a.seq.Add(1), 10),
	}
	// 缓冲为 1: TryResolve 单次投递，等待者离开 ( 超时/取消 ) 后迟到口令不阻塞回调方。
	ch := make(chan tools.ApprovalResult, 1)

	a.mu.Lock()
	a.pending[key] = ch
	a.mu.Unlock()

	// 等待结束 ( 决定、超时或取消 ) 后注销，防止泄漏与迟到的口令命中。
	defer func() {
		a.mu.Lock()
		delete(a.pending, key)
		a.mu.Unlock()
	}()

	sendCtx, cancel := context.WithTimeout(ctx, a.sendTimeout)
	defer cancel()

	if err := a.send(sendCtx, chatID, a.buildRequestText(key.reqID, tc)); err != nil {
		slog.Error("[feishu-approval] 审核请求发送失败", "chat_id", chatID, "req_id", key.reqID, "error", err)
		return tools.ApprovalResult{
			Decision: tools.DecisionRejected,
			Reason:   "审核请求发送失败，默认拒绝",
		}
	}

	select {
	case result := <-ch:
		return result
	case <-ctx.Done():
		return tools.ApprovalResult{
			Decision: tools.DecisionRejected,
			Reason:   "人工审核超时未响应，系统默认拒绝",
		}
	}
}

// TryResolve 尝试将一条飞书消息解析为审核口令并路由给对应等待者。
// 返回 false 表示不是口令消息，调用方 ( FeishuBot ) 按普通用户输入处理。
// 动作词合法但无匹配请求时仍返回 true，避免滞留口令被当作 prompt 交给模型。
func (a *FeishuApprover) TryResolve(ctx context.Context, chatID, text string) bool {
	// 口令形态固定为两个词: 「动作 请求编号」。
	fields := strings.Fields(text)
	if len(fields) != approvalCmdTokenCount {
		return false
	}
	action, reqID := fields[0], fields[1]

	var result tools.ApprovalResult
	switch action {
	case approvalCmdApprove:
		result = tools.ApprovalResult{Decision: tools.DecisionApproved}
	case approvalCmdAlways:
		result = tools.ApprovalResult{Decision: tools.DecisionApproved, Remember: true}
	case approvalCmdReject:
		result = tools.ApprovalResult{
			Decision: tools.DecisionRejected,
			Reason:   "人工审核选择拒绝执行该工具调用",
		}
	default:
		return false
	}

	key := approvalKey{chatID: chatID, reqID: reqID}
	a.mu.Lock()
	ch, ok := a.pending[key]
	if ok {
		delete(a.pending, key)
	}
	a.mu.Unlock()

	if !ok {
		a.notify(ctx, chatID, fmt.Sprintf("编号 %s 无待审核请求，口令已忽略", reqID))
		return true
	}

	ch <- result
	a.notify(ctx, chatID, fmt.Sprintf("✅ 已记录审核决定 ( 编号 %s )", reqID))
	return true
}

// buildRequestText 构造发送给会话的审核请求文案。
func (a *FeishuApprover) buildRequestText(reqID string, tc schema.ToolCall) string {
	return fmt.Sprintf(
		"⚠️ 工具 [%s] 请求人工审核 ( 编号 %s )\n参数: %s\n回复口令: 「%s %s」允许一次 / 「%s %s」总是允许 / 「%s %s」拒绝",
		tc.Name, reqID, tc.Args,
		approvalCmdApprove, reqID,
		approvalCmdAlways, reqID,
		approvalCmdReject, reqID,
	)
}

// notify 发送口令处理回执，失败仅记录日志，不影响审核结果。
func (a *FeishuApprover) notify(ctx context.Context, chatID, text string) {
	notifyCtx, cancel := context.WithTimeout(ctx, a.sendTimeout)
	defer cancel()

	if err := a.send(notifyCtx, chatID, text); err != nil {
		slog.Warn("[feishu-approval] 审核回执发送失败", "chat_id", chatID, "error", err)
	}
}

// sendViaLark 经飞书 API 发送文本消息，复用 FeishuReporter 的发送实现。
func (a *FeishuApprover) sendViaLark(ctx context.Context, chatID, text string) error {
	return NewFeishuReporter(a.client, chatID).sendMessage(ctx, text)
}
