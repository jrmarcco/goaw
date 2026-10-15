// Package logging 提供项目统一的日志输出:
// 单行紧凑格式 ( 时间 级别 组件 消息 k=v )、TTY 着色与可选双写落盘。
//
// 环境变量:
//
//	GOAW_LOG_LEVEL: debug | info | warn | error ( 缺省 info )
//	GOAW_LOG_COLOR: auto | always | never ( 缺省 auto，仅终端着色 )
//	GOAW_LOG_FILE:  追加写的日志文件路径 ( 缺省不落盘，落盘为同格式纯文本 )
package logging

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
)

// logFilePerm 日志文件权限。
const logFilePerm os.FileMode = 0o644

// componentKey 组件属性键: For 注入、ConsoleHandler 据此渲染组件列。
const componentKey = "component"

// For 返回携带组件标识的日志器 ( 组件即日志的 功能域 标签，如 engine、approval )。
// 每次即时解析 slog.Default()，测试期替换全局 handler 后新调用即刻生效。
func For(component string) *slog.Logger {
	return slog.Default().With(componentKey, component)
}

// Truncate 按 rune 安全截断字符串，超限时以省略号加剩余字符数标注。
func Truncate(s string, limit int) string {
	if limit <= 0 {
		return s
	}

	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit]) + fmt.Sprintf("…(+%d)", len(runes)-limit)
}

// Setup 按环境变量装配全局日志 ( 终端单行 handler + 可选文件双写 )，
// 返回的 cleanup 需在进程退出前调用以冲刷并关闭文件句柄。
func Setup() func() {
	level := ParseLevel(os.Getenv("GOAW_LOG_LEVEL"))
	terminal := NewConsoleHandler(os.Stdout, level, shouldColor(os.Getenv("GOAW_LOG_COLOR")))

	var handler slog.Handler = terminal
	closers := make([]func(), 0, 1)
	if path := os.Getenv("GOAW_LOG_FILE"); path != "" {
		// 路径来自运维配置的环境变量，与进程自身信任级一致。
		file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, logFilePerm) //nolint:gosec // G304: 运维显式指定的日志落盘路径，非不可信输入
		if err != nil {
			// 装配失败降级为仅终端输出，不阻断启动。
			slog.Error("日志文件打开失败", "path", path, "error", err)
		} else {
			handler = multiHandler{handlers: []slog.Handler{terminal, NewConsoleHandler(file, level, false)}}
			closers = append(closers, func() { _ = file.Close() })
		}
	}

	slog.SetDefault(slog.New(handler))

	return func() {
		for _, closer := range closers {
			closer()
		}
	}
}

// shouldColor 判定终端输出是否着色 ( mode 见包文档 )。
func shouldColor(mode string) bool {
	switch mode {
	case "always":
		return true
	case "never":
		return false
	default:
		return isTerminal(os.Stdout)
	}
}

// isTerminal 判定文件描述符是否为终端 ( 字符设备 )。
func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// ParseLevel 解析级别名 ( 空串与非法值回落 info )。
func ParseLevel(raw string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
