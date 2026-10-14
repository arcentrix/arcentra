// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package sandboxexec

import "fmt"

// CommandExitError 表示后端成功取得的明确 Shell 非零退出，不代表运行时或清理故障
// 自动重试仍由离线运行时策略拒绝
// Code 只接受进程退出范围内的非零值
type CommandExitError struct{ Code int32 }

func (e *CommandExitError) Error() string {
	return fmt.Sprintf("sandbox command exited with code %d", e.Code)
}

// IsCommandExit 仅允许错误树全部由明确命令退出及其包装组成
// 任意附带的清理、日志或运行时错误均阻止 continueOnError 降级
func IsCommandExit(err error) bool {
	var only func(error, int) bool
	only = func(current error, depth int) bool {
		if current == nil || depth > 64 {
			return false
		}
		if exit, ok := current.(*CommandExitError); ok {
			return exit != nil && exit.Code > 0 && exit.Code <= 255
		}
		if joined, ok := current.(interface{ Unwrap() []error }); ok {
			children := joined.Unwrap()
			if len(children) == 0 {
				return false
			}
			for _, child := range children {
				if !only(child, depth+1) {
					return false
				}
			}
			return true
		}
		if wrapped, ok := current.(interface{ Unwrap() error }); ok {
			return only(wrapped.Unwrap(), depth+1)
		}
		return false
	}
	return only(err, 0)
}

// RetryCommandExitCode 仅返回错误树每个叶节点一致且在显式允许列表内的命令退出码
// 运行时、清理、日志、取消及混合退出码均不构成重试依据
func RetryCommandExitCode(err error, allowed []int32) (int32, bool) {
	var code int32
	var walk func(error, int) bool
	walk = func(current error, depth int) bool {
		if current == nil || depth > 64 {
			return false
		}
		if exit, ok := current.(*CommandExitError); ok {
			if exit == nil || exit.Code < 1 || exit.Code > 125 || code != 0 && code != exit.Code {
				return false
			}
			code = exit.Code
			return true
		}
		if joined, ok := current.(interface{ Unwrap() []error }); ok {
			children := joined.Unwrap()
			if len(children) == 0 {
				return false
			}
			for _, child := range children {
				if !walk(child, depth+1) {
					return false
				}
			}
			return true
		}
		if wrapped, ok := current.(interface{ Unwrap() error }); ok {
			return walk(wrapped.Unwrap(), depth+1)
		}
		return false
	}
	if !walk(err, 0) {
		return 0, false
	}
	for _, candidate := range allowed {
		if candidate == code {
			return code, true
		}
	}
	return 0, false
}
