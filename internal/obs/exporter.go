package obs

import (
	"encoding/json"

	"github.com/jrmarcco/goaw/internal/logging"
)

// Exporter 是自研追踪树的导出器规范。
// 一次完整任务的根 Span 结束后，整棵树经由此接口离开进程内存;
// OTel 镜像管线独立于此，由 TracerProvider 自行流式导出。
type Exporter interface {
	// Export 导出一棵已结束的追踪树 ( root 已调用 End )。
	Export(root *Span)
}

// SlogExporter 把追踪树经 slog 导出:
// Info 常驻摘要 ( 节点数与总耗时 )，完整树 JSON 体积大、移入 Debug。
type SlogExporter struct{}

// NewSlogExporter 创建日志流导出器。
func NewSlogExporter() *SlogExporter {
	return &SlogExporter{}
}

// Export 实现 Exporter: 序列化失败降级为错误日志，不中断调用方。
func (e *SlogExporter) Export(root *Span) {
	logging.For("trace").Info(
		"追踪树导出",
		"root", root.Name,
		"spans", countSpans(root),
		"duration_ms", root.DurationMs,
	)

	payload, err := json.Marshal(root)
	if err != nil {
		logging.For("trace").Error("追踪树序列化失败", "root", root.Name, "error", err)
		return
	}
	logging.For("trace").Debug("追踪树完整数据", "trace", string(payload))
}

// countSpans 统计整树的 Span 节点数 ( 含根 )。
func countSpans(root *Span) int {
	count := 1
	for _, child := range root.Children {
		count += countSpans(child)
	}
	return count
}
