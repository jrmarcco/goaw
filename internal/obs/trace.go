package obs

import (
	"context"
	"sync"
	"time"
)

// traceCtxKey 是 Span 在 context 中的键类型。
// 独立的私有结构体类型，保证不与其他包注入的 context 值冲突。
type traceCtxKey struct{}

// Span 代表链路追踪总的一个时间跨度和操作节点。
type Span struct {
	mu sync.Mutex

	Name string `json:"name"`

	StartedAt   int64 `json:"startedAt"`
	EndedAt     int64 `json:"endedAt"`
	DurantionMs int64 `json:"durantionMs"`

	Attributes map[string]any `json:"attributes,omitempty"`
	Children   []*Span        `json:"children,omitempty"`
}

// StartSpan 开启一个新的追踪跨度，并级联到 Context 中。
func StartSpan(ctx context.Context, name string) (context.Context, *Span) {
	span := &Span{
		Name:       name,
		StartedAt:  time.Now().UnixMilli(),
		Attributes: make(map[string]any),
	}

	// 尝试获取父 Span。
	if p, ok := ctx.Value(traceCtxKey{}).(*Span); ok {
		p.mu.Lock()
		p.Children = append(p.Children, span)
		p.mu.Unlock()
	}

	// 将新创建的 Span 作为父 Span 传递。
	subCtx := context.WithValue(ctx, traceCtxKey{}, span)
	return subCtx, span
}

// End 结束跨度并计算耗时。
func (s *Span) End() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.EndedAt = time.Now().UnixMilli()
	s.DurantionMs = s.EndedAt - s.StartedAt
}

// Attribute 为当前 Span 记录元素据。
func (s *Span) Attribute(key string, val any) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.Attributes[key] = val
}
