package obs

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// swapTracerProvider 以录音 Provider 替换全局注册并随测试恢复。
// otel 全局注册内部同步，替换期间其他并行测试的 span 落入录音器不影响其断言。
func swapTracerProvider(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()

	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))

	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
	})

	return recorder
}

// findEnded 按名称查找已结束的镜像 Span。
func findEnded(t *testing.T, spans []sdktrace.ReadOnlySpan, name string) sdktrace.ReadOnlySpan {
	t.Helper()

	for _, span := range spans {
		if span.Name() == name {
			return span
		}
	}
	t.Fatalf("镜像中未找到名为 %q 的已结束 Span", name)
	return nil
}

func TestDualWriteMirrorsSpanLifecycle(t *testing.T) {
	t.Parallel()

	recorder := swapTracerProvider(t)

	_, span := StartSpan(context.Background(), "tool.execute", WithSpanKind(SpanKindClient))
	span.Attribute("tool_name", "bash")
	span.Attribute("retries", 3)
	span.SetError(errors.New("boom"))
	span.End()

	ended := recorder.Ended()
	if len(ended) != 1 {
		t.Fatalf("应镜像出 1 个 OTel Span，实际 %d", len(ended))
	}
	got := ended[0]

	if got.Name() != "tool.execute" {
		t.Fatalf("镜像名称应为 tool.execute，实际 %q", got.Name())
	}
	if got.SpanKind() != trace.SpanKindClient {
		t.Fatalf("镜像种类应为 client，实际 %v", got.SpanKind())
	}
	if got.Status().Code != codes.Error {
		t.Fatalf("镜像状态应为 Error，实际 %v", got.Status().Code)
	}

	attrs := make(map[string]string, len(got.Attributes()))
	for _, kv := range got.Attributes() {
		attrs[string(kv.Key)] = kv.Value.String()
	}
	if attrs["tool_name"] != "bash" {
		t.Fatalf("镜像属性 tool_name 应为 bash，实际 %q", attrs["tool_name"])
	}
	if attrs["retries"] != "3" {
		t.Fatalf("镜像属性 retries 应为 3，实际 %q", attrs["retries"])
	}

	// SetError 镜像为 exception 事件。
	if len(got.Events()) == 0 || got.Events()[0].Name != "exception" {
		t.Fatalf("SetError 应镜像 RecordError 事件，实际事件数 %d", len(got.Events()))
	}
}

func TestDualWriteRootStartsNewTraceWithLink(t *testing.T) {
	t.Parallel()

	recorder := swapTracerProvider(t)

	parentCtx, parent := StartSpan(context.Background(), "outer.task")
	_, root := StartRootSpan(parentCtx, "agent.run")
	root.End()
	parent.End()

	ended := recorder.Ended()
	if len(ended) != 2 {
		t.Fatalf("应镜像出 2 个 OTel Span，实际 %d", len(ended))
	}

	mirrorRoot := findEnded(t, ended, "agent.run")
	if mirrorRoot.Parent().IsValid() {
		t.Fatal("根镜像不应携带父 SpanContext ( 新 Trace 开根 )")
	}
	if len(mirrorRoot.Links()) != 1 {
		t.Fatalf("根镜像应保留 1 条指向父的 Link，实际 %d", len(mirrorRoot.Links()))
	}

	mirrorParent := findEnded(t, ended, "outer.task")
	if mirrorRoot.Links()[0].SpanContext.SpanID() != mirrorParent.SpanContext().SpanID() {
		t.Fatal("Link 应指向父镜像的 SpanContext")
	}
}

func TestOtelAttributePrimitives(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		val  any
	}{
		{name: "string", val: "text"},
		{name: "bool", val: true},
		{name: "int", val: 3},
		{name: "float", val: 1.5},
		{name: "named type fallback", val: time.Hour},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			want := fmt.Sprintf("%v", tc.val)
			if got := otelAttribute("k", tc.val).Value.String(); got != want {
				t.Fatalf("属性 %q 的镜像表示应为 %q，实际 %q", tc.name, want, got)
			}
		})
	}
}
