// Copyright 2025 Arcentra Team
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

// Package loop Provide a tool for performing tasks in a loop.
// use example:
// l := loop.New()
// l.Do(func() (bool, error) {
package loop

import (
	"context"
	"math"
	"time"
)

// Loop Define a structure that executes tasks in a loop..
type Loop struct {
	maxTimes      uint64
	declineRatio  float64
	declineLimit  time.Duration
	interval      time.Duration
	lastSleepTime time.Duration
	ctx           context.Context
}

// Option Define Loop option type.
type Option func(*Loop)

func New(options ...Option) *Loop {
	loop := &Loop{
		interval:     time.Second,
		maxTimes:     math.MaxUint64,
		declineRatio: 1,
		declineLimit: 0,
	}

	for _, op := range options {
		op(loop)
	}

	loop.lastSleepTime = loop.interval

	return loop
}

// sleepUntilCtxDone sleep d duration until ctx done.
// Done maybe triggered by context timeout of deadline exceeded.
func sleepUntilCtxDone(d time.Duration, ctx context.Context) (abort bool) {
	if ctx == nil {
		time.Sleep(d)
		return false
	}

	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-timer.C:
		return false
	case <-ctx.Done():
		return true
	}
}

// Do Execute the given method in a loop.
// The method returns two values: a boolean indicating whether to abort the loop,
func (l *Loop) Do(f func() (bool, error)) error {
	if l.ctx != nil && l.ctx.Err() != nil {
		return nil
	}

	var (
		i     uint64
		err   error
		abort bool
	)
	for i = 0; i < l.maxTimes; i++ {
		abort, err = f()
		if abort {
			return err
		}
		if err != nil {
			// Multiply the time since last sleep pause by the rate of decline
			// decline = decline * declineRatio
			// t: time since last sleep pause
			// r: declineRatio
			// t = t * r
			l.lastSleepTime = time.Duration(float64(l.lastSleepTime) * l.declineRatio)
			if l.declineLimit > 0 && l.lastSleepTime > l.declineLimit {
				l.lastSleepTime = l.declineLimit
			}
			if sleepUntilCtxDone(l.lastSleepTime, l.ctx) {
				return nil
			}
			continue
		}

		// Reset the last sleep time to the interval time
		l.lastSleepTime = l.interval
		if sleepUntilCtxDone(l.lastSleepTime, l.ctx) {
			return nil
		}
	}
	return err
}

// WithMaxTimes Set the maximum number of loop executions, default is unlimited.
func WithMaxTimes(n uint64) Option {
	return func(l *Loop) {
		l.maxTimes = n
	}
}

// WithDeclineRatio Set the decline ratio for error retries, default is 1 (no decline).
func WithDeclineRatio(n float64) Option {
	return func(l *Loop) {
		if n < 1 {
			return
		}
		l.declineRatio = n
	}
}

// WithDeclineLimit Set the maximum decline time for error retries, default is no limit.
func WithDeclineLimit(t time.Duration) Option {
	return func(l *Loop) {
		if t < 0 {
			return
		}
		l.declineLimit = t
	}
}

// WithInterval Set the interval time between loop executions, default is 1 second.
func WithInterval(t time.Duration) Option {
	return func(l *Loop) {
		if t < time.Millisecond {
			return
		}
		l.interval = t
	}
}

// WithContext set the context to cancel loop
func WithContext(ctx context.Context) Option {
	return func(loop *Loop) {
		loop.ctx = ctx
	}
}
