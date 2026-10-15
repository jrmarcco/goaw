package obs

import (
	"context"
	"errors"
	"testing"
	"time"
)

// endGuardSleep 保证两次 End 落在不同毫秒，使幂等断言不受毫秒精度影响。
const endGuardSleep = 2 * time.Millisecond

func TestStartSpanCascadesToParent(t *testing.T) {
	t.Parallel()

	parentCtx, parent := StartSpan(context.Background(), "parent")
	_, child := StartSpan(parentCtx, "child")

	if len(parent.Children) != 1 || parent.Children[0] != child {
		t.Fatalf("子 Span 应挂接到父 Span，实际父节点子数为 %d", len(parent.Children))
	}
}

func TestStartRootSpanDetachesAndRecascades(t *testing.T) {
	t.Parallel()

	parentCtx, parent := StartSpan(context.Background(), "outer.task")
	rootCtx, root := StartRootSpan(parentCtx, "agent.run")
	_, leaf := StartSpan(rootCtx, "turn")

	if len(parent.Children) != 0 {
		t.Fatalf("根 Span 不应挂接任何父节点，实际挂接 %d 个", len(parent.Children))
	}
	if len(root.Children) != 1 || root.Children[0] != leaf {
		t.Fatal("根之后的 Span 应级联到新根之下")
	}
}

func TestSpanFromContext(t *testing.T) {
	t.Parallel()

	if _, ok := SpanFromContext(context.Background()); ok {
		t.Fatal("普通 context 不应解析出 Span")
	}

	ctx, span := StartSpan(context.Background(), "leaf")
	if got, ok := SpanFromContext(ctx); !ok || got != span {
		t.Fatal("应解析出 context 中的当前 Span")
	}
}

func TestSpanEndIdempotent(t *testing.T) {
	t.Parallel()

	_, span := StartSpan(context.Background(), "leaf")
	span.End()
	first := span.EndedAt

	time.Sleep(endGuardSleep)
	span.End()

	if span.EndedAt != first {
		t.Fatalf("End 应幂等: 首次 %d，二次 %d", first, span.EndedAt)
	}
}

func TestSpanSetError(t *testing.T) {
	t.Parallel()

	_, span := StartSpan(context.Background(), "leaf")

	span.SetError(nil)
	if span.Error != "" {
		t.Fatal("nil 错误不应产生写入")
	}

	span.SetError(errors.New("boom"))
	if span.Error != "boom" {
		t.Fatalf("应记录错误信息，实际 %q", span.Error)
	}
}

func TestSpanKindOption(t *testing.T) {
	t.Parallel()

	_, internal := StartSpan(context.Background(), "leaf")
	if internal.Kind != SpanKindInternal {
		t.Fatalf("缺省种类应为 internal，实际 %q", internal.Kind)
	}

	_, client := StartSpan(context.Background(), "llm.generate", WithSpanKind(SpanKindClient))
	if client.Kind != SpanKindClient {
		t.Fatalf("应记录 client 种类，实际 %q", client.Kind)
	}
}

func TestRootSpanInheritsNoParentAttributes(t *testing.T) {
	t.Parallel()

	// 根 span 不复制父属性: 两棵树的数据边界清晰，互链仅靠约定属性。
	parentCtx, parent := StartSpan(context.Background(), "outer.task")
	parent.Attribute("outer", "yes")

	_, root := StartRootSpan(parentCtx, "agent.run")

	if _, exists := root.Attributes["outer"]; exists {
		t.Fatal("根 Span 不应继承父节点的属性")
	}
}
