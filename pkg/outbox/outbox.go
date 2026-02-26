// Copyright 2026 Arcentra Authors.
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

package outbox

import (
	"context"
	"sync"
	"time"

	"github.com/arcentrix/arcentra/pkg/safe"
	"github.com/bytedance/sonic"
)

// Outbox provides WAL-based outbox for reliable event sending.
type Outbox struct {
	wal    *WAL
	sender Sender
	cfg    *Config
	mu     sync.Mutex
	closed bool
	eg     sync.WaitGroup
	ctx    context.Context
	cancel context.CancelFunc
}

// NewOutbox creates an Outbox with the given config and sender.
func NewOutbox(cfg Config, sender Sender) (*Outbox, error) {
	cfg.SetDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	wal, err := NewWAL(&cfg)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	o := &Outbox{
		wal:    wal,
		sender: sender,
		cfg:    &cfg,
		ctx:    ctx,
		cancel: cancel,
	}
	o.eg.Add(1)
	safe.Go(o.runSender)
	return o, nil
}

// Append appends an event to the WAL. Returns the assigned seq.
func (o *Outbox) Append(ctx context.Context, payload []byte) (uint64, error) {
	o.mu.Lock()
	if o.closed {
		o.mu.Unlock()
		return 0, ErrClosed
	}
	o.mu.Unlock()
	r := &Record{
		Type:    RecordTypeEvent,
		Codec:   CodecJSON,
		Payload: payload,
	}
	return o.wal.Append(ctx, r)
}

// AppendMap appends an event from a map (e.g. CloudEvents) to the WAL.
func (o *Outbox) AppendMap(ctx context.Context, payload map[string]any) (uint64, error) {
	data, err := sonic.Marshal(payload)
	if err != nil {
		return 0, err
	}
	return o.Append(ctx, data)
}

func (o *Outbox) runSender() {
	defer o.eg.Done()
	ticker := time.NewTicker(o.cfg.SendInterval)
	defer ticker.Stop()
	for {
		select {
		case <-o.ctx.Done():
			return
		case <-ticker.C:
			o.sendBatch()
		}
	}
}

func (o *Outbox) sendBatch() {
	lastAcked, err := o.wal.Commit().Read()
	if err != nil {
		return
	}
	flushedSeq := o.wal.FlushedSeq()
	if flushedSeq <= lastAcked {
		return
	}
	recs, err := o.wal.ReadRecords(lastAcked, flushedSeq, o.cfg.SendBatchSize)
	if err != nil || len(recs) == 0 {
		return
	}
	events := make([]Event, 0, len(recs))
	for _, r := range recs {
		e, err := RecordToEvent(r, o.cfg.AgentId, o.cfg.PipelineId)
		if err != nil {
			continue
		}
		events = append(events, e)
	}
	if len(events) == 0 {
		return
	}
	result, err := o.sender.Send(o.ctx, events)
	if err != nil {
		return
	}
	newAcked := result.LastSeq
	if newAcked > flushedSeq {
		newAcked = flushedSeq
	}
	if newAcked > lastAcked {
		_ = o.wal.Commit().Write(newAcked)
		_ = o.wal.DeleteSegmentsUpTo(newAcked)
	}
}

// Close stops the outbox and waits for cleanup.
func (o *Outbox) Close() error {
	o.mu.Lock()
	if o.closed {
		o.mu.Unlock()
		return nil
	}
	o.closed = true
	o.mu.Unlock()
	o.cancel()
	o.eg.Wait()
	return o.wal.Close()
}
