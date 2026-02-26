// Package retry provides a simple, production-ready retry mechanism with
// configurable backoff strategies, jitter, context cancellation, and retry conditions.
package retry

import (
	"context"
	"errors"
	"math/rand"
	"time"
)

// Func defines a retryable function.
// The function must respect the provided context.
type Func func(ctx context.Context) error

// If determines whether an error should trigger a retry.
// Return true to retry, false to stop immediately.
type If func(error) bool

// Backoff defines how long to wait before the next retry.
// attempt starts from 0 (first retry after the first failure).
type Backoff interface {
	Next(attempt int) time.Duration
}

// Fixed backoff strategy.
type fixedBackoff struct {
	interval time.Duration
}

func (b fixedBackoff) Next(int) time.Duration {
	return b.interval
}

// Fixed returns a fixed backoff strategy.
func Fixed(interval time.Duration) Backoff {
	return fixedBackoff{interval: interval}
}

// Linear backoff strategy.
type linearBackoff struct {
	base time.Duration
	max  time.Duration
}

func (b linearBackoff) Next(attempt int) time.Duration {
	d := b.base * time.Duration(attempt+1)
	if b.max > 0 && d > b.max {
		return b.max
	}
	return d
}

// Linear returns a linear backoff strategy.
func Linear(base time.Duration, max ...time.Duration) Backoff {
	var m time.Duration
	if len(max) > 0 {
		m = max[0]
	}
	return linearBackoff{base: base, max: m}
}

// Exponential backoff strategy.
type exponentialBackoff struct {
	base time.Duration
	max  time.Duration
}

func (b exponentialBackoff) Next(attempt int) time.Duration {
	d := b.base * time.Duration(1<<attempt)
	if b.max > 0 && d > b.max {
		return b.max
	}
	return d
}

// Exponential returns an exponential backoff strategy.
func Exponential(base time.Duration, max ...time.Duration) Backoff {
	var m time.Duration
	if len(max) > 0 {
		m = max[0]
	}
	return exponentialBackoff{base: base, max: m}
}

// Jitter modifies the backoff duration to avoid thundering herd problems.
type Jitter func(time.Duration) time.Duration

// NoJitter applies no jitter.
func NoJitter(d time.Duration) time.Duration {
	return d
}

// FullJitter applies full jitter: random duration in [0, d).
func FullJitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	return time.Duration(rand.Int63n(int64(d)))
}

// Config defines retry behavior.
// It is immutable during execution.
type Config struct {
	maxAttempts    int
	maxElapsedTime time.Duration
	backoff        Backoff
	jitter  Jitter
	retryIf If
}

func defaultConfig() *Config {
	return &Config{
		maxAttempts: 3,
		backoff:     Fixed(time.Second),
		jitter:      NoJitter,
		retryIf:     IsRetryableError,
	}
}

// Option configures retry behavior.
type Option func(*Config)

// WithMaxAttempts sets the maximum number of attempts (including the first attempt).
func WithMaxAttempts(n int) Option {
	return func(c *Config) {
		if n > 0 {
			c.maxAttempts = n
		}
	}
}

// WithMaxElapsedTime limits the total retry duration.
func WithMaxElapsedTime(d time.Duration) Option {
	return func(c *Config) {
		c.maxElapsedTime = d
	}
}

// WithBackoff sets the backoff strategy.
func WithBackoff(b Backoff) Option {
	return func(c *Config) {
		if b != nil {
			c.backoff = b
		}
	}
}

// WithJitter sets the jitter strategy.
func WithJitter(j Jitter) Option {
	return func(c *Config) {
		if j != nil {
			c.jitter = j
		}
	}
}

// WithRetryIf sets the retry condition function.
func WithRetryIf(fn If) Option {
	return func(c *Config) {
		if fn != nil {
			c.retryIf = fn
		}
	}
}

// Do executes fn with retry logic.
// The provided context controls cancellation and timeout.
func Do(ctx context.Context, fn Func, opts ...Option) error {
	if ctx == nil {
		ctx = context.Background()
	}

	cfg := defaultConfig()
	for _, opt := range opts {
		opt(cfg)
	}

	start := time.Now()
	var lastErr error

	for attempt := 0; attempt < cfg.maxAttempts; attempt++ {
		// Context cancellation check
		if err := ctx.Err(); err != nil {
			return err
		}

		// Max elapsed time check
		if cfg.maxElapsedTime > 0 && time.Since(start) >= cfg.maxElapsedTime {
			return lastErr
		}

		// Execute function
		err := fn(ctx)
		if err == nil {
			return nil
		}

		lastErr = err

		// Should retry?
		if !cfg.retryIf(err) {
			return err
		}

		// Last attempt, do not sleep
		if attempt == cfg.maxAttempts-1 {
			break
		}

		// Backoff + jitter
		wait := cfg.backoff.Next(attempt)
		wait = cfg.jitter(wait)

		if wait > 0 {
			timer := time.NewTimer(wait)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			}
		}
	}

	return lastErr
}

// IsRetryableError is the default retry condition.
// It retries all errors except context cancellation or deadline exceeded.
func IsRetryableError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	return true
}
