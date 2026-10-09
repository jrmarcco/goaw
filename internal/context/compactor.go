package context

import (
	"fmt"
	"log/slog"
	"sync"
	"unicode/utf8"

	"github.com/jrmarcco/goaw/internal/schema"
)

const (
	// defaultTokensPerChar 初始校准系数。
	// 首轮请求尚无真实反馈时的保守估计 ( 中英混排场景 )，第一次 Observe 之后即被真实数据修正。
	defaultTokensPerChar = 0.5

	// ewmaAlpha 校准平滑系数。
	// 偏向近期样本，同时滤掉 think / act 两次调用间工具定义开销带来的抖动。
	ewmaAlpha = 0.3

	// maxCompactRounds 单次 Compact 的最大压缩轮数。
	// 每轮之后若仍超水位，收紧保护区截断预算再来一轮，直到达标或轮数耗尽。
	maxCompactRounds = 4

	// farHistoryMaskChars 远期历史单条消息超过该字符数才值得掩码。
	farHistoryMaskChars = 200

	// defaultTriggerRatio 缺省触发水位线比例 ( 0, 1 )。
	defaultTriggerRatio = 0.8

	// minTruncateKeep 保护区单条消息截断预算的收紧下限 ( 字符 )。
	// 再减半已省不出多少空间，宁可保住头尾上下文的可读性。
	minTruncateKeep = 250
)

// Compactor 是基于真实 API Token 消耗水位线的自适应上下文压缩器。
//
// 传统的固定字符阈值拦截误差极大:
//
//	同样的字符数在不同语言、不同模型分词器下折算出的 Token 可以相差数倍，
//	而且 System Prompt、工具定义 Schema 等隐性消耗，本地字符统计根本覆盖不到。
//
// 本实现把度量换成两级结构:
//   - 水位线:
//     每次 API 响应回传的 Usage.PromptTokens，是含全部隐性消耗的真实值，由 Observe 持续刷新;
//   - 增量估算:
//     水位线之后新增的消息按校准系数 ( tokens/char ) 折算。
//     该系数用每次真实消耗做指数加权平均 ( EWMA ) 持续逼近当前模型的真实分词比率，即"自适应"的核心。
//
// 预估消耗超过 ContextWindow * TriggerRatio - ReserveTokens 即触发压缩。
type Compactor struct {
	mu sync.Mutex

	// ContextWindow 模型上下文窗口大小 ( token )。
	ContextWindow int

	// TriggerRatio 触发压缩的水位线比例 ( 0, 1 )。
	TriggerRatio float64

	// ReserveTokens 为模型单次补全预留的输出空间 ( token )，
	// 与请求参数的 max_tokens 对齐，防止"输入 + 输出"合起来撑爆窗口。
	ReserveTokens int

	// RetainLastMsg 是 Working Memory 保护区 ( 最近的 n 条消息 )。
	// 保护区内的消息不做 Masking，只做局部截断。
	RetainLastMsg int

	// watermark 最近一次 API 上报的真实 PromptTokens 水位。
	watermark int

	// baselineChars 产生 watermark 的那次请求的字符量，增量估算的基准。
	baselineChars int

	// sentChars 最近一次 Compact 放行的字符量，供下一次 Observe 校准。
	sentChars int

	// tokensPerChar 校准系数: 每字符折算的真实 token 数。
	tokensPerChar float64
}

// NewCompactor 创建自适应压缩器。
func NewCompactor(contextWindow, reserveTokens, retainLastMsg int) *Compactor {
	return &Compactor{
		ContextWindow: contextWindow,
		TriggerRatio:  defaultTriggerRatio,
		ReserveTokens: reserveTokens,
		RetainLastMsg: retainLastMsg,
		tokensPerChar: defaultTokensPerChar,
	}
}

// Compact 接收准备发送给大模型的消息数组并预估 Token 消耗。
// 低于水位线时直接放行；超过则执行多轮降级压缩:
//   - System Prompt 直接保留 ( 最高优先级 )。
//   - 远期历史区: 工具输出全量掩码 ( Masking )、推理过程折叠。
//   - 短期保护区: 超长消息截断保留头尾 ( Truncation )，预算逐轮收紧。
//
// msg.ToolCalls 是模型行动的证据，任何情况下保持不动。
func (c *Compactor) Compact(msgs []schema.Message) []schema.Message {
	c.mu.Lock()
	defer c.mu.Unlock()

	threshold := c.triggerThreshold()

	chars := estimateLength(msgs)
	c.sentChars = chars

	if projected := c.projectTokensLocked(chars); projected < threshold {
		return msgs
	}

	slog.Warn(
		"[compactor] 上下文水位触及阈值，触发自适应压缩...",
		"projected_tokens", c.projectTokensLocked(chars),
		"watermark", c.watermark,
		"threshold", threshold,
		"tokens_per_char", c.tokensPerChar,
		"chars", chars,
	)

	compacted := msgs
	maxKeep := 1000 // 保护区单条消息的截断预算 ( 保留头尾各半 )，逐轮收紧。
	for round := 1; round <= maxCompactRounds; round++ {
		compacted = c.degrade(compacted, maxKeep)

		chars = estimateLength(compacted)
		if c.projectTokensLocked(chars) < threshold {
			break
		}
		if maxKeep > minTruncateKeep {
			maxKeep /= 2
		}
	}

	c.sentChars = chars

	slog.Debug(
		"[compactor] 压缩完成",
		"after_tokens", c.projectTokensLocked(chars),
		"after_chars", chars,
	)

	return compacted
}

// Observe 消费一次真实 API 响应的 Usage.PromptTokens。
//
// 这是整个自适应机制的反馈回路:
//
//	刷新水位线，并用 ( 真实 token 消耗 / 最近放行的字符量 ) 校准换算系数。
//	每次模型调用返回后都必须调用。
//
// 说明:
//
//	act 请求的工具定义开销会被一并计入样本，使系数略微偏高，
//	这符合 "宁可高估也不能低估" 的原则，EWMA 会自动抹平该系统性偏差。
func (c *Compactor) Observe(promptTokens int) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if promptTokens <= 0 {
		return
	}

	if c.sentChars > 0 {
		sample := float64(promptTokens) / float64(c.sentChars)
		c.tokensPerChar = ewmaAlpha*sample + (1-ewmaAlpha)*c.tokensPerChar
	}

	c.watermark = promptTokens
	c.baselineChars = c.sentChars
}

// Watermark 返回最近一次 API 上报的真实 PromptTokens ( 监控/调试用 )。
func (c *Compactor) Watermark() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.watermark
}

// triggerThreshold 计算触发压缩的 token 水位线。
func (c *Compactor) triggerThreshold() int {
	return max(int(float64(c.ContextWindow)*c.TriggerRatio)-c.ReserveTokens, 1)
}

// projectTokensLocked 估算当前这组消息发送出去会消耗多少 Prompt Token。
// 以最近一次真实水位为基准，其后的增量 ( 或压缩减量 ) 按校准系数折算，
// 并与纯估算值取 max —— 宁可高估也不能低估，否则会撑爆上下文窗口。
func (c *Compactor) projectTokensLocked(chars int) int {
	estimated := int(float64(chars) * c.tokensPerChar)
	if c.watermark <= 0 {
		// 尚无真实反馈，完全依赖初始校准系数。
		return estimated
	}

	projected := c.watermark + int(float64(chars-c.baselineChars)*c.tokensPerChar)
	if projected < estimated {
		return estimated
	}
	return projected
}

// degrade 对消息数组执行一轮降级压缩。
func (c *Compactor) degrade(msgs []schema.Message, maxKeep int) []schema.Message {
	compacted := make([]schema.Message, 0, len(msgs))

	msgCnt := len(msgs)
	protectStartIdx := max(msgCnt-c.RetainLastMsg, 0)

	for i, msg := range msgs {
		// System Prompt 直接保留 ( 最高优先级 )。
		if msg.Role == schema.RoleSystem {
			compacted = append(compacted, msg)
			continue
		}

		// 拷贝并修改。
		// 防止并发环境中直接修改导致底层数据结构被污染。
		copied := msg
		inWorkingMemory := i >= protectStartIdx

		// 双重降级。
		if msg.Role == schema.RoleUser && msg.ToolCallID != "" {
			// 对于工具的返回结果 ( Observation / ToolResult )。
			if !inWorkingMemory {
				// 第一重：远期历史执行 Full Masking。
				if len(msg.Content) > farHistoryMaskChars {
					copied.Content = fmt.Sprintf(
						"...[为节省上下文，早期的工具输出已被清理。原始长度: %d 字节]...",
						len(msg.Content),
					)
				}
			} else if len(msg.Content) > maxKeep {
				// 第二重：对短期记忆单条内容过大的消息，分别保留头尾。
				//nolint:mnd // 不需要为 /2 增加常量。
				copied.Content = fmt.Sprintf(
					"%s\n\n...[内容过长，截断中间 %d 字节]...\n\n%s",
					truncatePrefix(msg.Content, maxKeep/2),
					len(msg.Content)-maxKeep,
					truncateSuffix(msg.Content, maxKeep/2),
				)
			}
		} else if msg.Role == schema.RoleAssistant && msg.Content != "" {
			// 对于大模型的冗长推理对话 ( Thinking Trace ) 进行折叠。
			if !inWorkingMemory && len(msg.Content) > farHistoryMaskChars {
				copied.Content = "...[早期的推理思考过程已折叠]..."
			}
		}

		// msg.ToolCalls 是模型行动的证据，保持不动。
		compacted = append(compacted, copied)
	}

	return compacted
}

// estimateLength 粗略计算当前上下文的总字符长度。
// 在本实现中它只作为校准系数的分母，真正的度量是 Observe 回传的 Token 水位线。
func estimateLength(msgs []schema.Message) int {
	length := 0
	for _, msg := range msgs {
		length += len(msg.Content)
		for _, tc := range msg.ToolCalls {
			length += len(tc.Name) + len(tc.Args)
		}
	}
	return length
}

// truncatePrefix 截取字符串前 n 字节，回退到字符边界，避免切断多字节字符。
func truncatePrefix(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// truncateSuffix 截取字符串后 n 字节，起点对齐到字符边界。
func truncateSuffix(s string, n int) string {
	if len(s) <= n {
		return s
	}
	start := len(s) - n
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	return s[start:]
}
