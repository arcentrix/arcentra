// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package service

import (
	"fmt"
	"reflect"
	"sync"
)

// pipelineEngineBinding 发布一次启动绑定，允许先构造的服务安全读取执行器
// 未绑定时读取返回 nil，绑定后不能更换或移除执行器
// 持锁期间不调用执行器，避免把执行时长或回调带入锁范围
// Services 及其绑定均通过指针使用，不得在使用后复制
type pipelineEngineBinding struct {
	mu     sync.RWMutex
	engine IPipelineEngine
}

// BindPipelineEngine 在启动期间绑定唯一执行器，拒绝缺失值和后续重新绑定
func (s *Services) BindPipelineEngine(engine IPipelineEngine) error {
	if s == nil {
		return fmt.Errorf("pipeline services unavailable")
	}
	if isNilPipelineEngine(engine) {
		return fmt.Errorf("pipeline engine must not be nil")
	}
	s.pipelineEngine.mu.Lock()
	defer s.pipelineEngine.mu.Unlock()
	if s.pipelineEngine.engine != nil {
		return fmt.Errorf("pipeline engine is already bound")
	}
	s.pipelineEngine.engine = engine
	return nil
}

func isNilPipelineEngine(engine IPipelineEngine) bool {
	if engine == nil {
		return true
	}
	switch reflect.ValueOf(engine).Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflect.ValueOf(engine).IsNil()
	default:
		return false
	}
}

func (b *pipelineEngineBinding) load() IPipelineEngine {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.engine
}

func (s *PipelineServiceImpl) currentEngine() IPipelineEngine {
	if s.engineProvider != nil {
		return s.engineProvider()
	}
	// 包内独立构造的服务保持原有显式注入方式；生产构造器始终使用同步绑定
	if isNilPipelineEngine(s.engine) {
		return nil
	}
	return s.engine
}
