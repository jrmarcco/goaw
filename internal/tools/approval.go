package tools

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/jrmarcco/goaw/internal/schema"
	"github.com/jrmarcco/jit/xbean/option"
)

const defaultApprovalTimeout = 5 * time.Minute

// ApprovalDecision 审核决定。
type ApprovalDecision string

const (
	DecisionApproved ApprovalDecision = "approved" // 放行
	DecisionRejected ApprovalDecision = "rejected" // 拒绝
)

// ApprovalSource 审核决定来源。
// 用于日志观测与测试断言，说明结果由决策链的哪一环产生。
type ApprovalSource string

const (
	SourcePolicy  ApprovalSource = "policy"  // 策略自动放行 ( 只读工具 )
	SourceCache   ApprovalSource = "cache"   // 人工曾选择 "总是允许" 记忆缓存直接放行
	SourceManual  ApprovalSource = "manual"  // 人工即时确认
	SourceTimeout ApprovalSource = "timeout" // 等待人工超时，安全兜底拒绝
)

// ApprovalResult 是一次工具调用审核的结果。
// 拒绝时的 Reason 经 Registry 拦截文案包装 ( ErrCodeCallIntercepted ) 回传给模型，指导其调整后续动作;
// Remember 仅在人工放行且勾选 "总是允许" 时置位，由 ApprovalManager 消费写入记忆缓存，对调用方只读。
type ApprovalResult struct {
	Decision ApprovalDecision `json:"decision"`
	Reason   string           `json:"reason,omitempty"`
	Source   ApprovalSource   `json:"source"`
	Remember bool             `json:"remember,omitempty"`
}

// Allowed 报告本次审核是否放行。
func (r ApprovalResult) Allowed() bool {
	return r.Decision == DecisionApproved
}

// ApprovalPolicy 审核策略: 判定一个工具调用是否需要人工审核。
type ApprovalPolicy interface {
	// NeedsApproval 返回 true 表示该调用需进入人工审核。
	NeedsApproval(tc schema.ToolCall) bool
}

// ApprovalPolicyFunc 函数适配器，让闭包直接作为 ApprovalPolicy 使用。
type ApprovalPolicyFunc func(tc schema.ToolCall) bool

func (f ApprovalPolicyFunc) NeedsApproval(tc schema.ToolCall) bool {
	return f(tc)
}

// readOnlyTools 只读工具白名单。
// 不在名单内的工具 ( 含未来新增的工具 ) 一律进入人工审核，默认从严。
var readOnlyTools = map[string]struct{}{
	"file_reader":  {},
	"skill_reader": {},
}

// defaultApprovalPolicy 默认审核策略: 只读工具放行，其余全部进入人工审核。
var defaultApprovalPolicy ApprovalPolicy = ApprovalPolicyFunc(
	func(tc schema.ToolCall) bool {
		_, ok := readOnlyTools[tc.Name]
		return !ok
	},
)

// Approver 人工审核器: 展示待审核的工具调用并阻塞等待人工决定。
// 具体交互 ( 终端、飞书口令 ) 由调用方注入，本包不预设实现。
// 实现必须尊重 ctx 取消: ctx 结束时立即返回未决结果，由 Manager 兜底为拒绝。
type Approver interface {
	RequestApproval(ctx context.Context, tc schema.ToolCall) ApprovalResult
}

// ApproverFunc 函数适配器，让闭包直接作为 Approver 使用。
type ApproverFunc func(ctx context.Context, tc schema.ToolCall) ApprovalResult

func (f ApproverFunc) RequestApproval(ctx context.Context, tc schema.ToolCall) ApprovalResult {
	return f(ctx, tc)
}

// ApprovalManager 工具调用审核管理器 ( 混合模式 )。
//
// 决策链: 记忆缓存 → 策略判定 → 人工审核 ( 带超时 )。
//
// 并发契约:
// Registry 在 errgroup goroutine 中调用 Middleware，记忆缓存的访问需持锁;
// 但人工审核耗时长，等待人工期间不持锁。
// 记忆缓存为进程级生命周期，不按会话隔离，入口可用 Reset 手动清空。
type ApprovalManager struct {
	mu sync.Mutex

	remembered map[string]struct{}

	policy   ApprovalPolicy
	approver Approver
	timeout  time.Duration
}

func ApprovalManagerWithPolicy(policy ApprovalPolicy) option.Opt[ApprovalManager] {
	return func(m *ApprovalManager) {
		m.policy = policy
	}
}

func ApprovalManagerWithApprover(approver Approver) option.Opt[ApprovalManager] {
	return func(m *ApprovalManager) {
		m.approver = approver
	}
}

func ApprovalManagerWithTimeout(timeout time.Duration) option.Opt[ApprovalManager] {
	return func(m *ApprovalManager) {
		m.timeout = timeout
	}
}

func NewApprovalManager(opts ...option.Opt[ApprovalManager]) *ApprovalManager {
	m := &ApprovalManager{
		remembered: make(map[string]struct{}),

		policy:   defaultApprovalPolicy,
		approver: nil,
		timeout:  defaultApprovalTimeout,
	}

	option.Apply(m, opts...)
	return m
}

// Approve 审核一次工具调用。
// 所有异常路径 ( 未配置审核器、人工未决、超时、非法决定 ) 一律兜底为拒绝。
func (m *ApprovalManager) Approve(ctx context.Context, tc schema.ToolCall) ApprovalResult {
	// 1. 人工曾选择 "总是允许"，记忆缓存直接放行。
	m.mu.Lock()
	if _, ok := m.remembered[tc.Name]; ok {
		m.mu.Unlock()
		return ApprovalResult{Decision: DecisionApproved, Source: SourceCache}
	}
	m.mu.Unlock()

	// 2. 策略判定: 无需审核的工具自动放行。
	if !m.policy.NeedsApproval(tc) {
		return ApprovalResult{Decision: DecisionApproved, Source: SourcePolicy}
	}

	// 3. 需要人工审核但未配置审核器，安全兜底拒绝。
	if m.approver == nil {
		return ApprovalResult{
			Decision: DecisionRejected,
			Reason:   "该工具需要人工审核，但当前未配置人工审核器",
			Source:   SourcePolicy,
		}
	}

	// 4. 人工审核，带超时; 等待期间不持锁，不阻塞其他工具调用的审核。
	approveCtx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()

	result := m.approver.RequestApproval(approveCtx, tc)

	switch result.Decision {
	case DecisionApproved:
		// 人工勾选 "总是允许" 时记住该工具，后续调用走缓存放行。
		if result.Remember {
			m.mu.Lock()
			m.remembered[tc.Name] = struct{}{}
			m.mu.Unlock()
		}
		return ApprovalResult{
			Decision: DecisionApproved,
			Reason:   result.Reason,
			Source:   SourceManual,
		}
	default:
		// 拒绝、未决 ( ctx 取消 ) 与非法决定统一按拒绝处理:
		// 未决时给出可区分的来源与文案，其余透传人工给出的原因。
		if approveCtx.Err() != nil {
			return ApprovalResult{
				Decision: DecisionRejected,
				Reason:   "人工审核超时未响应，系统默认拒绝",
				Source:   SourceTimeout,
			}
		}
		return ApprovalResult{
			Decision: DecisionRejected,
			Reason:   result.Reason,
			Source:   SourceManual,
		}
	}
}

// Middleware 将管理器适配为 Registry 的审核中间件。
// 拒绝时的原因文案由 Registry 统一包装为 ErrCodeCallIntercepted 回传模型。
func (m *ApprovalManager) Middleware() Middleware {
	return func(ctx context.Context, tc schema.ToolCall) (bool, string) {
		result := m.Approve(ctx, tc)
		if result.Allowed() {
			slog.Debug(
				"[approval] 工具调用审核通过",
				"tool_name", tc.Name,
				"source", result.Source,
			)
			return true, ""
		}

		slog.Info(
			"[approval] 工具调用审核拒绝",
			"tool_name", tc.Name,
			"source", result.Source,
			"reason", result.Reason,
		)
		return false, result.Reason
	}
}

// Reset 清空 "总是允许" 记忆缓存。
// 缓存不按会话隔离，入口 ( 如新会话开始 ) 可调用此方法避免跨会话放行。
func (m *ApprovalManager) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.remembered = make(map[string]struct{})
}
