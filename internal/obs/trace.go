package obs

import (
	"context"
	"sync"
	"time"

	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/jrmarcco/goaw/internal/option"
)

// SpanKind 是跨度的种类标记 ( 映射到 OTel 的 SpanKind，见 otelSpanKind )。
// 自定义类型避免调用方直接依赖 OTel。
type SpanKind string

const (
	// SpanKindInternal 进程内操作 ( 缺省 )。
	SpanKindInternal SpanKind = "internal"

	// SpanKindClient 出站调用 ( 如对模型 API 的请求 )。
	SpanKindClient SpanKind = "client"
)

// SpanOpt 是创建 Span 的可选项 ( 基于 option.Opt，底层配置结构不导出 )。
type SpanOpt = option.Opt[spanOptions]

type spanOptions struct {
	kind SpanKind
}

// WithSpanKind 标记跨度种类 ( 缺省 SpanKindInternal )。
func WithSpanKind(kind SpanKind) SpanOpt {
	return func(opts *spanOptions) {
		opts.kind = kind
	}
}

// traceCtxKey 是 Span 在 context 中的键类型。
// 独立的私有结构体类型，保证不与其他包注入的 context 值冲突。
type traceCtxKey struct{}

// Span 代表链路追踪总的一个时间跨度和操作节点。
//
// 双写模型:
// 本结构是自研追踪树的节点 ( 树形结构、可整体 JSON 导出 )，
// 同时每个节点镜像持有一个 OTel Span ( Start/End/SetError/Attribute ) 同步写入两侧;
// OTel Provider 未装配时镜像为 noop，行为与纯自研一致。
type Span struct {
	mu sync.Mutex

	Name string `json:"name"`

	// Kind 跨度种类 ( 见 SpanKind )。
	Kind SpanKind `json:"kind,omitempty"`

	StartedAt  int64 `json:"startedAt"`
	EndedAt    int64 `json:"endedAt"`
	DurationMs int64 `json:"durationMs"`

	// Error 记录跨度内的失败信号 ( 由 SetError 写入 )，空值表示成功。
	Error string `json:"error,omitempty"`

	Attributes map[string]any `json:"attributes,omitempty"`
	Children   []*Span        `json:"children,omitempty"`

	// otelSpan 镜像到 OpenTelemetry 的跨度。
	otelSpan trace.Span

	// ended 防重标记: End 幂等，重复结束不覆盖首次结果。
	ended bool
}

// newSpan 构造一个已计时的空跨度。
func newSpan(name string, opts ...SpanOpt) *Span {
	options := spanOptions{kind: SpanKindInternal}
	option.Apply(&options, opts...)

	return &Span{
		Name:       name,
		Kind:       options.kind,
		StartedAt:  time.Now().UnixMilli(),
		Attributes: make(map[string]any),
	}
}

// StartRootSpan 开启一个任务根跨度:
// 自研侧不挂接任何父节点，OTel 侧以新 Trace 开根，
// 并以 Link 指向 ctx 中残留的父 SpanContext ( 如子代理委派场景 )，
// 保持 "关联但不父子" 的独立任务树语义。
func StartRootSpan(ctx context.Context, name string, opts ...SpanOpt) (context.Context, *Span) {
	span := newSpan(name, opts...)

	startOpts := []trace.SpanStartOption{
		trace.WithSpanKind(otelSpanKind(span.Kind)),
		trace.WithNewRoot(),
	}
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		startOpts = append(startOpts, trace.WithLinks(trace.Link{SpanContext: sc}))
	}

	otelCtx, otelSpan := tracer().Start(ctx, name, startOpts...)
	span.otelSpan = otelSpan

	return context.WithValue(otelCtx, traceCtxKey{}, span), span
}

// StartSpan 开启一个新的追踪跨度，并级联到 Context 中:
// 自研侧挂接到当前父 Span，OTel 侧经 SpanContext 自动继承父子。
func StartSpan(ctx context.Context, name string, opts ...SpanOpt) (context.Context, *Span) {
	span := newSpan(name, opts...)

	// 尝试获取父 Span。
	if p, ok := ctx.Value(traceCtxKey{}).(*Span); ok {
		p.mu.Lock()
		p.Children = append(p.Children, span)
		p.mu.Unlock()
	}

	otelCtx, otelSpan := tracer().Start(ctx, name, trace.WithSpanKind(otelSpanKind(span.Kind)))
	span.otelSpan = otelSpan

	// 将新创建的 Span 作为父 Span 传递 ( 同时保留 OTel 侧的 SpanContext )。
	return context.WithValue(otelCtx, traceCtxKey{}, span), span
}

// SpanFromContext 从 context 解析当前 Span，供中间层为所处跨度补充属性。
func SpanFromContext(ctx context.Context) (*Span, bool) {
	span, ok := ctx.Value(traceCtxKey{}).(*Span)
	return span, ok
}

// End 结束跨度并计算耗时; 幂等，重复调用不覆盖首次结果。
func (s *Span) End() {
	s.mu.Lock()
	if s.ended {
		s.mu.Unlock()
		return
	}
	s.ended = true

	s.EndedAt = time.Now().UnixMilli()
	s.DurationMs = s.EndedAt - s.StartedAt
	mirror := s.otelSpan
	s.mu.Unlock()

	if mirror != nil {
		mirror.End()
	}
}

// SetError 记录跨度内的失败信号; err 为 nil 时不产生任何写入。
func (s *Span) SetError(err error) {
	if err == nil {
		return
	}

	s.mu.Lock()
	s.Error = err.Error()
	mirror := s.otelSpan
	s.mu.Unlock()

	if mirror != nil {
		mirror.RecordError(err)
		mirror.SetStatus(codes.Error, err.Error())
	}
}

// Attribute 为当前 Span 记录元素据。
func (s *Span) Attribute(key string, val any) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.Attributes[key] = val
	if s.otelSpan != nil {
		s.otelSpan.SetAttributes(otelAttribute(key, val))
	}
}
