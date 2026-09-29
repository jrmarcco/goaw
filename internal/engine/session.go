package engine

import (
	"sync"
	"sync/atomic"
	"time"

	icontext "github.com/jrmarcco/goaw/internal/context"
	"github.com/jrmarcco/goaw/internal/schema"
)

const (
	// defaultContextWindow Compactor 使用的上下文窗口缺省值 ( token )。
	defaultContextWindow = 128_000

	// defaultReserveTokens 为模型单次补全预留的输出空间 ( token )。
	defaultReserveTokens = 8_192

	// defaultRetainLastMsg Working Memory 保护区的消息条数。
	defaultRetainLastMsg = 20
)

// Session 代表一次人机交互过程。
// 是 Agent 运行所需环境与状态的唯一载体:
//   - Workspace: 工具执行范围与 System Prompt 构建所依据的工作区。
//   - history: 会话的完整上下文历史，跨多次 Run 持久累积。
//   - compactor: 会话级自适应压缩器，Token 水位线与校准系数跨 Run 存活。
type Session struct {
	mu sync.RWMutex

	ID        string
	Workspace string

	CreatedAt time.Time
	UpdatedAt time.Time

	history []schema.Message

	compactor *icontext.Compactor

	// running 会话级运行互斥标志。
	// 同一会话上两次并发 Run 会交错写入历史，破坏消息序列的连续性，
	// 大模型 API 会直接拒绝这种残缺序列，因此必须拒绝并发运行。
	running atomic.Bool
}

func NewSession(id, workspace string) *Session {
	now := time.Now()
	return &Session{
		ID:        id,
		Workspace: workspace,

		CreatedAt: now,
		UpdatedAt: now,

		history:   make([]schema.Message, 0),
		compactor: icontext.NewCompactor(defaultContextWindow, defaultReserveTokens, defaultRetainLastMsg),
	}
}

// TryStartRun 尝试将会话标记为运行中。
// 返回 false 表示该会话上已有一次 Agent 运行在进行中。
func (s *Session) TryStartRun() bool {
	return s.running.CompareAndSwap(false, true)
}

// EndRun 解除会话的运行状态，允许下一次 Run 接管。
func (s *Session) EndRun() {
	s.running.Store(false)
}

func (s *Session) Append(msgs ...schema.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.history = append(s.history, msgs...)
	s.UpdatedAt = time.Now()

	// TODO: 保存历史消息到持久化存储 ( 建议抽取 SessionStorage 接口 )。
}

// GetWorkingMemory 获取工作记忆 ( Agent Harness 的核心 )。
// 返回最近的 N 条消息 ( Agent 的短期工作记忆 )，limit 小于等于 0 表示不限制条数。
//
// Token 维度的上下文压力不在这里设防，统一交由 Compactor 基于真实水位线处理，
// 避免本地估算的硬截断丢弃 Compactor 本可仅掩码的内容。
func (s *Session) GetWorkingMemory(limit int) []schema.Message {
	s.mu.RLock()
	defer s.mu.RUnlock()

	historyCnt := len(s.history)

	// 按条数截取，确定回溯的起始下标。
	start := 0
	if limit > 0 && historyCnt > limit {
		start = historyCnt - limit
	}

	res := make([]schema.Message, historyCnt-start)
	copy(res, s.history[start:])

	// 大模型 API 对历史消息的连续性有要求。
	// 如果返回消息的第一条恰好是一个 ToolCallResult ( RoleUser 且含有 ToolCallID )，
	// 但发出这个请求的 ToolCall 被截断丢弃了，
	// 那么大模型 API 会直接报错 ( 400 Bad Request )。
	// 因此必须强制舍弃 ToolCallResult 消息，顺延到下一条正常的 User/Assistant 消息。
	for len(res) > 0 {
		if res[0].Role != schema.RoleUser || res[0].ToolCallID == "" {
			break
		}
		res = res[1:]
	}
	return res
}

type SessionManager struct {
	mu       sync.RWMutex
	sessions map[string]*Session
}

func NewSessionManager() *SessionManager {
	return &SessionManager{
		sessions: make(map[string]*Session),
	}
}

// Get 获取一个会话。
// 如果会话不存在，则创建一个新的会话。
func (m *SessionManager) Get(id, workspace string) *Session {
	m.mu.Lock()
	defer m.mu.Unlock()

	if sess, ok := m.sessions[id]; ok {
		return sess
	}

	sess := NewSession(id, workspace)
	m.sessions[id] = sess
	return sess
}
