package obs

import (
	"encoding/json"
	"log/slog"
)

// Exporter 是自研追踪树的导出器规范。
// 一次完整任务的根 Span 结束后，整棵树经由此接口离开进程内存;
// OTel 镜像管线独立于此，由 TracerProvider 自行流式导出。
type Exporter interface {
	// Export 导出一棵已结束的追踪树 ( root 已调用 End )。
	Export(root *Span)
}

// SlogExporter 把整棵追踪树序列化为 JSON，经 slog 输出到现有日志流。
type SlogExporter struct{}

// NewSlogExporter 创建日志流导出器。
func NewSlogExporter() *SlogExporter {
	return &SlogExporter{}
}

// Export 实现 Exporter: 序列化失败降级为错误日志，不中断调用方。
func (e *SlogExporter) Export(root *Span) {
	payload, err := json.Marshal(root)
	if err != nil {
		slog.Error("[trace] 追踪树序列化失败", "root", root.Name, "error", err)
		return
	}
	slog.Info("[trace] 追踪树导出", "root", root.Name, "trace", string(payload))
}
