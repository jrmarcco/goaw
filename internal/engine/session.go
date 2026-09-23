package engine

import (
	"sync"
	"time"

	"github.com/jrmarcco/goaw/internal/schema"
)

// Session 代表一次人机交互过程。
// 负责维护会话的完整上下文历史。
type Session struct {
	mu sync.RWMutex

	id        string
	workspace string

	history []schema.Message

	createdAt time.Time
	updatedAt time.Time
}

func NewSession(id, workspace string) *Session {
	now := time.Now()
	return &Session{
		id:        id,
		workspace: workspace,

		history: make([]schema.Message, 0),

		createdAt: now,
		updatedAt: now,
	}
}

func (s *Session) Append(msgs ...schema.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.history = append(s.history, msgs...)
	s.updatedAt = time.Now()

	// TODO: 保存历史消息到持久化存储 ( 建议抽取 SessionStorage 接口 )。
}

// GetWorkingMemory 获取工作记忆 ( Agent Harness 的核心 )。
// 返回最近的 N 条消息 ( Agent 的短期工作记忆 )。
// 当 limit 小于等于 0 时，返回所有消息。
func (s *Session) GetWorkingMemory(limit int) []schema.Message {
	s.mu.RLock()
	defer s.mu.RUnlock()

	historyCnt := len(s.history)
	if historyCnt <= limit || limit <= 0 {
		res := make([]schema.Message, historyCnt)
		copy(res, s.history)
		return res
	}

	res := make([]schema.Message, limit)
	copy(res, s.history[historyCnt-limit:])

	// 大模型 API 对历史消息的连续性有要求。
	// 如果返回消息的第一条桥好是一个 ToolCallResult ( RoleUser 且含有 ToolCallID )，
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

// GetSession 获取一个会话。
// 如果会话不存在，则创建一个新的会话。
func (m *SessionManager) GetSession(id, workspace string) *Session {
	m.mu.Lock()
	defer m.mu.Unlock()

	if sess, ok := m.sessions[id]; ok {
		return sess
	}

	sess := NewSession(id, workspace)
	m.sessions[id] = sess
	return sess
}
