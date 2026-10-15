package obs

import (
	"context"
	"fmt"

	"github.com/jrmarcco/goaw/internal/logging"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// tracerName OTel instrumentation scope 名，约定取模块路径。
const tracerName = "github.com/jrmarcco/goaw"

// tracer 返回当前全局 Provider 下的 tracer。
// 每次调用即时读取全局注册 ( 未装配时为 noop )，保证测试与装配可在运行期替换 Provider。
func tracer() trace.Tracer {
	return otel.Tracer(tracerName)
}

// otelSpanKind 把自研 SpanKind 映射为 OTel SpanKind。
func otelSpanKind(kind SpanKind) trace.SpanKind {
	switch kind {
	case SpanKindClient:
		return trace.SpanKindClient
	case SpanKindInternal:
		return trace.SpanKindInternal
	default:
		return trace.SpanKindInternal
	}
}

// otelAttribute 把单一属性值转换为 OTel 属性。
//
// 纪律:
// Span 属性值仅使用基本类型; 其余类型 ( 含命名字符串类型 ) 降级为字符串表示，
// 保证 OTLP 序列化无损。
func otelAttribute(key string, val any) attribute.KeyValue {
	switch v := val.(type) {
	case string:
		return attribute.String(key, v)
	case bool:
		return attribute.Bool(key, v)
	case int:
		return attribute.Int(key, v)
	case int64:
		return attribute.Int64(key, v)
	case float64:
		return attribute.Float64(key, v)
	default:
		return attribute.String(key, fmt.Sprintf("%v", v))
	}
}

// SetupOTel 装配 OTLP ( over HTTP ) 导出的全局 TracerProvider。
// exporter 自动读取 OTEL_EXPORTER_OTLP_* 标准环境变量族;
// 返回的 shutdown 必须在进程退出前调用，冲刷缓冲区中的已结束跨度。
func SetupOTel(ctx context.Context) (func(context.Context) error, error) {
	exporter, err := otlptracehttp.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create otlp trace exporter: %w", err)
	}

	provider := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exporter))
	otel.SetTracerProvider(provider)

	// SDK 内部错误 ( 导出失败等 ) 导向 slog，避免静默丢失。
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		logging.For("trace").Error("otel 内部错误", "error", err)
	}))

	return provider.Shutdown, nil
}
