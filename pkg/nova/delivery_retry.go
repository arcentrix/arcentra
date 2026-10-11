package nova

import (
	"context"
	"errors"
	"time"
)

func retryDelivery(ctx context.Context, operation func() error) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := operation(); err == nil {
			return nil
		} else {
			var permanent permanentDeliveryError
			if errors.As(err, &permanent) {
				return err
			}
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// permanentDeliveryError 表示无法重试解码的毒消息，应进入死信主题
type permanentDeliveryError struct{ cause error }

func (e permanentDeliveryError) Error() string { return e.cause.Error() }
func (e permanentDeliveryError) Unwrap() error { return e.cause }
