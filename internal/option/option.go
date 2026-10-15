// Package option 提供泛型选项模式工具。
//
// 本包自 github.com/jrmarcco/goaw/internal/option 收编而来 ( 仅保留项目实际使用的 API )，
// 用于消除对 jit 模块的外部依赖; 语义与原实现完全一致。
package option

// Opt 是选项模式的泛型定义: 一个作用于目标指针的配置函数。
type Opt[T any] func(*T)

// Apply 依序把选项应用到目标。
func Apply[T any](t *T, opts ...Opt[T]) {
	for _, opt := range opts {
		opt(t)
	}
}
