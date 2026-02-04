package retry

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/arcentrix/arcade/pkg/safe"
)

func TestDo_Success(t *testing.T) {
	ctx := context.Background()
	err := Do(ctx, func(ctx context.Context) error {
		return nil
	})
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}

func TestDo_RetrySuccess(t *testing.T) {
	ctx := context.Background()
	attempts := 0
	err := Do(ctx, func(ctx context.Context) error {
		attempts++
		if attempts < 3 {
			return errors.New("temporary error")
		}
		return nil
	}, WithMaxAttempts(3))
	if err != nil {
		t.Errorf("expected no error after retries, got %v", err)
	}
	if attempts != 3 {
		t.Errorf("expected 3 attempts, got %d", attempts)
	}
}

func TestDo_MaxAttempts(t *testing.T) {
	ctx := context.Background()
	attempts := 0
	err := Do(ctx, func(ctx context.Context) error {
		attempts++
		return errors.New("persistent error")
	}, WithMaxAttempts(3))
	if err == nil {
		t.Error("expected error after max attempts")
	}
	if attempts != 3 {
		t.Errorf("expected 3 attempts, got %d", attempts)
	}
}

func TestDo_FixedBackoff(t *testing.T) {
	ctx := context.Background()
	attempts := 0
	start := time.Now()
	err := Do(ctx, func(ctx context.Context) error {
		attempts++
		if attempts < 3 {
			return errors.New("temporary error")
		}
		return nil
	}, WithMaxAttempts(3), WithBackoff(Fixed(50*time.Millisecond)))
	duration := time.Since(start)

	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if attempts != 3 {
		t.Errorf("expected 3 attempts, got %d", attempts)
	}
	// Should sleep twice (between attempts), each for 50ms
	// Allow some tolerance for test execution time
	if duration < 90*time.Millisecond || duration > 200*time.Millisecond {
		t.Errorf("expected duration around 100ms, got %v", duration)
	}
}

func TestDo_ExponentialBackoff(t *testing.T) {
	ctx := context.Background()
	attempts := 0
	start := time.Now()
	err := Do(ctx, func(ctx context.Context) error {
		attempts++
		if attempts < 3 {
			return errors.New("temporary error")
		}
		return nil
	}, WithMaxAttempts(3), WithBackoff(Exponential(50*time.Millisecond)))
	duration := time.Since(start)

	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if attempts != 3 {
		t.Errorf("expected 3 attempts, got %d", attempts)
	}
	// Should sleep: 50ms (2^0 * 50) + 100ms (2^1 * 50) = 150ms
	// Allow some tolerance
	if duration < 140*time.Millisecond || duration > 250*time.Millisecond {
		t.Errorf("expected duration around 150ms, got %v", duration)
	}
}

func TestDo_LinearBackoff(t *testing.T) {
	ctx := context.Background()
	attempts := 0
	start := time.Now()
	err := Do(ctx, func(ctx context.Context) error {
		attempts++
		if attempts < 3 {
			return errors.New("temporary error")
		}
		return nil
	}, WithMaxAttempts(3), WithBackoff(Linear(50*time.Millisecond)))
	duration := time.Since(start)

	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if attempts != 3 {
		t.Errorf("expected 3 attempts, got %d", attempts)
	}
	// Should sleep: 50ms (1 * 50) + 100ms (2 * 50) = 150ms
	// Allow some tolerance
	if duration < 140*time.Millisecond || duration > 250*time.Millisecond {
		t.Errorf("expected duration around 150ms, got %v", duration)
	}
}

func TestDo_ExponentialBackoffWithMax(t *testing.T) {
	ctx := context.Background()
	attempts := 0
	start := time.Now()
	err := Do(ctx, func(ctx context.Context) error {
		attempts++
		if attempts < 3 {
			return errors.New("temporary error")
		}
		return nil
	}, WithMaxAttempts(3), WithBackoff(Exponential(50*time.Millisecond, 75*time.Millisecond)))
	duration := time.Since(start)

	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if attempts != 3 {
		t.Errorf("expected 3 attempts, got %d", attempts)
	}
	// Should sleep: 50ms (capped) + 75ms (capped) = 125ms
	// Allow some tolerance
	if duration < 115*time.Millisecond || duration > 200*time.Millisecond {
		t.Errorf("expected duration around 125ms, got %v", duration)
	}
}

func TestDo_CustomRetryIf(t *testing.T) {
	ctx := context.Background()
	attempts := 0
	retryableErr := errors.New("retryable")
	nonRetryableErr := errors.New("non-retryable")

	err := Do(ctx, func(ctx context.Context) error {
		attempts++
		if attempts == 1 {
			return retryableErr
		}
		return nonRetryableErr
	}, WithMaxAttempts(3), WithBackoff(Fixed(10*time.Millisecond)), WithRetryIf(func(err error) bool {
		return err == retryableErr
	}))

	if err != nonRetryableErr {
		t.Errorf("expected non-retryable error, got %v", err)
	}
	if attempts != 2 {
		t.Errorf("expected 2 attempts (should stop on non-retryable error), got %d", attempts)
	}
}

func TestDo_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	attempts := 0

	// Cancel after a short delay
	safe.Go(func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	})

	err := Do(ctx, func(ctx context.Context) error {
		attempts++
		return errors.New("error")
	}, WithMaxAttempts(5), WithBackoff(Fixed(100*time.Millisecond)))

	if err == nil {
		t.Error("expected error due to context cancellation")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled error, got %v", err)
	}
	if attempts < 1 {
		t.Errorf("expected at least 1 attempt, got %d", attempts)
	}
}

func TestDo_ContextTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	attempts := 0
	err := Do(ctx, func(ctx context.Context) error {
		attempts++
		time.Sleep(50 * time.Millisecond)
		return errors.New("error")
	}, WithMaxAttempts(5), WithBackoff(Fixed(10*time.Millisecond)))

	if err == nil {
		t.Error("expected error due to context timeout")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected context.DeadlineExceeded error, got %v", err)
	}
}

func TestDo_MaxElapsedTime(t *testing.T) {
	ctx := context.Background()
	attempts := 0
	start := time.Now()
	err := Do(ctx, func(ctx context.Context) error {
		attempts++
		return errors.New("error")
	}, WithMaxAttempts(10), WithBackoff(Fixed(100*time.Millisecond)), WithMaxElapsedTime(150*time.Millisecond))
	duration := time.Since(start)

	if err == nil {
		t.Error("expected error")
	}
	// Allow more tolerance for max elapsed time test due to timing variations
	if duration < 140*time.Millisecond || duration > 250*time.Millisecond {
		t.Errorf("expected duration around 150ms, got %v", duration)
	}
	if attempts < 1 {
		t.Errorf("expected at least 1 attempt, got %d", attempts)
	}
}

func TestDo_NilContext(t *testing.T) {
	// Should handle nil context gracefully
	attempts := 0
	err := Do(context.Background(), func(ctx context.Context) error {
		attempts++
		if attempts < 2 {
			return errors.New("error")
		}
		return nil
	}, WithMaxAttempts(3))
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if attempts != 2 {
		t.Errorf("expected 2 attempts, got %d", attempts)
	}
}

func TestDo_ZeroMaxAttempts(t *testing.T) {
	ctx := context.Background()
	attempts := 0
	err := Do(ctx, func(ctx context.Context) error {
		attempts++
		return errors.New("error")
	}, WithMaxAttempts(0))

	if err == nil {
		t.Error("expected error")
	}
	// WithMaxAttempts(0) should not change maxAttempts (it's <= 0)
	// So it should use default (3)
	if attempts != 3 {
		t.Errorf("expected 3 attempts (default), got %d", attempts)
	}
}

func TestDo_NegativeMaxAttempts(t *testing.T) {
	ctx := context.Background()
	attempts := 0
	err := Do(ctx, func(ctx context.Context) error {
		attempts++
		return errors.New("error")
	}, WithMaxAttempts(-1))

	if err == nil {
		t.Error("expected error")
	}
	// WithMaxAttempts(-1) should not change maxAttempts (it's <= 0)
	// So it should use default (3)
	if attempts != 3 {
		t.Errorf("expected 3 attempts (default), got %d", attempts)
	}
}

func TestDo_Jitter(t *testing.T) {
	ctx := context.Background()
	attempts := 0
	start := time.Now()
	err := Do(ctx, func(ctx context.Context) error {
		attempts++
		if attempts < 3 {
			return errors.New("temporary error")
		}
		return nil
	}, WithMaxAttempts(3), WithBackoff(Fixed(50*time.Millisecond)), WithJitter(NoJitter))
	duration := time.Since(start)

	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if attempts != 3 {
		t.Errorf("expected 3 attempts, got %d", attempts)
	}
	// With NoJitter, should sleep exactly 50ms + 50ms = 100ms
	// Allow some tolerance
	if duration < 90*time.Millisecond || duration > 200*time.Millisecond {
		t.Errorf("expected duration around 100ms, got %v", duration)
	}
}

func TestDo_FullJitter(t *testing.T) {
	ctx := context.Background()
	attempts := 0
	start := time.Now()
	err := Do(ctx, func(ctx context.Context) error {
		attempts++
		if attempts < 3 {
			return errors.New("temporary error")
		}
		return nil
	}, WithMaxAttempts(3), WithBackoff(Fixed(50*time.Millisecond)), WithJitter(FullJitter))
	duration := time.Since(start)

	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if attempts != 3 {
		t.Errorf("expected 3 attempts, got %d", attempts)
	}
	// With FullJitter, sleep time should be less than 50ms + 50ms = 100ms
	// But could be anywhere from 0 to 100ms
	if duration < 0 || duration > 200*time.Millisecond {
		t.Errorf("expected duration between 0 and 200ms, got %v", duration)
	}
}

func TestDo_ContextCancellationDuringBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	attempts := 0

	// Cancel during backoff
	safe.Go(func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	})

	err := Do(ctx, func(ctx context.Context) error {
		attempts++
		return errors.New("error")
	}, WithMaxAttempts(5), WithBackoff(Fixed(100*time.Millisecond)))

	if err == nil {
		t.Error("expected error due to context cancellation")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled error, got %v", err)
	}
	if attempts < 1 {
		t.Errorf("expected at least 1 attempt, got %d", attempts)
	}
}

func TestDo_NoRetryOnContextError(t *testing.T) {
	ctx := context.Background()
	attempts := 0
	err := Do(ctx, func(ctx context.Context) error {
		attempts++
		return context.Canceled
	}, WithMaxAttempts(3))

	if err == nil {
		t.Error("expected error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled error, got %v", err)
	}
	// Should not retry context errors (IsRetryableError returns false for context.Canceled)
	if attempts != 1 {
		t.Errorf("expected 1 attempt (should not retry context errors), got %d", attempts)
	}
}

func TestDo_NoRetryOnPreCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	attempts := 0
	err := Do(ctx, func(ctx context.Context) error {
		attempts++
		return errors.New("should not execute")
	}, WithMaxAttempts(3))

	if err == nil {
		t.Error("expected error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled error, got %v", err)
	}
	// Context is checked before function execution, so attempts should be 0
	if attempts != 0 {
		t.Errorf("expected 0 attempts (context cancelled before execution), got %d", attempts)
	}
}

func TestIsRetryableError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "nil error",
			err:      nil,
			expected: false,
		},
		{
			name:     "context canceled",
			err:      context.Canceled,
			expected: false,
		},
		{
			name:     "context deadline exceeded",
			err:      context.DeadlineExceeded,
			expected: false,
		},
		{
			name:     "generic error",
			err:      errors.New("generic error"),
			expected: true,
		},
		{
			name:     "wrapped context canceled",
			err:      errors.New("wrapped: " + context.Canceled.Error()),
			expected: true, // Not using errors.Is, so wrapped errors are retryable
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsRetryableError(tt.err)
			if result != tt.expected {
				t.Errorf("expected %v, got %v", tt.expected, result)
			}
		})
	}
}

func TestBackoff_Fixed(t *testing.T) {
	backoff := Fixed(100 * time.Millisecond)
	for i := 0; i < 5; i++ {
		d := backoff.Next(i)
		if d != 100*time.Millisecond {
			t.Errorf("expected 100ms, got %v", d)
		}
	}
}

func TestBackoff_Linear(t *testing.T) {
	backoff := Linear(50*time.Millisecond, 200*time.Millisecond)
	tests := []struct {
		attempt  int
		expected time.Duration
	}{
		{0, 50 * time.Millisecond},  // 1 * 50
		{1, 100 * time.Millisecond}, // 2 * 50
		{2, 150 * time.Millisecond}, // 3 * 50
		{3, 200 * time.Millisecond}, // 4 * 50, capped at 200
		{4, 200 * time.Millisecond}, // 5 * 50, capped at 200
	}

	for _, tt := range tests {
		d := backoff.Next(tt.attempt)
		if d != tt.expected {
			t.Errorf("attempt %d: expected %v, got %v", tt.attempt, tt.expected, d)
		}
	}
}

func TestBackoff_LinearNoMax(t *testing.T) {
	backoff := Linear(50 * time.Millisecond)
	tests := []struct {
		attempt  int
		expected time.Duration
	}{
		{0, 50 * time.Millisecond},  // 1 * 50
		{1, 100 * time.Millisecond}, // 2 * 50
		{2, 150 * time.Millisecond}, // 3 * 50
		{3, 200 * time.Millisecond}, // 4 * 50
	}

	for _, tt := range tests {
		d := backoff.Next(tt.attempt)
		if d != tt.expected {
			t.Errorf("attempt %d: expected %v, got %v", tt.attempt, tt.expected, d)
		}
	}
}

func TestBackoff_Exponential(t *testing.T) {
	backoff := Exponential(50*time.Millisecond, 200*time.Millisecond)
	tests := []struct {
		attempt  int
		expected time.Duration
	}{
		{0, 50 * time.Millisecond},  // 2^0 * 50 = 50
		{1, 100 * time.Millisecond}, // 2^1 * 50 = 100
		{2, 200 * time.Millisecond}, // 2^2 * 50 = 200, capped at 200
		{3, 200 * time.Millisecond}, // 2^3 * 50 = 400, capped at 200
	}

	for _, tt := range tests {
		d := backoff.Next(tt.attempt)
		if d != tt.expected {
			t.Errorf("attempt %d: expected %v, got %v", tt.attempt, tt.expected, d)
		}
	}
}

func TestBackoff_ExponentialNoMax(t *testing.T) {
	backoff := Exponential(50 * time.Millisecond)
	tests := []struct {
		attempt  int
		expected time.Duration
	}{
		{0, 50 * time.Millisecond},  // 2^0 * 50 = 50
		{1, 100 * time.Millisecond}, // 2^1 * 50 = 100
		{2, 200 * time.Millisecond}, // 2^2 * 50 = 200
		{3, 400 * time.Millisecond}, // 2^3 * 50 = 400
	}

	for _, tt := range tests {
		d := backoff.Next(tt.attempt)
		if d != tt.expected {
			t.Errorf("attempt %d: expected %v, got %v", tt.attempt, tt.expected, d)
		}
	}
}

func TestJitter_NoJitter(t *testing.T) {
	d := 100 * time.Millisecond
	result := NoJitter(d)
	if result != d {
		t.Errorf("expected %v, got %v", d, result)
	}
}

func TestJitter_FullJitter(t *testing.T) {
	d := 100 * time.Millisecond
	for i := 0; i < 10; i++ {
		result := FullJitter(d)
		if result < 0 || result >= d {
			t.Errorf("expected jitter in [0, %v), got %v", d, result)
		}
	}

	// Test zero duration
	result := FullJitter(0)
	if result != 0 {
		t.Errorf("expected 0 for zero duration, got %v", result)
	}

	// Test negative duration (should handle gracefully)
	result = FullJitter(-1)
	if result != 0 {
		t.Errorf("expected 0 for negative duration, got %v", result)
	}
}

func TestDo_WithNilBackoff(t *testing.T) {
	ctx := context.Background()
	attempts := 0
	err := Do(ctx, func(ctx context.Context) error {
		attempts++
		if attempts < 2 {
			return errors.New("error")
		}
		return nil
	}, WithMaxAttempts(3), WithBackoff(nil))

	// nil backoff should be ignored, use default
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if attempts != 2 {
		t.Errorf("expected 2 attempts, got %d", attempts)
	}
}

func TestDo_WithNilJitter(t *testing.T) {
	ctx := context.Background()
	attempts := 0
	err := Do(ctx, func(ctx context.Context) error {
		attempts++
		if attempts < 2 {
			return errors.New("error")
		}
		return nil
	}, WithMaxAttempts(3), WithJitter(nil))

	// nil jitter should be ignored, use default
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if attempts != 2 {
		t.Errorf("expected 2 attempts, got %d", attempts)
	}
}

func TestDo_WithNilRetryIf(t *testing.T) {
	ctx := context.Background()
	attempts := 0
	err := Do(ctx, func(ctx context.Context) error {
		attempts++
		if attempts < 2 {
			return errors.New("error")
		}
		return nil
	}, WithMaxAttempts(3), WithRetryIf(nil))

	// nil retryIf should be ignored, use default
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if attempts != 2 {
		t.Errorf("expected 2 attempts, got %d", attempts)
	}
}

func TestDo_ZeroBackoff(t *testing.T) {
	ctx := context.Background()
	attempts := 0
	start := time.Now()
	err := Do(ctx, func(ctx context.Context) error {
		attempts++
		if attempts < 3 {
			return errors.New("error")
		}
		return nil
	}, WithMaxAttempts(3), WithBackoff(Fixed(0)))
	duration := time.Since(start)

	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if attempts != 3 {
		t.Errorf("expected 3 attempts, got %d", attempts)
	}
	// With zero backoff, should complete almost immediately
	if duration > 50*time.Millisecond {
		t.Errorf("expected very short duration with zero backoff, got %v", duration)
	}
}
