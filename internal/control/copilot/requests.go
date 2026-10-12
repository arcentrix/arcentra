// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package copilot

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ErrBusy 表示本机并发或去重容量已达到上限
var ErrBusy = errors.New("Copilot request limit reached or request ID was already used")

type requestState struct {
	cancel  context.CancelFunc
	expires time.Time
}

// Requests 以用户、项目及随机请求标识隔离取消和短期去重状态
// 多副本部署须为分析和取消请求配置相同实例的会话亲和性
type Requests struct {
	mu     sync.Mutex
	states map[string]requestState
}

func requestKey(user, project, id string) string { return user + "\x00" + project + "\x00" + id }
func (r *Requests) prune() {
	if r.states == nil {
		r.states = map[string]requestState{}
	}
	for k, v := range r.states {
		if v.cancel == nil && time.Now().After(v.expires) {
			delete(r.states, k)
		}
	}
}

// Begin 建立有界取消上下文，重复标识永不自动重发
func (r *Requests) Begin(parent context.Context, user, project, id string) (context.Context, func(), error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.prune()
	key := requestKey(user, project, id)
	if _, exists := r.states[key]; exists {
		return nil, nil, ErrBusy
	}
	active, owned := 0, 0
	for k, v := range r.states {
		if v.cancel != nil {
			active++
			if len(k) > len(user) && k[:len(user)+1] == user+"\x00" {
				owned++
			}
		}
	}
	if active >= 16 || owned >= 2 || len(r.states) >= 1024 {
		return nil, nil, ErrBusy
	}
	ctx, cancel := context.WithTimeout(parent, 45*time.Second)
	r.states[key] = requestState{cancel: cancel}
	finish := func() {
		cancel()
		r.mu.Lock()
		r.states[key] = requestState{expires: time.Now().Add(90 * time.Second)}
		r.mu.Unlock()
	}
	return ctx, finish, nil
}

// Cancel 同时记录短期取消墓碑，防止取消先于分析注册时发生竞态
func (r *Requests) Cancel(user, project, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.prune()
	key := requestKey(user, project, id)
	state, exists := r.states[key]
	if !exists && len(r.states) >= 1024 {
		return ErrBusy
	}
	if state.cancel != nil {
		state.cancel()
		return nil
	}
	r.states[key] = requestState{expires: time.Now().Add(90 * time.Second)}
	return nil
}
