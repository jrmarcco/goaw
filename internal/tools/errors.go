package tools

import (
	"errors"
	"fmt"
	"io/fs"

	"github.com/jrmarcco/goaw/internal/schema"
)

// ToolError 工具领域错误。
// Code 是跨层传输的稳定错误码；Msg 面向模型可读；Cause 保留底层错误链 ( 供 errors.Is/As 与日志排查 )。
type ToolError struct {
	Code  schema.ToolErrorCode
	Msg   string
	Cause error

	// Soft 标记软失败 ( 如 bash 命令执行失败的自愈机制 )：
	// 输出照常回传给模型自纠，Registry 不置 IsError，但错误码仍然随结果传输，
	// 供恢复层决定是否注入自救提示。
	Soft bool
}

// Error 实现 error 接口。
// 错误码 token 直接渲染进文案，让模型在没有任何下游处理时也能看到稳定的自纠信号。
func (e *ToolError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("[%s] %s: %v", e.Code, e.Msg, e.Cause)
	}
	return fmt.Sprintf("[%s] %s", e.Code, e.Msg)
}

// Unwrap 保留底层错误链，使 errors.Is/As 能穿透 ToolError 抵达 OS 哨兵错误。
func (e *ToolError) Unwrap() error {
	return e.Cause
}

// newToolError 构造硬失败的工具领域错误。
func newToolError(code schema.ToolErrorCode, cause error, format string, args ...any) *ToolError {
	return &ToolError{
		Code:  code,
		Msg:   fmt.Sprintf(format, args...),
		Cause: cause,
	}
}

// wrapSysError 基于 POSIX 哨兵错误分类 OS 层错误。
// 使用 errors.Is 匹配哨兵值 ( fs.ErrNotExist / fs.ErrPermission ) 而非错误文案，
// 完全不受 locale 与包装文案影响，且天然穿透 %w 包装链。
// Msg 只保留中文上下文前缀，底层错误由 Error() 统一渲染，避免文案重复。
func wrapSysError(prefix string, err error) *ToolError {
	code := schema.ErrCodeIOFailure
	switch {
	case errors.Is(err, fs.ErrNotExist):
		code = schema.ErrCodeFileNotFound
	case errors.Is(err, fs.ErrPermission):
		code = schema.ErrCodePermissionDenied
	}

	return &ToolError{
		Code:  code,
		Msg:   prefix,
		Cause: err,
	}
}
