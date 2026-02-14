package ringbuffer

import (
	"runtime"
	"time"
)

type WaitStrategy interface {
	Wait()
}

type YieldingWaitStrategy struct{}

func (y *YieldingWaitStrategy) Wait() {
	runtime.Gosched()
}

type SleepWaitStrategy struct {
	d time.Duration
}

func (s *SleepWaitStrategy) Wait() {
	time.Sleep(s.d)
}
