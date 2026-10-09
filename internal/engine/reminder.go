package engine

import (
	_ "embed"
	"fmt"
	"log/slog"

	"github.com/cespare/xxhash/v2"
	"github.com/jrmarcco/goaw/internal/schema"
	"github.com/jrmarcco/jit/xbean/option"
)

const defaultRemindThreshold = 3

//go:embed prompts/reminder.md
var reminderPrompt string

// ReminderInjector 负责运行时监控上下文，并在模型陷入死循环时注入打断信息。
// 以轮次为计数单位:
//
//	同一指纹连续 N 个轮次失败才触发，其他工具的成功不清空在途指纹的计数 ( 交错型死循环仍可检出 )。
//	实例挂载在 Session 上与 Compactor 同生命周期，计数跨 turn 存活、Run 开始时重置;
//	仅在 Run 的串行路径 ( execToolCalls 的 Wait 之后 ) 上访问，自身无锁。
type ReminderInjector struct {
	threshold int

	// 记录按指纹累计的连续失败轮次:
	//  - key: hash(ToolName + NUL + Args)
	//  - val: 连续失败轮次 ( 同一指纹一轮内只计一次 )
	// 本轮未失败的指纹 ( 成功或未调用 ) 即从表中清除。
	consecutiveFailures map[uint64]int
}

func ReminderInjectorWithThreshold(threshold int) option.Opt[ReminderInjector] {
	return func(ri *ReminderInjector) {
		ri.threshold = threshold
	}
}

func NewReminderInjector(opts ...option.Opt[ReminderInjector]) *ReminderInjector {
	i := &ReminderInjector{
		threshold:           defaultRemindThreshold,
		consecutiveFailures: make(map[uint64]int),
	}

	option.Apply(i, opts...)
	return i
}

// CheckAndInject 以轮次为单位分析整个批次的执行结果，
// 并决定是否要在上下文尾部追加 Reminder。
// 返回的 scheme.Message 将作为最新的用户输入，
// 强制提升阅读优先级。
func (i *ReminderInjector) CheckAndInject(
	turnCalls []schema.ToolCall,
	turnResults []schema.ToolCallResult,
) *schema.Message {
	// 统计本轮失败的指纹，同一指纹一轮内只计一次。
	failed := make(map[uint64]string, len(turnCalls))
	for idx, tc := range turnCalls {
		if turnResults[idx].IsError {
			failed[i.genFingerprint(tc.Name, tc.Args)] = tc.Name
		}
	}

	// 本轮未失败的指纹 ( 成功或未调用 ) 视为行为已改变，清零其计数。
	for fingerprint := range i.consecutiveFailures {
		if _, ok := failed[fingerprint]; !ok {
			delete(i.consecutiveFailures, fingerprint)
		}
	}

	// 累计连续失败轮次，追踪计数最高的指纹 ( 并列时任取其一 )。
	var (
		worstName string
		worstCnt  int
	)
	for fingerprint, toolName := range failed {
		i.consecutiveFailures[fingerprint]++
		failCnt := i.consecutiveFailures[fingerprint]

		slog.Warn("[reminder] 监控到工具执行失败", "tool_name", toolName, "consecutive_fail_turns", failCnt)

		if failCnt >= i.threshold && failCnt > worstCnt {
			worstCnt = failCnt
			worstName = toolName
		}
	}

	// 连续失败轮次达到阈值触发打断机制。
	if worstCnt == 0 {
		return nil
	}

	slog.Warn("[reminder] 工具触发打断机制。", "tool_name", worstName, "threshold", i.threshold)

	return &schema.Message{
		// 必须使用 RoleUser，以保证下一次 API 请求时拥有最高的近因效应权重。
		Role:    schema.RoleUser,
		Content: fmt.Sprintf(reminderPrompt, worstCnt, worstName),
	}
}

// Reset 清空全部失败计数。
// 计数语义是 "连续轮次" 不跨 Run:
// 新一次运行代表新的任务上下文，残留计数会让 "连续" 措辞失真。
func (i *ReminderInjector) Reset() {
	i.consecutiveFailures = make(map[uint64]int)
}

// genFingerprint 生成 ( toolName, args ) 的循环检测指纹。
// xxhash 是纯内存哈希，写入永不失败，无需错误返回;
// NUL 分隔符消除 name/args 的拼接边界歧义 ( JSON 负载中不会出现裸 NUL )。
func (i *ReminderInjector) genFingerprint(toolName string, args []byte) uint64 {
	h := xxhash.New()
	_, _ = h.WriteString(toolName)
	_, _ = h.WriteString("\x00")
	_, _ = h.Write(args)
	return h.Sum64()
}
