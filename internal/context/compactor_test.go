package context

import (
	"math"
	"slices"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/jrmarcco/goaw/internal/schema"
)

const (
	testToolCallID1 = "t1"
	testToolCallID2 = "t2"
)

// newTestCompactor 窗口 1000 token、预留 100、保护区 2 条。
// 触发阈值 = 1000 * 0.8 - 100 = 700 token。
func newTestCompactor() *Compactor {
	return NewCompactor(1000, 100, 2)
}

func TestCompactPassthroughBelowWatermark(t *testing.T) {
	t.Parallel()

	c := newTestCompactor()

	msgs := []schema.Message{
		{Role: schema.RoleSystem, Content: strings.Repeat("a", 100)},
		{Role: schema.RoleUser, Content: "hello"},
	}

	got := c.Compact(msgs)

	if len(got) != len(msgs) {
		t.Fatalf("未超水位线不应增删消息, got %d msgs, want %d", len(got), len(msgs))
	}
	for i := range msgs {
		if got[i].Content != msgs[i].Content {
			t.Fatalf("未超水位线不应修改消息内容, idx=%d", i)
		}
	}
}

// TestObserveCalibratesTokensPerChar 验证 EWMA 校准回路:
// 真实消耗 / 放行字符量 作为样本，向初始系数做指数加权收敛。
func TestObserveCalibratesTokensPerChar(t *testing.T) {
	t.Parallel()

	c := newTestCompactor()

	msgs := []schema.Message{
		{Role: schema.RoleUser, Content: strings.Repeat("a", 1000)},
	}
	c.Compact(msgs) // sentChars = 1000
	c.Observe(400)  // 样本 = 0.4 token/char

	want := ewmaAlpha*0.4 + (1-ewmaAlpha)*defaultTokensPerChar
	if math.Abs(c.tokensPerChar-want) > 1e-9 {
		t.Fatalf("tokensPerChar = %f, want %f", c.tokensPerChar, want)
	}
	if c.watermark != 400 {
		t.Fatalf("watermark = %d, want 400", c.watermark)
	}
	if c.baselineChars != 1000 {
		t.Fatalf("baselineChars = %d, want 1000", c.baselineChars)
	}
}

// TestObserveIgnoresInvalidUsage 非正值 ( 如网关未回传 ) 不应污染校准状态。
func TestObserveIgnoresInvalidUsage(t *testing.T) {
	t.Parallel()

	c := newTestCompactor()
	c.Observe(0)
	c.Observe(-5)

	if c.watermark != 0 {
		t.Fatalf("非法样本不应刷新水位线, watermark = %d", c.watermark)
	}
	if c.tokensPerChar != defaultTokensPerChar {
		t.Fatalf("非法样本不应污染校准系数, tokensPerChar = %f", c.tokensPerChar)
	}
}

// TestCompactTriggersOnRealWatermark 核心场景:
// 字符数远低于旧的字符阈值，但真实 API 上报的 PromptTokens 越过水位线，
// 必须触发压缩 —— 这正是"真实消耗水位线"替代"字符阈值"的意义。
func TestCompactTriggersOnRealWatermark(t *testing.T) {
	t.Parallel()

	c := newTestCompactor()

	// 第一轮: 建立基准 ( 放行并记录字符量 )。
	base := []schema.Message{
		{Role: schema.RoleSystem, Content: strings.Repeat("s", 300)},
		{Role: schema.RoleUser, Content: "hi"},
	}
	c.Compact(base)
	c.Observe(750) // 真实水位 750 > 阈值 700。

	// 第二轮: 即使新增内容不多，水位 + 增量仍超标，应触发压缩。
	msgs := append(slices.Clone(base), schema.Message{
		Role:       schema.RoleUser,
		Content:    strings.Repeat("x", 500),
		ToolCallID: "tc-1",
	})
	got := c.Compact(msgs)

	// msgs 只有 3 条，保护区为最后 2 条，tc-1 的结果在保护区内会被截断。
	if !strings.Contains(got[2].Content, "内容过长") {
		t.Fatalf("超水位线的工具输出应被降级, got %q", got[2].Content)
	}
	if len(got[2].Content) >= len(msgs[2].Content) {
		t.Fatalf("截断后内容应显著变短, got %d bytes, orig %d bytes",
			len(got[2].Content), len(msgs[2].Content))
	}
}

// TestDegradeLayers 验证分层降级策略:
// System 直通、远期工具输出全量掩码、远期推理折叠、保护区只截断、ToolCalls 保持。
func TestDegradeLayers(t *testing.T) {
	t.Parallel()

	c := newTestCompactor()

	msgs := []schema.Message{
		{Role: schema.RoleSystem, Content: strings.Repeat("S", 400)},
		{Role: schema.RoleAssistant, Content: "", ToolCalls: []schema.ToolCall{
			{ID: testToolCallID1, Name: "bash", Args: []byte(`{"cmd":"ls -la"}`)},
		}},
		{Role: schema.RoleUser, Content: strings.Repeat("o", 800), ToolCallID: testToolCallID1},  // 远期工具输出
		{Role: schema.RoleAssistant, Content: strings.Repeat("思考", 200)},                         // 远期推理 ( 400 字节 )
		{Role: schema.RoleUser, Content: strings.Repeat("p", 3000), ToolCallID: testToolCallID2}, // 保护区: 超长工具输出
		{Role: schema.RoleAssistant, Content: "最终答复"},                                            // 保护区
	}

	// 直接测 degrade: maxKeep = 1000。
	got := c.degrade(msgs, 1000)

	if got[0].Content != msgs[0].Content {
		t.Fatal("System Prompt 必须原样保留")
	}
	if !strings.Contains(got[2].Content, "工具输出已被清理") {
		t.Fatalf("远期工具输出应被全量掩码, got %.40q", got[2].Content)
	}
	if got[3].Content != "...[早期的推理思考过程已折叠]..." {
		t.Fatalf("远期推理应被折叠, got %.40q", got[3].Content)
	}
	if !strings.Contains(got[4].Content, "内容过长") {
		t.Fatalf("保护区超长消息应被截断, got %.40q", got[4].Content)
	}
	if len(got[1].ToolCalls) != 1 || got[1].ToolCalls[0].ID != testToolCallID1 {
		t.Fatal("ToolCalls 是模型行动的证据，必须保持不动")
	}
	if got[5].Content != msgs[5].Content {
		t.Fatal("保护区内不超长的消息应保持原样")
	}
}

// TestCompactConvergesUnderPressure 极端场景:
// 水位逼近窗口上限时，多轮压缩应持续收紧保护区截断预算直到收敛。
func TestCompactConvergesUnderPressure(t *testing.T) {
	t.Parallel()

	c := newTestCompactor()

	base := []schema.Message{
		{Role: schema.RoleSystem, Content: strings.Repeat("s", 100)},
		{Role: schema.RoleUser, Content: strings.Repeat("u", 100)},
	}
	c.Compact(base)
	c.Observe(990) // 水位逼近窗口上限。

	// 保护区内的巨型工具输出。
	msgs := append(slices.Clone(base), schema.Message{
		Role:       schema.RoleUser,
		Content:    strings.Repeat("巨", 5000), // 15000 字节
		ToolCallID: "tc-1",
	})

	got := c.Compact(msgs)

	if len(got[2].Content) >= 15000 {
		t.Fatalf("多轮压缩后内容仍为全量: %d bytes", len(got[2].Content))
	}
	if !utf8.ValidString(got[2].Content) {
		t.Fatal("截断不应产生非法 UTF-8 序列")
	}
	// 多轮收紧后，最终内容应显著小于首轮预算 ( 1000 字节 )。
	if len(got[2].Content) > 1000 {
		t.Fatalf("截断预算应逐轮收紧, final = %d bytes", len(got[2].Content))
	}
}

// TestProjectionUsesWatermarkBaseline 验证增量估算:
// 水位 + ( 新字符 - 基准字符 ) * 校准系数。
func TestProjectionUsesWatermarkBaseline(t *testing.T) {
	t.Parallel()

	c := newTestCompactor()

	c.Compact([]schema.Message{{Role: schema.RoleUser, Content: strings.Repeat("a", 1000)}})
	c.Observe(500) // ratio = 0.47, watermark = 500, baseline = 1000

	// 新增 1000 chars → projected = 500 + 1000 * 0.47 = 970 > 700 触发压缩。
	chars := 2000
	got := c.projectTokensLocked(chars)
	want := int(float64(500) + float64(chars-1000)*c.tokensPerChar)
	if got != want {
		t.Fatalf("projectTokens = %d, want %d", got, want)
	}
}

func TestTruncateHelpersUTF8Safe(t *testing.T) {
	t.Parallel()

	s := strings.Repeat("你好世界", 100) // 1200 字节, 每字符 3 字节

	for _, n := range []int{1, 2, 3, 4, 7, 500, 501, 1199} {
		if p := truncatePrefix(s, n); !utf8.ValidString(p) {
			t.Fatalf("truncatePrefix(%d) 产生非法 UTF-8", n)
		}
		if sf := truncateSuffix(s, n); !utf8.ValidString(sf) {
			t.Fatalf("truncateSuffix(%d) 产生非法 UTF-8", n)
		}
	}

	if p := truncatePrefix(s, 4); len(p) != 3 {
		t.Fatalf("truncatePrefix 应回退到字符边界, got %d bytes", len(p))
	}
	if sf := truncateSuffix(s, 4); len(sf) != 3 {
		t.Fatalf("truncateSuffix 应对齐到字符边界, got %d bytes", len(sf))
	}
	if got := truncatePrefix("abc", 10); got != "abc" {
		t.Fatalf("短字符串应原样返回, got %q", got)
	}
}

// TestCompactorConcurrentAccess 并发烟测 ( 配合 -race )。
func TestCompactorConcurrentAccess(t *testing.T) {
	t.Parallel()

	c := newTestCompactor()

	msgs := []schema.Message{
		{Role: schema.RoleSystem, Content: strings.Repeat("s", 100)},
		{Role: schema.RoleUser, Content: strings.Repeat("x", 500), ToolCallID: testToolCallID1},
		{Role: schema.RoleAssistant, Content: strings.Repeat("r", 300)},
	}

	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				c.Compact(msgs)
				c.Observe(n*j + 1)
			}
		}(i)
	}
	wg.Wait()
}
