// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package admission

import "errors"

// RejectionError 表示本次提交在创建协调器或持久化调度认领前已被明确拒绝
// 拒绝不保证失败状态已持久化，调用方不得据此无条件覆盖运行状态
// 未分类错误（包括重复提交或认领结果不明）不提供此保证
type RejectionError struct {
	Cause error
}

// Error 返回入场拒绝原因
func (e *RejectionError) Error() string {
	return "pipeline admission rejected: " + e.Cause.Error()
}

// Unwrap 保留底层拒绝原因以供错误匹配
func (e *RejectionError) Unwrap() error { return e.Cause }

// IsRejected 判断错误是否明确表示提交在调度认领前被拒绝
func IsRejected(err error) bool {
	var rejection *RejectionError
	return errors.As(err, &rejection)
}
