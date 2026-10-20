// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package taskqueue

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func rerunIncarnationBinding() rerunAdmissionBinding {
	return rerunAdmissionBinding{AgentID: "agent", PipelineID: "pipeline", RunID: uuid.NewString(), JobRunID: uuid.NewString(), GateID: uuid.NewString(), ScheduleID: uuid.NewString(), SealID: uuid.NewString(), SealDigest: "v1:" + strings.Repeat("a", 64), PayloadDigest: "v1:" + strings.Repeat("b", 64)}
}
func inspectIncarnationClaim(t *testing.T, m *rerunIncarnation, h *rerunAdmissionHandle) (string, [32]byte) {
	t.Helper()
	var claim string
	var nonceHash [32]byte
	require.NoError(t, m.withClaim(h, func(r rerunIncarnationRequest) error {
		return r.withNonce(func(id string, nonce []byte) error { claim = id; nonceHash = sha256.Sum256(nonce); return nil })
	}))
	return claim, nonceHash
}
func verifyIncarnationReceipt(m *rerunIncarnation, h *rerunAdmissionHandle, b rerunAdmissionBinding, claim string) error {
	return m.withClaim(h, func(r rerunIncarnationRequest) error { return m.verifyReceipt(h, r, b, claim) })
}
func TestRerunIncarnationDuplicateClaimAndOnceOnlyStart(t *testing.T) {
	m, err := newRerunIncarnation(2)
	require.NoError(t, err)
	binding := rerunIncarnationBinding()
	h, err := m.reserve(binding)
	require.NoError(t, err)
	claim, nonce := inspectIncarnationClaim(t, m, h)
	// Lost response has no new identity and does not grant startup.
	require.ErrorIs(t, m.withClaim(h, func(r rerunIncarnationRequest) error { return errors.New("transport lost") }), errRerunIncarnationUnconfirmed)
	require.ErrorIs(t, m.takeStart(h), errRerunIncarnationBusy)
	other, err := m.reserve(binding)
	require.NoError(t, err)
	same, sameNonce := inspectIncarnationClaim(t, m, other)
	require.Equal(t, claim, same)
	require.Equal(t, nonce, sameNonce)
	require.NoError(t, verifyIncarnationReceipt(m, h, binding, claim))
	var starts atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			handle, e := m.reserve(binding)
			if e == nil && m.takeStart(handle) == nil {
				starts.Add(1)
			}
		}()
	}
	wg.Wait()
	require.Equal(t, int32(1), starts.Load())
	require.NoError(t, m.finish(h, false))
	require.ErrorIs(t, m.takeStart(other), errRerunIncarnationStarted)
	require.ErrorIs(t, m.withClaim(other, func(r rerunIncarnationRequest) error { t.Error("post-start fetch"); return nil }), errRerunIncarnationStarted)
	require.NoError(t, m.finish(h, true))
	require.NoError(t, m.finish(h, false))
	require.Equal(t, rerunTerminalKnown, h.entry.phase)
	require.ErrorIs(t, m.takeStart(h), errRerunIncarnationStarted)
}
func TestRerunIncarnationConcurrentClaimAndCallbackLifetime(t *testing.T) {
	m, err := newRerunIncarnation(1)
	require.NoError(t, err)
	binding := rerunIncarnationBinding()
	h, err := m.reserve(binding)
	require.NoError(t, err)
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	var retained rerunIncarnationRequest
	go func() {
		done <- m.withClaim(h, func(r rerunIncarnationRequest) error {
			retained = r
			close(entered)
			<-release
			return r.withNonce(func(id string, nonce []byte) error { return m.verifyReceipt(h, r, binding, id) })
		})
	}()
	<-entered
	require.ErrorIs(t, m.withClaim(h, func(r rerunIncarnationRequest) error { t.Error("duplicate RPC"); return nil }), errRerunIncarnationBusy)
	require.ErrorIs(t, m.takeStart(h), errRerunIncarnationBusy)
	close(release)
	require.NoError(t, <-done)
	require.ErrorIs(t, retained.withNonce(func(string, []byte) error { t.Error("expired nonce"); return nil }), errRerunIncarnationInvalid)
	require.NoError(t, m.takeStart(h))
}
func TestRerunIncarnationCapacityAndBindingRejectBeforeClaim(t *testing.T) {
	m, err := newRerunIncarnation(1)
	require.NoError(t, err)
	binding := rerunIncarnationBinding()
	h, err := m.reserve(binding)
	require.NoError(t, err)
	for _, mutate := range []func(*rerunAdmissionBinding){func(b *rerunAdmissionBinding) { b.AgentID = "other" }, func(b *rerunAdmissionBinding) { b.SealID = uuid.NewString() }, func(b *rerunAdmissionBinding) { b.PayloadDigest = "v1:" + strings.Repeat("c", 64) }, func(b *rerunAdmissionBinding) { b.SealDigest = "v1:" + strings.Repeat("c", 64) }, func(b *rerunAdmissionBinding) { b.RunID = uuid.NewString() }, func(b *rerunAdmissionBinding) { b.ScheduleID = uuid.NewString() }, func(b *rerunAdmissionBinding) { b.GateID = uuid.NewString() }, func(b *rerunAdmissionBinding) { b.PipelineID = "other" }} {
		bad := binding
		mutate(&bad)
		_, err := m.reserve(bad)
		require.ErrorIs(t, err, errRerunIncarnationInvalid)
	}
	_, err = m.reserve(rerunIncarnationBinding())
	require.ErrorIs(t, err, errRerunIncarnationCapacity)
	claim, _ := inspectIncarnationClaim(t, m, h)
	require.Error(t, verifyIncarnationReceipt(m, h, binding, uuid.NewString()))
	require.NoError(t, verifyIncarnationReceipt(m, h, binding, claim))
	require.NoError(t, m.takeStart(h))
	require.NoError(t, m.finish(h, true))
	_, err = m.reserve(rerunIncarnationBinding())
	require.ErrorIs(t, err, errRerunIncarnationCapacity)
	repeated, err := m.reserve(binding)
	require.NoError(t, err)
	require.ErrorIs(t, m.takeStart(repeated), errRerunIncarnationStarted)
}
func TestRerunIncarnationRestartCannotRecoverNonceOrReceipt(t *testing.T) {
	for _, phase := range []string{"lost response before journal", "receipt before start", "start owned before journal", "unknown after start"} {
		t.Run(phase, func(t *testing.T) {
			old, err := newRerunIncarnation(1)
			require.NoError(t, err)
			binding := rerunIncarnationBinding()
			h, err := old.reserve(binding)
			require.NoError(t, err)
			claim, nonce := inspectIncarnationClaim(t, old, h)
			if phase != "lost response before journal" {
				require.NoError(t, verifyIncarnationReceipt(old, h, binding, claim))
			}
			if phase == "start owned before journal" || phase == "unknown after start" {
				require.NoError(t, old.takeStart(h))
			}
			if phase == "unknown after start" {
				require.NoError(t, old.finish(h, false))
			}
			fresh, err := newRerunIncarnation(1)
			require.NoError(t, err)
			next, err := fresh.reserve(binding)
			require.NoError(t, err)
			newClaim, newNonce := inspectIncarnationClaim(t, fresh, next)
			require.NotEqual(t, nonce, newNonce)
			require.NotEqual(t, claim, newClaim)
			require.Error(t, verifyIncarnationReceipt(fresh, next, binding, claim))
			require.Error(t, verifyIncarnationReceipt(fresh, h, binding, claim))
			require.Error(t, fresh.takeStart(next))
			// Server must deny the new nonce/claim for the already-claimed Gate. This
			// primitive has no network authority or journal import method.
		})
	}
}
func TestRerunIncarnationNeverFormatsNonceAndSanitizesErrors(t *testing.T) {
	canary := bytes.Repeat([]byte{0x71}, 32)
	entropy := append(append([]byte(nil), canary...), bytes.Repeat([]byte{0x42}, 32)...)
	m, err := newRerunIncarnationFrom(1, bytes.NewReader(entropy))
	require.NoError(t, err)
	h, err := m.reserve(rerunIncarnationBinding())
	require.NoError(t, err)
	require.ErrorIs(t, m.withClaim(h, func(r rerunIncarnationRequest) error {
		for _, value := range []any{m, *m, h, *h, r, r.window, *r.window} {
			for _, format := range []string{"%v", "%+v", "%#v", "%x", "%s"} {
				text := fmt.Sprintf(format, value)
				require.NotContains(t, text, string(canary))
				require.NotContains(t, text, hex.EncodeToString(canary))
				require.NotContains(t, text, "113 113")
			}
			body, err := json.Marshal(value)
			require.NoError(t, err)
			require.Equal(t, "{}", string(body))
		}
		return errors.New(string(canary))
	}), errRerunIncarnationUnconfirmed)
	for _, capacity := range []int{-1, 0, 4097} {
		_, err := newRerunIncarnation(capacity)
		require.Error(t, err)
	}
	_, err = newRerunIncarnationFrom(1, bytes.NewReader(nil))
	require.Error(t, err)
	m, err = newRerunIncarnationFrom(1, bytes.NewReader(canary))
	require.NoError(t, err)
	_, err = m.reserve(rerunIncarnationBinding())
	require.Error(t, err)
	require.Empty(t, m.entries)
}

func TestRerunIncarnationConcurrentReservationHasOneIdentity(t *testing.T) {
	m, err := newRerunIncarnation(1)
	require.NoError(t, err)
	binding := rerunIncarnationBinding()
	handles := make(chan *rerunAdmissionHandle, 32)
	errs := make(chan error, 32)
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); h, e := m.reserve(binding); handles <- h; errs <- e }()
	}
	wg.Wait()
	close(handles)
	close(errs)
	for e := range errs {
		require.NoError(t, e)
	}
	var first *rerunIncarnationEntry
	for h := range handles {
		if first == nil {
			first = h.entry
		}
		require.Same(t, first, h.entry)
	}
	require.Len(t, m.entries, 1)
}

func TestRerunIncarnationFailedAttemptDiscardsStagedAndPriorVerification(t *testing.T) {
	for _, priorVerified := range []bool{false, true} {
		for _, panics := range []bool{false, true} {
			t.Run(fmt.Sprintf("prior=%t/panic=%t", priorVerified, panics), func(t *testing.T) {
				m, err := newRerunIncarnation(1)
				require.NoError(t, err)
				binding := rerunIncarnationBinding()
				h, err := m.reserve(binding)
				require.NoError(t, err)
				claim, nonce := inspectIncarnationClaim(t, m, h)
				if priorVerified {
					require.NoError(t, verifyIncarnationReceipt(m, h, binding, claim))
				}
				var retained rerunIncarnationRequest
				run := func() {
					err = m.withClaim(h, func(r rerunIncarnationRequest) error {
						retained = r
						var wg sync.WaitGroup
						results := make(chan error, 16)
						for i := 0; i < 16; i++ {
							wg.Add(1)
							go func() { defer wg.Done(); results <- m.verifyReceipt(h, r, binding, claim) }()
						}
						wg.Wait()
						close(results)
						for result := range results {
							require.NoError(t, result)
						}
						require.ErrorIs(t, m.takeStart(h), errRerunIncarnationBusy)
						if panics {
							panic("synthetic callback panic")
						}
						return errors.New("revoked or lost response")
					})
				}
				if panics {
					require.Panics(t, run)
				} else {
					run()
					require.ErrorIs(t, err, errRerunIncarnationUnconfirmed)
				}
				require.Equal(t, rerunClaimPending, h.entry.phase)
				require.False(t, h.entry.inFlight)
				require.Nil(t, h.entry.requestWindow)
				require.False(t, h.entry.receiptStaged)
				require.ErrorIs(t, m.takeStart(h), errRerunIncarnationBusy)
				require.ErrorIs(t, retained.withNonce(func(string, []byte) error { t.Error("nonce survived failed callback"); return nil }), errRerunIncarnationInvalid)
				// Old callback cannot validate the next attempt, even for identical receipt IDs.
				require.NoError(t, m.withClaim(h, func(current rerunIncarnationRequest) error {
					require.ErrorIs(t, m.verifyReceipt(h, retained, binding, claim), errRerunIncarnationInvalid)
					return current.withNonce(func(id string, value []byte) error {
						require.Equal(t, claim, id)
						require.Equal(t, nonce, sha256.Sum256(value))
						return m.verifyReceipt(h, current, binding, id)
					})
				}))
				require.NoError(t, m.takeStart(h))
				require.NoError(t, m.finish(h, false))
				require.ErrorIs(t, m.withClaim(h, func(r rerunIncarnationRequest) error { t.Error("reset start-owned"); return nil }), errRerunIncarnationStarted)
			})
		}
	}
}
