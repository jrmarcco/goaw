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
//   - PlanMode: 会话运行模式，决定 System Prompt 是否注入长程任务规范。
//   - history: 会话的完整上下文历史，跨多次 Run 持久累积。
//   - compactor: 会话级自适应压缩器，Token 水位线与校准系数跨 Run 存活。
type Session struct {
	mu sync.RWMutex

	ID        string
	Workspace string
	PlanMode  bool

	CreatedAt time.Time
	UpdatedAt time.Time

	history []schema.Message

	compactor *icontext.Compactor

	// running 会话级运行互斥标志。
	// 同一会话上两次并发 Run 会交错写入历史，破坏消息序列的连续性，
	// 大模型 API 会直接拒绝这种残缺序列，因此必须拒绝并发运行。
	running atomic.Bool
}

func NewSession(id, workspace string, planMode bool) *Session {
	now := time.Now()
	return &Session{
		ID:        id,
		Workspace: workspace,
		PlanMode:  planMode,

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
// 返回最近的 limit 条消息 ( Agent 的短期工作记忆 )，limit 小于等于 0 表示不限制条数。
//
// 大模型 API 对消息序列的合法性有硬约束:
//   - ToolCallResult 必须紧跟发出对应 ToolCall 的 Assistant 消息，截断起点落在
//     两者之间会造出"孤儿结果"，API 直接 400 Bad Request;
//   - Anthropic 系 API 还要求对话以 user 角色开头，起点落在 Assistant 消息上同样 400。
//
// 因此截断起点向前回退 ( 而不是向后丢弃 )，直到落在一条普通 User 消息上，
// 补回 ToolCall 的父消息与前置的用户消息。序列完整性优先于条数精度，
// 返回条数可能超过 limit ( 上界为起点到上一条普通 User 消息的距离 )。
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

	// 起点向前回退到普通 User 消息:
	//   - 落在 ToolCallResult 上 → 回退补回发出 ToolCall 的父消息，孤儿自然消除;
	//   - 落在 Assistant 消息上 → 继续回退，开头必为 user 角色;
	//   - 窗口内全是 ToolCallResult → 一路回退到 Run 的开场用户消息，不会返回空窗口。
	// history[0] 由 Run 流程保证是普通用户消息，循环必然终止;
	// limit 小于等于 0 时 start 恒为 0，循环零开销。
	for start > 0 && !isPlainUserMessage(s.history[start]) {
		start--
	}

	res := make([]schema.Message, historyCnt-start)
	copy(res, s.history[start:])
	return res
}

// isPlainUserMessage 判断是否为普通 User 消息。
// ToolCallResult ( RoleUser 且含 ToolCallID ) 不算:
// 它必须跟在对应 ToolCall 之后，不能充当合法的序列开头。
func isPlainUserMessage(msg schema.Message) bool {
	return msg.Role == schema.RoleUser && msg.ToolCallID == ""
}

// SessionManager 进程级会话注册表。
// 保证会话标识到 Session 实例的唯一映射:
//
//	同一 ID 在任意入口 ( 飞书机器人、终端 ) 都命中同一个会话，跨入口共享上下文。
//
// 由组合根 ( main ) 创建并注入各入口，不归任何单一入口私有;
// 当前为纯内存实现，会话只增不减，淘汰与持久化待 SessionStorage 落地。
type SessionManager struct {
	mu       sync.RWMutex
	sessions map[string]*Session
}

// NewSessionManager 创建一个空的会话管理器。
func NewSessionManager() *SessionManager {
	return &SessionManager{
		sessions: make(map[string]*Session),
	}
}

// Get 获取一个会话 ( get-or-create )。
// 如果会话不存在，则按给定环境创建一个新的会话。
//
// workspace 与 planMode 仅在首次创建时生效:
// 会话已存在时直接返回既有实例，本次传入的环境参数被静默忽略，
// 会话环境以第一次调用为准。
func (m *SessionManager) Get(id, workspace string, planMode bool) *Session {
	m.mu.Lock()
	defer m.mu.Unlock()

	if sess, ok := m.sessions[id]; ok {
		return sess
	}

	sess := NewSession(id, workspace, planMode)
	m.sessions[id] = sess
	return sess
}
