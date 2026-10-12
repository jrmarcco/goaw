// Package obs 提供系统级运行观测: token 成本记账与预算拦截。
package obs

import (
	"log/slog"
	"sync"
	"time"

	"github.com/jrmarcco/goaw/internal/schema"
)

// CostSnapshot 是某一时刻成本账本的值拷贝 ( 监控/查询用 )。
// 维度分开输出:
// 预算拦截用合并总量 ( TotalTokens )，
// 对账与未来加权计费用缓存子集维度 ( CacheReadTokens / CacheCreationTokens )。
type CostSnapshot struct {
	Model               string
	Calls               int
	PromptTokens        int
	CompletionTokens    int
	CacheReadTokens     int
	CacheCreationTokens int
	TotalTokens         int           // Prompt + Completion 合并口径。
	TokenThreshold      int           // 阈值，小于等于 0 表示未设限。
	BusyDuration        time.Duration // 模型调用耗时累计 ( 不含工具执行 )。
}

// CostTracker 是进程级 token 成本记账器与预算闸门。
//
// 记账口径为合并总量 ( PromptTokens + CompletionTokens ):
// PromptTokens 本身已含缓存维度，且缓存读按全价计入，
// 阈值只会偏保守 ( 提前拦截 )，不会放过预算。
//
// 经结构化类型匹配满足 engine.CostObserver ( 方法签名须与接口完全一致 )，
// 本包不依赖 engine，与 tools.SubReporter / engine.Reporter 的解法同构。
//
// 并发契约: 多会话 ( 终端、飞书、SubAgent ) 的 Run 共享同一实例，全部方法持锁。
type CostTracker struct {
	mu sync.Mutex

	model          string
	tokenThreshold int

	calls               int
	promptTokens        int
	completionTokens    int
	cacheReadTokens     int
	cacheCreationTokens int
	busyDuration        time.Duration

	// warned 跨阈值瞬间只告警一次。
	warned bool
}

// NewCostTracker 创建成本记账器。
// model 仅作账本标签; tokenThreshold 小于等于 0 表示不设预算上限。
func NewCostTracker(model string, tokenThreshold int) *CostTracker {
	return &CostTracker{
		model:          model,
		tokenThreshold: tokenThreshold,
	}
}

// Observe 记账一次模型调用，engine 每次补全 ( think/act/final ) 后调用。
// phase 区分调用阶段，仅入日志。
func (t *CostTracker) Observe(usage schema.Usage, phase string, took time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.calls++
	t.promptTokens += usage.PromptTokens
	t.completionTokens += usage.CompletionTokens
	t.cacheReadTokens += usage.CacheReadTokens
	t.cacheCreationTokens += usage.CacheCreationTokens
	t.busyDuration += took

	slog.Debug(
		"[cost] 模型调用记账",
		"phase", phase,
		"model", t.model,
		"prompt_tokens", usage.PromptTokens,
		"completion_tokens", usage.CompletionTokens,
		"took", took,
	)

	if !t.warned && t.exceededLocked() {
		t.warned = true
		slog.Warn(
			"[cost] token 预算已耗尽，后续运行将被强制收尾",
			"model", t.model,
			"total_tokens", t.totalLocked(),
			"threshold", t.tokenThreshold,
		)
	}
}

// Exceeded 报告 token 预算是否已耗尽 ( 引擎在轮次边界消费，触发强制收尾 )。
func (t *CostTracker) Exceeded() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.exceededLocked()
}

// Snapshot 返回当前账本的值拷贝。
func (t *CostTracker) Snapshot() CostSnapshot {
	t.mu.Lock()
	defer t.mu.Unlock()

	return CostSnapshot{
		Model:               t.model,
		Calls:               t.calls,
		PromptTokens:        t.promptTokens,
		CompletionTokens:    t.completionTokens,
		CacheReadTokens:     t.cacheReadTokens,
		CacheCreationTokens: t.cacheCreationTokens,
		TotalTokens:         t.totalLocked(),
		TokenThreshold:      t.tokenThreshold,
		BusyDuration:        t.busyDuration,
	}
}

// exceededLocked 判定预算是否耗尽 ( 调用方持锁 )。
func (t *CostTracker) exceededLocked() bool {
	if t.tokenThreshold <= 0 {
		return false
	}
	return t.totalLocked() >= t.tokenThreshold
}

// totalLocked 合并口径总量 ( 调用方持锁 )。
func (t *CostTracker) totalLocked() int {
	return t.promptTokens + t.completionTokens
}
