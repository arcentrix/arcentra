// Copyright 2025 Arcentra Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package context

import (
	"context"
	"sync"

	"github.com/arcentrix/arcentra/pkg/num"
	"github.com/timandy/routine"
	"go.opentelemetry.io/otel/trace"
)

const bucketsSize = 128

type (
	contextBucket struct {
		lock sync.RWMutex
		data map[int64]context.Context
	}
	contextBuckets struct {
		buckets [bucketsSize]*contextBucket
	}
)

var goroutineContext contextBuckets

func init() {
	for i := range goroutineContext.buckets {
		goroutineContext.buckets[i] = &contextBucket{
			data: make(map[int64]context.Context),
		}
	}
}

// GetContext get context from goroutine context
func GetContext() context.Context {
	god := routine.Goid()
	idx := god % bucketsSize
	bucket := goroutineContext.buckets[idx]
	bucket.lock.RLock()
	ctx := bucket.data[num.MustInt64(god)]
	bucket.lock.RUnlock()
	return ctx
}

// SetContext set context to goroutine context
func SetContext(ctx context.Context) {
	god := routine.Goid()
	idx := god % bucketsSize
	bucket := goroutineContext.buckets[idx]
	bucket.lock.Lock()
	defer bucket.lock.Unlock()
	bucket.data[num.MustInt64(god)] = ctx
}

// ClearContext clear context from goroutine context
func ClearContext() {
	god := routine.Goid()
	idx := god % bucketsSize
	bucket := goroutineContext.buckets[idx]
	bucket.lock.Lock()
	defer bucket.lock.Unlock()
	delete(bucket.data, num.MustInt64(god))
}

// RunWithContext run function with context
func RunWithContext(ctx context.Context, fn func(ctx context.Context)) {
	SetContext(ctx)
	defer ClearContext()
	fn(ctx)
}

// WithSpan context with span
func WithSpan(ctx context.Context) context.Context {
	if span := trace.SpanFromContext(ctx); !span.SpanContext().IsValid() {
		pct := GetContext()
		if pct != nil {
			if span := trace.SpanFromContext(pct); span.SpanContext().IsValid() {
				ctx = trace.ContextWithSpan(ctx, span)
			}
		}
	}
	return ctx
}
