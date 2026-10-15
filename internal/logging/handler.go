package logging

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 时间与列宽格式常量。
const (
	// timeLayout 毫秒精度、无日期时区 ( CLI 会话通常为分钟级 )。
	timeLayout = "15:04:05.000"

	// levelWidth 级别徽宽度 ( DEBUG/ERROR 为最长 )。
	levelWidth = 5

	// componentWidth 组件列宽度 ( compactor 为最长组件名 )。
	componentWidth = 10
)

// ANSI 颜色码 ( WSL2/Linux 终端，非 TTY 环境由 shouldColor 关闭 )。
const (
	ansiReset  = "\x1b[0m"
	ansiGray   = "\x1b[90m"
	ansiBlue   = "\x1b[34m"
	ansiYellow = "\x1b[33m"
	ansiRed    = "\x1b[31m"
	ansiCyan   = "\x1b[36m"
)

// writeMu 串行化底层 Write，保证多 handler 并发时行不交错。
var writeMu sync.Mutex

var _ slog.Handler = (*ConsoleHandler)(nil)

// ConsoleHandler 渲染单行紧凑日志:
//
//	15:04:05.000 INFO  engine    运行开始 session=s1 workspace=/ws
//
// 整行拼装完成后一次写入; color 控制是否输出 ANSI 着色 ( 终端实例开、文件实例关 )。
type ConsoleHandler struct {
	w     io.Writer
	level slog.Leveler
	color bool

	attrs  []slog.Attr
	groups []string
}

// NewConsoleHandler 创建单行控制台 handler。
func NewConsoleHandler(w io.Writer, level slog.Leveler, color bool) *ConsoleHandler {
	return &ConsoleHandler{
		w:     w,
		level: level,
		color: color,
	}
}

func (h *ConsoleHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level.Level()
}

func (h *ConsoleHandler) Handle(_ context.Context, r slog.Record) error {
	var buf strings.Builder

	h.appendTime(&buf, r.Time)
	h.appendLevel(&buf, r.Level)
	h.appendComponent(&buf)
	buf.WriteString(r.Message)
	h.appendAttrs(&buf, &r)
	buf.WriteByte('\n')

	writeMu.Lock()
	defer writeMu.Unlock()
	_, err := io.WriteString(h.w, buf.String())
	return err
}

// clone 复制 handler 以支持 WithAttrs/WithGroup 的不可变派生。
func (h *ConsoleHandler) clone() *ConsoleHandler {
	return &ConsoleHandler{
		w:      h.w,
		level:  h.level,
		color:  h.color,
		attrs:  append([]slog.Attr(nil), h.attrs...),
		groups: append([]string(nil), h.groups...),
	}
}

func (h *ConsoleHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	next := h.clone()
	next.attrs = append(next.attrs, attrs...)
	return next
}

func (h *ConsoleHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	next := h.clone()
	next.groups = append(next.groups, name)
	return next
}

// appendTime 写入毫秒时间戳。
func (h *ConsoleHandler) appendTime(buf *strings.Builder, t time.Time) {
	buf.WriteString(t.Format(timeLayout))
	buf.WriteByte(' ')
}

// appendLevel 写入着色的级别徽。
func (h *ConsoleHandler) appendLevel(buf *strings.Builder, level slog.Level) {
	if h.color {
		buf.WriteString(levelColor(level))
	}
	fmt.Fprintf(buf, "%-*s", levelWidth, levelLabel(level))
	if h.color {
		buf.WriteString(ansiReset)
	}
	buf.WriteByte(' ')
}

// appendComponent 把 component 属性提升为独立的组件列，其余属性留在行尾。
func (h *ConsoleHandler) appendComponent(buf *strings.Builder) {
	component := ""
	for _, attr := range h.attrs {
		if attr.Key == componentKey {
			component = attr.Value.String()
			break
		}
	}

	if h.color && component != "" {
		buf.WriteString(ansiCyan)
	}
	fmt.Fprintf(buf, "%-*s", componentWidth, component)
	if h.color && component != "" {
		buf.WriteString(ansiReset)
	}
	buf.WriteByte(' ')
}

// appendAttrs 写入行尾属性 ( 预置属性中的 component 已提升为列，跳过 )。
func (h *ConsoleHandler) appendAttrs(buf *strings.Builder, r *slog.Record) {
	for _, attr := range h.attrs {
		if attr.Key == componentKey {
			continue
		}
		h.appendAttr(buf, attr)
	}
	r.Attrs(func(attr slog.Attr) bool {
		h.appendAttr(buf, attr)
		return true
	})
}

// appendAttr 写入单个 k=v 属性，值含空白或引号时加引号转义。
func (h *ConsoleHandler) appendAttr(buf *strings.Builder, attr slog.Attr) {
	if attr.Equal(slog.Attr{}) {
		return
	}

	key := attr.Key
	if len(h.groups) > 0 {
		key = strings.Join(h.groups, ".") + "." + key
	}

	buf.WriteByte(' ')
	if h.color {
		buf.WriteString(ansiGray)
		buf.WriteString(key)
		buf.WriteString(ansiReset)
	} else {
		buf.WriteString(key)
	}
	buf.WriteByte('=')
	buf.WriteString(quoteValue(formatValue(attr.Value)))
}

// levelLabel 级别徽文本。
func levelLabel(level slog.Level) string {
	switch level {
	case slog.LevelDebug:
		return "DEBUG"
	case slog.LevelInfo:
		return "INFO"
	case slog.LevelWarn:
		return "WARN"
	case slog.LevelError:
		return "ERROR"
	default:
		return "LEVEL"
	}
}

// levelColor 级别徽颜色: DEBUG 灰 / INFO 蓝 / WARN 黄 / ERROR 红。
func levelColor(level slog.Level) string {
	switch level {
	case slog.LevelDebug:
		return ansiGray
	case slog.LevelInfo:
		return ansiBlue
	case slog.LevelWarn:
		return ansiYellow
	case slog.LevelError:
		return ansiRed
	default:
		return ""
	}
}

// formatValue 渲染属性值 ( error 等任意类型经 %v 兜底 )。
func formatValue(v slog.Value) string {
	switch v.Kind() {
	case slog.KindString:
		return v.String()
	case slog.KindTime:
		return v.Time().Format(time.RFC3339)
	case slog.KindDuration:
		return v.Duration().String()
	case slog.KindInt64:
		return strconv.FormatInt(v.Int64(), 10)
	default:
		return fmt.Sprintf("%v", v.Any())
	}
}

// quoteValue 值含空白、引号或等号时加引号，保证单行内的 k=v 可再切分。
func quoteValue(s string) string {
	if strings.ContainsAny(s, " \t\n\"=") {
		return strconv.Quote(s)
	}
	return s
}

var _ slog.Handler = (*multiHandler)(nil)

// multiHandler 把记录分发给多个 handler ( 终端 + 文件双写 )。
type multiHandler struct {
	handlers []slog.Handler
}

func (m multiHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, handler := range m.handlers {
		if handler.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (m multiHandler) Handle(ctx context.Context, r slog.Record) error {
	var errs []error
	for _, handler := range m.handlers {
		if err := handler.Handle(ctx, r); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (m multiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := make([]slog.Handler, len(m.handlers))
	for idx, handler := range m.handlers {
		next[idx] = handler.WithAttrs(attrs)
	}
	return multiHandler{handlers: next}
}

func (m multiHandler) WithGroup(name string) slog.Handler {
	next := make([]slog.Handler, len(m.handlers))
	for idx, handler := range m.handlers {
		next[idx] = handler.WithGroup(name)
	}
	return multiHandler{handlers: next}
}
