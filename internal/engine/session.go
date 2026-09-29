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

	ID        string
	Workspace string

	CreatedAt time.Time
	UpdatedAt time.Time

	history []schema.Message
}

func NewSession(id, workspace string) *Session {
	now := time.Now()
	return &Session{
		ID:        id,
		Workspace: workspace,

		CreatedAt: now,
		UpdatedAt: now,

		history: make([]schema.Message, 0),
	}
}

func (s *Session) Append(msgs ...schema.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.history = append(s.history, msgs...)
	s.UpdatedAt = time.Now()

	// TODO: 保存历史消息到持久化存储 ( 建议抽取 SessionStorage 接口 )。
}

// GetWorkingMemory 获取工作记忆 ( Agent Harness 的核心 )。
// 返回最近的 N 条消息 ( Agent 的短期工作记忆 )。
// 双维度截取，从最新消息向前回溯，任一维度触界即停止:
//   - limit: 最多返回的消息条数，小于等于 0 表示不限制条数。
//   - maxTokens: 返回消息的预估 Token 总量上限，小于等于 0 表示不限制 Token。
//
// 无论 Token 预算多小，至少保留最后一条消息，避免工作记忆被截空。
func (s *Session) GetWorkingMemory(limit, maxTokens int) []schema.Message {
	s.mu.RLock()
	defer s.mu.RUnlock()

	historyCnt := len(s.history)

	// 第一维度: 按条数截取，确定回溯的起始下标。
	start := 0
	if limit > 0 && historyCnt > limit {
		start = historyCnt - limit
	}

	// 第二维度: 从最新消息向前累加预估 Token，继续收缩起始下标。
	if maxTokens > 0 {
		used := 0
		for i := historyCnt - 1; i >= start; i-- {
			used += estimateMessageTokens(s.history[i])
			if used > maxTokens && i < historyCnt-1 {
				// 这条消息放不下了，连同更早的消息一起截断，
				// 但 i == historyCnt-1 ( 最后一条 ) 时必须保留。
				start = i + 1
				break
			}
		}
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

// messageOverheadTokens 是单条消息的固定 Token 开销 ( 角色、结构分隔符等 )。
// 参考主流大模型 API 的经验值 ( OpenAI cookbook 约 4 tokens / message )。
const messageOverheadTokens = 4

// estimateMessageTokens 估算单条消息的 Token 数。
// 除了正文内容，工具调用的名字和参数也会被发送给大模型，必须一并计入。
func estimateMessageTokens(msg schema.Message) int {
	n := messageOverheadTokens + estimateTokens(msg.Content)
	for _, tc := range msg.ToolCalls {
		n += estimateTokens(tc.Name) + estimateTokens(string(tc.Args))
	}
	return n
}

// estimateTokens 基于字符总数启发式地估算 Token 数。
// 没有精确分词器时的通用近似 ( 宁可高估也不能低估，否则会撑爆上下文窗口 ):
//   - ASCII 字符约 4 个折算 1 token ( 英文单词平均长度约 4~5 )。
//   - 非 ASCII 字符 ( 如中日韩文字 ) 保守按 1 字符 1 token 估算。
func estimateTokens(text string) int {
	if text == "" {
		return 0
	}

	const asciiThreshold = 128
	asciiCnt, otherCnt := 0, 0
	for _, r := range text {
		if r < asciiThreshold {
			asciiCnt++
		} else {
			otherCnt++
		}
	}
	return asciiCnt/4 + otherCnt
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
