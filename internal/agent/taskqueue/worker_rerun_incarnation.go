// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package taskqueue

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"

	"github.com/arcentrix/arcentra/pkg/sourceprovenance"
	taskpayload "github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/google/uuid"
)

var (
	errRerunIncarnationInvalid     = errors.New("rerun incarnation request unavailable")
	errRerunIncarnationCapacity    = errors.New("rerun incarnation capacity exhausted before admission")
	errRerunIncarnationUnconfirmed = errors.New("rerun admission outcome unconfirmed; inspect exact claim")
	errRerunIncarnationBusy        = errors.New("rerun admission already in progress")
	errRerunIncarnationStarted     = errors.New("rerun execution already owned; inspect existing outcome")
)

// Before calling withClaim, the private handler MUST verify
// ControllerAuthenticated and confidential RPC
// transport. It must validate every server receipt/payload before verifyReceipt.
// No nonce, claim import, persistent storage or restart-resume API is provided.
type rerunIncarnation struct {
	mu       *sync.Mutex
	nonce    [32]byte
	entropy  io.Reader
	capacity int
	entries  map[string]*rerunIncarnationEntry
}
type rerunAdmissionBinding struct {
	AgentID, PipelineID, RunID, JobRunID, GateID, ScheduleID, SealID string
	SealDigest, PayloadDigest                                        string
	ArtifactStorageID, ArtifactStorageFingerprint                    string
}
type rerunIncarnationPhase uint8

const (
	rerunClaimPending rerunIncarnationPhase = iota + 1
	rerunReceiptVerified
	rerunStartOwned
	rerunTerminalUnknown
	rerunTerminalKnown
)

type rerunIncarnationEntry struct {
	binding         rerunAdmissionBinding
	claimID         string
	phase           rerunIncarnationPhase
	inFlight        bool
	receiptStaged   bool
	requestWindow   *rerunNonceWindow
	startToken      *rerunStartToken
	startConsumed   bool
	artifactStorage *rerunArtifactStorage
}
type rerunAdmissionHandle struct {
	owner *rerunIncarnation
	entry *rerunIncarnationEntry
}

// The request's only nonce access is a scoped callback. Formatting never prints
// the nonce, even with %#v, %+v or byte-oriented verbs.
type rerunIncarnationRequest struct{ window *rerunNonceWindow }
type rerunNonceWindow struct {
	mu      *sync.Mutex
	claimID string
	nonce   []byte
	active  *atomic.Bool
}

func (rerunIncarnation) Format(s fmt.State, _ rune) {
	_, _ = io.WriteString(s, "<private rerun incarnation>")
}
func (rerunAdmissionHandle) Format(s fmt.State, _ rune) {
	_, _ = io.WriteString(s, "<private rerun admission>")
}
func (rerunNonceWindow) Format(s fmt.State, _ rune) {
	_, _ = io.WriteString(s, "<private rerun nonce window>")
}
func (rerunIncarnationRequest) Format(s fmt.State, _ rune) {
	_, _ = io.WriteString(s, "<private rerun claim request>")
}
func (r rerunIncarnationRequest) withNonce(fn func(string, []byte) error) error {
	if fn == nil || r.window == nil {
		return errRerunIncarnationInvalid
	}
	r.window.mu.Lock()
	defer r.window.mu.Unlock()
	if r.window.active == nil || !r.window.active.Load() {
		return errRerunIncarnationInvalid
	}
	return fn(r.window.claimID, r.window.nonce)
}
func newRerunIncarnation(capacity int) (*rerunIncarnation, error) {
	return newRerunIncarnationFrom(capacity, rand.Reader)
}
func newRerunIncarnationFrom(capacity int, entropy io.Reader) (*rerunIncarnation, error) {
	if capacity < 1 || capacity > 4096 || entropy == nil {
		return nil, errRerunIncarnationInvalid
	}
	m := &rerunIncarnation{mu: &sync.Mutex{}, capacity: capacity, entropy: entropy, entries: map[string]*rerunIncarnationEntry{}}
	if _, err := io.ReadFull(entropy, m.nonce[:]); err != nil {
		return nil, errRerunIncarnationInvalid
	}
	return m, nil
}
func rerunLegacyIdentity(value string) bool {
	if len(value) < 1 || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}
func (b rerunAdmissionBinding) valid() bool {
	if taskpayload.ValidateRerunArtifactStorage(b.ArtifactStorageID, b.ArtifactStorageFingerprint) != nil || !rerunLegacyIdentity(b.AgentID) || !rerunLegacyIdentity(b.PipelineID) || !sourceprovenance.ValidBindingDigest(b.SealDigest) || !sourceprovenance.ValidBindingDigest(b.PayloadDigest) {
		return false
	}
	seen := map[string]bool{}
	for _, id := range []string{b.RunID, b.JobRunID, b.GateID, b.ScheduleID, b.SealID} {
		u, err := uuid.Parse(id)
		if err != nil || u == uuid.Nil || u.String() != id || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}

// reserve consumes capacity before any admission attempt. Entries are never
// evicted, including terminal tombstones. Duplicate notifications reuse identity.
func (m *rerunIncarnation) reserve(binding rerunAdmissionBinding) (*rerunAdmissionHandle, error) {
	if m == nil || m.mu == nil || m.entries == nil || !binding.valid() {
		return nil, errRerunIncarnationInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if old := m.entries[binding.JobRunID]; old != nil {
		if old.binding != binding {
			return nil, errRerunIncarnationInvalid
		}
		return &rerunAdmissionHandle{m, old}, nil
	}
	if len(m.entries) >= m.capacity {
		return nil, errRerunIncarnationCapacity
	}
	// Gate/run/schedule/seal identities cannot be adopted by a different job.
	for _, old := range m.entries {
		if old.binding.RunID == binding.RunID || old.binding.GateID == binding.GateID || old.binding.ScheduleID == binding.ScheduleID || old.binding.SealID == binding.SealID {
			return nil, errRerunIncarnationInvalid
		}
	}
	claim, err := uuid.NewRandomFromReader(m.entropy)
	if err != nil {
		return nil, errRerunIncarnationInvalid
	}
	entry := &rerunIncarnationEntry{binding: binding, claimID: claim.String(), phase: rerunClaimPending}
	m.entries[binding.JobRunID] = entry
	return &rerunAdmissionHandle{m, entry}, nil
}
func (m *rerunIncarnation) entryLocked(h *rerunAdmissionHandle) (*rerunIncarnationEntry, error) {
	if h == nil || h.owner != m || h.entry == nil || m.entries[h.entry.binding.JobRunID] != h.entry {
		return nil, errRerunIncarnationInvalid
	}
	return h.entry, nil
}

// withClaim serializes duplicate RPC attempts without holding the mutex during
// transport. Lost ACK leaves the SAME claim pending; it never allocates anew.
// The request and nonce slice are valid only until this callback returns.
func (m *rerunIncarnation) withClaim(h *rerunAdmissionHandle, fn func(rerunIncarnationRequest) error) error {
	if m == nil || m.mu == nil || m.entries == nil || fn == nil {
		return errRerunIncarnationInvalid
	}
	m.mu.Lock()
	entry, err := m.entryLocked(h)
	if err != nil {
		m.mu.Unlock()
		return err
	}
	if entry.phase >= rerunStartOwned {
		m.mu.Unlock()
		return errRerunIncarnationStarted
	}
	if entry.inFlight {
		m.mu.Unlock()
		return errRerunIncarnationBusy
	}
	entry.inFlight = true
	entry.phase = rerunClaimPending
	entry.receiptStaged = false
	request := rerunIncarnationRequest{&rerunNonceWindow{mu: &sync.Mutex{}, claimID: entry.claimID, nonce: append([]byte(nil), m.nonce[:]...), active: &atomic.Bool{}}}
	request.window.active.Store(true)
	entry.requestWindow = request.window
	m.mu.Unlock()
	succeeded := false
	defer func() {
		request.window.active.Store(false)
		request.window.mu.Lock()
		clear(request.window.nonce)
		request.window.mu.Unlock()
		m.mu.Lock()
		// A panic or any error leaves the same claim pending. Verification of
		// an earlier attempt cannot survive a failed/revoked fresh fetch.
		if succeeded && entry.receiptStaged {
			entry.phase = rerunReceiptVerified
		} else {
			entry.phase = rerunClaimPending
		}
		entry.receiptStaged = false
		entry.requestWindow = nil
		entry.inFlight = false
		m.mu.Unlock()
	}()
	if fn(request) != nil {
		return errRerunIncarnationUnconfirmed
	}
	succeeded = true
	return nil
}

// verifyReceipt stages a caller-validated exact receipt for this active request
// only. Successful callback completion commits it; errors/panics discard it.
func (m *rerunIncarnation) verifyReceipt(h *rerunAdmissionHandle, request rerunIncarnationRequest, binding rerunAdmissionBinding, claimID string) error {
	if m == nil || m.mu == nil || m.entries == nil {
		return errRerunIncarnationInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, err := m.entryLocked(h)
	if err != nil {
		return err
	}
	if !entry.inFlight || request.window == nil || request.window.active == nil || !request.window.active.Load() || entry.requestWindow != request.window || entry.binding != binding || entry.claimID != claimID {
		return errRerunIncarnationInvalid
	}
	entry.receiptStaged = true
	return nil
}

// takeStart MUST precede Allocate, checkout and all preparation. Errors after
// this CAS never restore admission/start eligibility, even in the same process.
func (m *rerunIncarnation) takeStart(h *rerunAdmissionHandle) error {
	if m == nil || m.mu == nil || m.entries == nil {
		return errRerunIncarnationInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, err := m.entryLocked(h)
	if err != nil {
		return err
	}
	if entry.phase >= rerunStartOwned {
		return errRerunIncarnationStarted
	}
	if entry.phase != rerunReceiptVerified || entry.inFlight {
		return errRerunIncarnationBusy
	}
	entry.phase = rerunStartOwned
	return nil
}
func (m *rerunIncarnation) finish(h *rerunAdmissionHandle, authoritativeTerminal bool) error {
	if m == nil || m.mu == nil || m.entries == nil {
		return errRerunIncarnationInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, err := m.entryLocked(h)
	if err != nil {
		return err
	}
	if entry.phase < rerunStartOwned {
		return errRerunIncarnationInvalid
	}
	if authoritativeTerminal {
		entry.phase = rerunTerminalKnown
	} else if entry.phase != rerunTerminalKnown {
		entry.phase = rerunTerminalUnknown
	}
	return nil
}
