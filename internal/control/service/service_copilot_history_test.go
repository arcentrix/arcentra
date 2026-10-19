// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package service

import (
	"context"
	"encoding/json"
	"errors"
	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	cluster "github.com/arcentrix/arcentra/internal/shared/deployment"
	artifactutil "github.com/arcentrix/arcentra/pkg/artifact"
	"github.com/arcentrix/arcentra/pkg/retryapproval"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/authz"
	"github.com/arcentrix/arcentra/internal/control/copilot"
	"github.com/arcentrix/arcentra/internal/control/model"
	"gorm.io/gorm"
)

type historyProvider struct {
	mu     sync.Mutex
	calls  []copilot.Input
	answer string
	before func(context.Context)
	err    error
}

func (p *historyProvider) Analyze(ctx context.Context, in copilot.Input) (copilot.Output, error) {
	p.mu.Lock()
	p.calls = append(p.calls, in)
	p.mu.Unlock()
	if p.before != nil {
		p.before(ctx)
	}
	return copilot.Output{Text: p.answer}, p.err
}
func (p *historyProvider) count() int { p.mu.Lock(); defer p.mu.Unlock(); return len(p.calls) }

type historyRegistry struct {
	secretValues []string
	provider     *historyProvider
	binding      string
	disabled     bool
}

func (r *historyRegistry) Options(string) []copilot.ModelOption {
	return []copilot.ModelOption{{ProviderID: "test", Model: "model"}}
}
func (r *historyRegistry) Resolve(string, string, string) (copilot.Provider, error) {
	if r.disabled {
		return nil, copilot.ErrUnconfigured
	}
	return r.provider, nil
}
func (r *historyRegistry) Secrets() []string {
	if r.secretValues != nil {
		return r.secretValues
	}
	return []string{"provider-secret"}
}
func (r *historyRegistry) Binding(string, string, string) (string, error) {
	if r.disabled {
		return "", copilot.ErrUnconfigured
	}
	return r.binding, nil
}
func historyFixture(t *testing.T) (*CopilotService, *gorm.DB, *historyProvider, CopilotConversation, CopilotHistoryTurnRequest) {
	t.Helper()
	s, db, _, _, old := copilotSourceFixture(t)
	if err := db.AutoMigrate(&model.CopilotConversation{}, &model.CopilotTurn{}, &model.CopilotActionPlan{}); err != nil {
		t.Fatal(err)
	}
	db.Model(&model.PipelineRun{}).Where("run_id = ?", "run").Updates(map[string]any{"project_id": "p", "status": model.PipelineStatusFailed})
	db.Model(&model.Pipeline{}).Where("pipeline_id = ?", "pipe-run").Update("last_commit_sha", strings.Repeat("a", 40))
	if err := db.Create(&model.StepRunLog{StepRunID: "step", LineNumber: 1, Content: "failure registered-old-key step-old-value provider-secret"}).Error; err != nil {
		t.Fatal(err)
	}
	provider := &historyProvider{answer: "A hypothesis; registered-old-key provider-secret"}
	s.registry = &historyRegistry{provider: provider, binding: "fixture-generation-1"}
	s.ConfigureSourceContext(copilot.SourceContextConfig{})
	if err := s.ConfigureHistory(copilot.HistoryConfig{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	subject := authz.Subject{UserID: "owner"}
	c, err := s.CreateConversation(context.Background(), subject, "p", CopilotCreateConversationRequest{RunID: "run", RequestID: "create-request-123456", AcknowledgeRetention: true})
	if err != nil {
		t.Fatal(err)
	}
	old.Context = CopilotSelection{StepRunIDs: []string{"step"}}
	old.Question = "Why registered-old-key?"
	return s, db, provider, c, CopilotHistoryTurnRequest{CopilotRequest: old, HistoryTurnIDs: []string{}}
}
func historySend(t *testing.T, s *CopilotService, c CopilotConversation, in CopilotHistoryTurnRequest) CopilotTurn {
	t.Helper()
	ctx := context.Background()
	subject := authz.Subject{UserID: "owner"}
	p, err := s.PreviewConversationTurn(ctx, subject, "p", c.ConversationID, in)
	if err != nil {
		t.Fatal(err)
	}
	in.ContextDigest = p.Digest
	v, err := s.SendConversationTurn(ctx, subject, "p", c.ConversationID, in)
	if err != nil {
		t.Fatal(err)
	}
	if v.State != "completed" || !v.ContentAvailable {
		t.Fatalf("turn not durable: %+v", v)
	}
	return v
}
func TestCopilotHistoryCipherBindsIdentityAndFailsClosed(t *testing.T) {
	c, err := newCopilotHistoryCipher([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	in := copilotHistoryEnvelope{Schema: 1, Question: "redacted question", Answer: "real durable answer"}
	encrypted, err := c.seal("owner", "p", "c", "t", "turn", in)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encrypted), in.Answer) {
		t.Fatal("plaintext stored")
	}
	var out copilotHistoryEnvelope
	if err = c.open("owner", "p", "c", "t", "turn", encrypted, &out); err != nil || out.Answer != in.Answer {
		t.Fatal("round trip", err)
	}
	for _, ids := range [][]string{{"other", "p", "c", "t", "turn"}, {"owner", "other", "c", "t", "turn"}, {"owner", "p", "other", "t", "turn"}, {"owner", "p", "c", "other", "turn"}, {"owner", "p", "c", "t", "plan"}} {
		if c.open(ids[0], ids[1], ids[2], ids[3], ids[4], encrypted, &out) == nil {
			t.Fatal("swapped identity decrypted")
		}
	}
	wrong, _ := newCopilotHistoryCipher([]byte("11234567890123456789012345678901"))
	if wrong.open("owner", "p", "c", "t", "turn", encrypted, &out) == nil {
		t.Fatal("wrong key accepted")
	}
	if _, err := newCopilotHistoryCipher(nil); err == nil {
		t.Fatal("missing key accepted")
	}
}
func TestCopilotHistoryDurablePrivateAndExplicitSecondTurn(t *testing.T) {
	s, db, p, c, in := historyFixture(t)
	one := historySend(t, s, c, in)
	if p.count() != 1 {
		t.Fatal("provider count")
	}
	var row model.CopilotTurn
	if err := db.Where("turn_id = ?", one.TurnID).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if len(row.PayloadCiphertext) == 0 {
		t.Fatal("missing encrypted body")
	}
	for _, canary := range []string{"registered-old-key", "provider-secret", "Why"} {
		if strings.Contains(string(row.PayloadCiphertext), canary) {
			t.Fatal("readable ciphertext")
		}
	}
	second := in
	second.RequestID = "second-request-123456"
	second.Question = "Explain that hypothesis"
	second.Context = CopilotSelection{}
	second.HistoryTurnIDs = []string{one.TurnID}
	two := historySend(t, s, c, second)
	p.mu.Lock()
	prompt := p.calls[1].User
	p.mu.Unlock()
	if !strings.Contains(prompt, one.Text) || !strings.Contains(prompt, "historical-turn") || strings.Contains(prompt, "registered-old-key") {
		t.Fatal("wrong selected history", prompt)
	}
	// Reconstruct the service with the same managed key: actual bodies survive,
	// source context stays disabled and a disabled model does not block reads.
	if err := s.ConfigureHistory(copilot.HistoryConfig{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	s.registry.(*historyRegistry).disabled = true
	got, err := s.GetConversation(context.Background(), authz.Subject{UserID: "owner"}, "p", c.ConversationID, 0, 20)
	if err != nil || len(got.Turns) != 2 || got.Turns[1].Text != two.Text {
		t.Fatalf("restart %+v %v", got, err)
	}
	if _, err = s.GetConversation(context.Background(), authz.Subject{UserID: "intruder"}, "p", c.ConversationID, 0, 20); err == nil {
		t.Fatal("foreign content")
	}
	if _, err = s.GetConversation(context.Background(), authz.Subject{UserID: "owner"}, "other", c.ConversationID, 0, 20); err == nil {
		t.Fatal("cross project")
	}
	if p.count() != 2 {
		t.Fatal("history read replayed")
	}
}
func TestCopilotHistoryNoKeyNoTransmissionAndPreviewDrift(t *testing.T) {
	s, _, p, c, in := historyFixture(t)
	ctx := context.Background()
	owner := authz.Subject{UserID: "owner"}
	preview, err := s.PreviewConversationTurn(ctx, owner, "p", c.ConversationID, in)
	if err != nil {
		t.Fatal(err)
	}
	in.ContextDigest = preview.Digest
	s.registry.(*historyRegistry).binding = "changed"
	if _, err = s.SendConversationTurn(ctx, owner, "p", c.ConversationID, in); !errors.Is(err, ErrCopilotPreview) {
		t.Fatal("config drift", err)
	}
	if p.count() != 0 {
		t.Fatal("drift dispatched")
	}
	s.secrets.encryptKey = nil
	s.ConfigureHistory(copilot.HistoryConfig{Enabled: true})
	if _, err = s.CreateConversation(ctx, owner, "p", CopilotCreateConversationRequest{RunID: "run", RequestID: "new-create-12345678", AcknowledgeRetention: true}); !errors.Is(err, ErrCopilotHistoryUnavailable) {
		t.Fatal("key gate", err)
	}
	if p.count() != 0 {
		t.Fatal("no-key sent")
	}
}
func TestCopilotHistoryDuplicateCancellationAndUnknownNeverReplay(t *testing.T) {
	s, _, p, c, in := historyFixture(t)
	ctx := context.Background()
	owner := authz.Subject{UserID: "owner"}
	preview, err := s.PreviewConversationTurn(ctx, owner, "p", c.ConversationID, in)
	if err != nil {
		t.Fatal(err)
	}
	in.ContextDigest = preview.Digest
	got, err := s.SendConversationTurn(ctx, owner, "p", c.ConversationID, in)
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.SendConversationTurn(ctx, owner, "p", c.ConversationID, in)
	if err != nil || again.TurnID != got.TurnID || p.count() != 1 {
		t.Fatal("duplicate replay", err, p.count())
	}
	in.Question = "different"
	if _, err = s.SendConversationTurn(ctx, owner, "p", c.ConversationID, in); !errors.Is(err, ErrCopilotHistoryConflict) {
		t.Fatal("fingerprint", err)
	}
	in.RequestID = "cancel-request-123456"
	cancelled, err := s.CancelConversationTurn(ctx, owner, "p", c.ConversationID, in.RequestID)
	if err != nil || cancelled.State != "cancelled" {
		t.Fatal("pre-cancel", err)
	}
	again, err = s.SendConversationTurn(ctx, owner, "p", c.ConversationID, in)
	if err != nil || again.State != "cancelled" || p.count() != 1 {
		t.Fatal("pre-cancel replay", err)
	}
	in.RequestID = "unknown-request-123456"
	p.err = context.DeadlineExceeded
	preview, err = s.PreviewConversationTurn(ctx, owner, "p", c.ConversationID, in)
	if err != nil {
		t.Fatal(err)
	}
	in.ContextDigest = preview.Digest
	unknown, err := s.SendConversationTurn(ctx, owner, "p", c.ConversationID, in)
	if err != nil || unknown.State != "outcome_unknown" {
		t.Fatalf("unknown %+v %v", unknown, err)
	}
	_, err = s.SendConversationTurn(ctx, owner, "p", c.ConversationID, in)
	if err != nil || p.count() != 2 {
		t.Fatal("unknown replay", err)
	}
}
func TestCopilotHistoryPostProviderRevokeAndNewSecret(t *testing.T) {
	for _, kind := range []string{"revoke", "new-secret", "delete"} {
		t.Run(kind, func(t *testing.T) {
			s, db, p, c, in := historyFixture(t)
			p.answer = "newly-registered-canary hypothesis"
			p.before = func(context.Context) {
				switch kind {
				case "revoke":
					db.Model(&model.RoleGrant{}).Where("grant_id = ?", "grant").Update("is_enabled", 0)
				case "new-secret":
					encoded, _ := s.secrets.encrypt("newly-registered-canary")
					db.Create(&model.Secret{SecretID: "new-secret", Name: "new", SecretValue: encoded, Scope: "project", ScopeID: "p"})
				case "delete":
					if err := s.DeleteConversation(context.Background(), authz.Subject{UserID: "owner"}, "p", c.ConversationID, c.Version); err != nil {
						t.Error(err)
					}
				}
			}
			preview, err := s.PreviewConversationTurn(context.Background(), authz.Subject{UserID: "owner"}, "p", c.ConversationID, in)
			if err != nil {
				t.Fatal(err)
			}
			in.ContextDigest = preview.Digest
			out, err := s.SendConversationTurn(context.Background(), authz.Subject{UserID: "owner"}, "p", c.ConversationID, in)
			if kind == "new-secret" {
				if err != nil || !out.ContentAvailable || strings.Contains(out.Text, "newly-registered-canary") {
					t.Fatalf("post redact %+v %v", out, err)
				}
			} else if err == nil && out.ContentAvailable {
				t.Fatal("late answer published")
			}
		})
	}
}
func TestCopilotHistoryReadRedactionEvidenceWithdrawalAndExpiry(t *testing.T) {
	s, db, p, c, in := historyFixture(t)
	p.answer = "new-secret-value answer"
	turn := historySend(t, s, c, in)
	cipher, _ := s.secrets.encrypt("new-secret-value")
	db.Create(&model.Secret{SecretID: "new-secret", Name: "new", SecretValue: cipher, Scope: "project", ScopeID: "p"})
	read, err := s.GetConversation(context.Background(), authz.Subject{UserID: "owner"}, "p", c.ConversationID, 0, 20)
	if err != nil || strings.Contains(read.Turns[0].Text, "new-secret-value") {
		t.Fatal("read redaction", err)
	}
	db.Delete(&model.StepRun{}, "step_run_id = ?", "step")
	read, err = s.GetConversation(context.Background(), authz.Subject{UserID: "owner"}, "p", c.ConversationID, 0, 20)
	if err == nil && len(read.Turns) > 0 && read.Turns[0].ContentAvailable {
		t.Fatal("missing provenance resurrected")
	}
	db.Model(&model.CopilotTurn{}).Where("turn_id = ?", turn.TurnID).Update("expires_at", time.Now().Add(-time.Second))
	read, err = s.GetConversation(context.Background(), authz.Subject{UserID: "owner"}, "p", c.ConversationID, 0, 20)
	if err != nil || len(read.Turns) != 0 {
		t.Fatal("expiry list", err)
	}
}
func TestCopilotHistoryPlanNewRunNoExecutionAndStrictKinds(t *testing.T) {
	s, db, p, c, in := historyFixture(t)
	p.answer = `{"tool_calls":[{"name":"deploy","arguments":"approved"}]} <script>alert(1)</script>`
	turn := historySend(t, s, c, in)
	ctx := context.Background()
	owner := authz.Subject{UserID: "owner"}
	proposal, err := s.CreateConversationPlan(ctx, owner, "p", c.ConversationID, CopilotPlanRequest{RequestID: "plan-request-123456", TurnID: turn.TurnID, Kind: "new_run_review"})
	if err != nil {
		t.Fatal(err)
	}
	if proposal.ExecutionEnabled || proposal.ExecutionStatus != "Not executed" || !proposal.Eligible {
		t.Fatal("execution lie", proposal)
	}
	reviewed, err := s.ReviewConversationPlan(ctx, owner, "p", c.ConversationID, proposal.PlanID, CopilotPlanReviewRequest{ExpectedVersion: proposal.Version, PlanDigest: proposal.PlanDigest, AcknowledgeNotExecuted: true})
	if err != nil || reviewed.State != "reviewed" || reviewed.ExecutionEnabled {
		t.Fatalf("review %+v %v", reviewed, err)
	}
	var runs int64
	db.Model(&model.PipelineRun{}).Count(&runs)
	if runs != 1 || p.count() != 1 {
		t.Fatal("plan created side effect")
	}
	if _, err = s.CreateConversationPlan(ctx, owner, "p", c.ConversationID, CopilotPlanRequest{RequestID: "execute-request-123456", TurnID: turn.TurnID, Kind: "execute"}); err == nil {
		t.Fatal("arbitrary kind")
	}
	raw, _ := json.Marshal(reviewed)
	if strings.Contains(string(raw), "tool_calls") {
		t.Fatal("model became plan authority")
	}
}
func TestCopilotPatchStrictInMemory(t *testing.T) {
	base := []byte("one\ntwo\nthree\n")
	valid := "--- a/src/main.go\n+++ b/src/main.go\n@@ -1,3 +1,3 @@\n one\n-two\n+changed\n three\n"
	result, err := verifyCopilotPatch(base, "src/main.go", valid)
	if err != nil || string(result) != "one\nchanged\nthree\n" {
		t.Fatal("valid", err, string(result))
	}
	for _, bad := range []string{strings.Replace(valid, "-1,3", "-1,2", 1), strings.Replace(valid, "+1,3", "+2,3", 1), strings.Replace(valid, " two", " [REDACTED]", 1) + "--- a/other.go\n", strings.Replace(valid, "src/main.go", "../main.go", -1), "diff --git a/src/main.go b/src/main.go\n" + valid, strings.Replace(valid, "+changed", "+[REDACTED]", 1), strings.Replace(valid, "-two", "-wrong", 1)} {
		if _, err := verifyCopilotPatch(base, "src/main.go", bad); err == nil {
			t.Fatal("unsafe diff accepted", bad)
		}
	}
}

func TestCopilotHistorySelectedOrderSizeAndBodyDrift(t *testing.T) {
	s, db, p, c, in := historyFixture(t)
	p.answer = "First answer"
	first := historySend(t, s, c, in)
	in.RequestID = "second-answer-123456"
	in.Question = "Second question"
	p.answer = "Second answer"
	second := historySend(t, s, c, in)
	follow := in
	follow.RequestID = "follow-request-123456"
	follow.Question = "Compare"
	follow.Context = CopilotSelection{}
	follow.HistoryTurnIDs = []string{first.TurnID, second.TurnID}
	ctx := context.Background()
	owner := authz.Subject{UserID: "owner"}
	preview, err := s.PreviewConversationTurn(ctx, owner, "p", c.ConversationID, follow)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Index(preview.Text, "First answer") > strings.Index(preview.Text, "Second answer") {
		t.Fatal("selection order lost")
	}
	follow.HistoryTurnIDs = []string{second.TurnID, first.TurnID}
	other, err := s.PreviewConversationTurn(ctx, owner, "p", c.ConversationID, follow)
	if err != nil || other.Digest == preview.Digest {
		t.Fatal("order missing from digest", err)
	}
	follow.ContextDigest = preview.Digest
	if _, err = s.SendConversationTurn(ctx, owner, "p", c.ConversationID, follow); !errors.Is(err, ErrCopilotPreview) {
		t.Fatal("stale order sent", err)
	}
	if p.count() != 2 {
		t.Fatal("stale order contacted provider")
	}
	// Current redaction changes selected bytes even though the encrypted row is immutable.
	follow.RequestID = "body-drift-12345678"
	follow.HistoryTurnIDs = []string{first.TurnID}
	preview, err = s.PreviewConversationTurn(ctx, owner, "p", c.ConversationID, follow)
	if err != nil {
		t.Fatal(err)
	}
	cipher, _ := s.secrets.encrypt("First answer")
	db.Create(&model.Secret{SecretID: "drift-secret", Name: "new", SecretValue: cipher, Scope: "project", ScopeID: "p"})
	follow.ContextDigest = preview.Digest
	if _, err = s.SendConversationTurn(ctx, owner, "p", c.ConversationID, follow); !errors.Is(err, ErrCopilotPreview) {
		t.Fatal("body drift sent", err)
	}
	in.RequestID = "large-answer-123456"
	p.answer = strings.Repeat("x", 8192)
	large := historySend(t, s, c, in)
	follow.RequestID = "too-large-12345678"
	follow.HistoryTurnIDs = []string{large.TurnID}
	if _, err = s.PreviewConversationTurn(ctx, owner, "p", c.ConversationID, follow); !errors.Is(err, ErrCopilotHistoryTooLarge) {
		t.Fatal("oversize selection silently truncated", err)
	}
}
func TestCopilotHistoryCrossInstanceCancelSuppressesLateAnswer(t *testing.T) {
	s, _, p, c, in := historyFixture(t)
	started := make(chan struct{})
	p.before = func(ctx context.Context) { close(started); <-ctx.Done() }
	ctx := context.Background()
	owner := authz.Subject{UserID: "owner"}
	preview, err := s.PreviewConversationTurn(ctx, owner, "p", c.ConversationID, in)
	if err != nil {
		t.Fatal(err)
	}
	in.ContextDigest = preview.Digest
	type response struct {
		v   CopilotTurn
		err error
	}
	done := make(chan response, 1)
	go func() { v, e := s.SendConversationTurn(ctx, owner, "p", c.ConversationID, in); done <- response{v, e} }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("provider did not start")
	}
	other := NewCopilotService(s.repos, s.authorizer, s.artifacts, nil, s.secrets)
	other.registry = s.registry
	if err = other.ConfigureHistory(copilot.HistoryConfig{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err = other.CancelConversationTurn(ctx, owner, "p", c.ConversationID, in.RequestID); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		if got.err == nil && (got.v.ContentAvailable || got.v.State != "cancelled") {
			t.Fatalf("late answer escaped %+v", got.v)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("durable cancel was not observed")
	}
	if p.count() != 1 {
		t.Fatal("cancel replay")
	}
}
func TestCopilotHistoryConcurrentDuplicateDoesNotCallProviderTwice(t *testing.T) {
	s, _, p, c, in := historyFixture(t)
	started, release := make(chan struct{}), make(chan struct{})
	p.before = func(context.Context) { close(started); <-release }
	ctx := context.Background()
	owner := authz.Subject{UserID: "owner"}
	preview, err := s.PreviewConversationTurn(ctx, owner, "p", c.ConversationID, in)
	if err != nil {
		t.Fatal(err)
	}
	in.ContextDigest = preview.Digest
	done := make(chan error, 1)
	go func() { _, e := s.SendConversationTurn(ctx, owner, "p", c.ConversationID, in); done <- e }()
	<-started
	duplicate, err := s.SendConversationTurn(ctx, owner, "p", c.ConversationID, in)
	if err != nil || duplicate.State != "dispatching" || p.count() != 1 {
		t.Fatal("inflight duplicate", err, duplicate.State, p.count())
	}
	close(release)
	if err = <-done; err != nil {
		t.Fatal(err)
	}
}
func TestCopilotHistoryCipherSwapUnavailable(t *testing.T) {
	s, db, _, c, in := historyFixture(t)
	one := historySend(t, s, c, in)
	in.RequestID = "second-swap-123456"
	two := historySend(t, s, c, in)
	var first, second model.CopilotTurn
	db.Where("turn_id = ?", one.TurnID).First(&first)
	db.Where("turn_id = ?", two.TurnID).First(&second)
	db.Model(&second).Update("payload_ciphertext", first.PayloadCiphertext)
	out, err := s.GetConversationTurn(context.Background(), authz.Subject{UserID: "owner"}, "p", c.ConversationID, two.RequestID)
	if err != nil || out.ContentAvailable || out.Text != "" || out.UnavailableReason != "history_content_unavailable" {
		t.Fatalf("swapped content %+v %v", out, err)
	}
}
func TestCopilotHistorySourcePatchPureReviewAndWithdrawal(t *testing.T) {
	s, _, p, c, in := historyFixture(t)
	if err := s.ConfigureSourceContext(copilot.SourceContextConfig{Enabled: true, AllowedProjectIDs: []string{"p"}}); err != nil {
		t.Fatal(err)
	}
	reader := &copilotFixtureReader{content: "package main\nfunc old() {}\n"}
	s.sourceReader = reader
	in.Context = CopilotSelection{SourceFiles: []CopilotSourceFile{{JobRunID: "job", Path: "src/main.go"}}}
	p.answer = "Proposed source change only"
	turn := historySend(t, s, c, in)
	patch := "--- a/src/main.go\n+++ b/src/main.go\n@@ -1,2 +1,2 @@\n package main\n-func old() {}\n+func next() {}\n"
	ctx := context.Background()
	owner := authz.Subject{UserID: "owner"}
	plan, err := s.CreateConversationPlan(ctx, owner, "p", c.ConversationID, CopilotPlanRequest{RequestID: "source-patch-123456", TurnID: turn.TurnID, Kind: "code_patch", CodePatch: &CopilotCodePatchInput{SourceFile: &CopilotSourceFile{JobRunID: "job", Path: "src/main.go"}, Patch: patch}})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Eligible || plan.ExecutionEnabled || plan.Payload.CodePatch.ResultDigest == "" {
		t.Fatal("unverified patch", plan)
	}
	reads := reader.calls
	plan, err = s.ReviewConversationPlan(ctx, owner, "p", c.ConversationID, plan.PlanID, CopilotPlanReviewRequest{ExpectedVersion: plan.Version, PlanDigest: plan.PlanDigest, AcknowledgeNotExecuted: true})
	if err != nil || plan.State != "reviewed" {
		t.Fatal("patch review", err)
	}
	if reader.calls != reads || p.count() != 1 {
		t.Fatal("read/review performed external read/write")
	}
	if err = s.ConfigureHistory(copilot.HistoryConfig{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err = s.ConfigureSourceContext(copilot.SourceContextConfig{Enabled: true, AllowedProjectIDs: []string{"p"}}); err != nil {
		t.Fatal(err)
	}
	s.sourceReader = reader
	restored, err := s.GetConversationPlan(ctx, owner, "p", c.ConversationID, plan.PlanID)
	if err != nil || !restored.Eligible {
		t.Fatal("restart broke immutable patch", err, restored)
	}
	s.ConfigureSourceContext(copilot.SourceContextConfig{})
	withdrawn, err := s.GetConversationPlan(ctx, owner, "p", c.ConversationID, plan.PlanID)
	if err != nil || withdrawn.Eligible || withdrawn.Payload != nil {
		t.Fatal("withdrawn source revealed patch", err, withdrawn)
	}
}

func historyArtifact(t *testing.T, s *CopilotService, db *gorm.DB, name, content string) *model.ProjectArtifact {
	t.Helper()
	root := t.TempDir()
	relative, err := artifactutil.LocalPath("p", "run", "job", "step", name)
	if err != nil {
		t.Fatal(err)
	}
	full := filepath.Join(root, relative)
	if err = os.MkdirAll(filepath.Dir(full), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(full, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	a := &model.ProjectArtifact{ArtifactID: "fixture-artifact", ProjectID: "p", PipelineID: "pipe-run", PipelineRunID: "run", JobRunID: "job", StepRunID: "step", Name: name, StorageType: "local", StoragePath: relative, Size: int64(len(content)), SHA256: historyHash(content)}
	if err = db.Create(a).Error; err != nil {
		t.Fatal(err)
	}
	s.artifacts = NewArtifactService(s.repos.Artifact, s.repos.Storage, root)
	return a
}
func TestCopilotHistoryArtifactPatchAndExpiredEvidence(t *testing.T) {
	s, db, _, c, in := historyFixture(t)
	a := historyArtifact(t, s, db, "main.go", "package main\nfunc old() {}\n")
	in.Context = CopilotSelection{ArtifactIDs: []string{a.ArtifactID}}
	turn := historySend(t, s, c, in)
	ctx := context.Background()
	owner := authz.Subject{UserID: "owner"}
	plan, err := s.CreateConversationPlan(ctx, owner, "p", c.ConversationID, CopilotPlanRequest{RequestID: "artifact-patch-123456", TurnID: turn.TurnID, Kind: "code_patch", CodePatch: &CopilotCodePatchInput{ArtifactID: a.ArtifactID, Patch: "--- a/main.go\n+++ b/main.go\n@@ -1,2 +1,2 @@\n package main\n-func old() {}\n+func next() {}\n"}})
	if err != nil || !plan.Eligible || !plan.Payload.CodePatch.SnapshotOnly {
		t.Fatal("snapshot patch", err, plan)
	}
	db.Model(a).Update("expires_at", time.Now().Add(-time.Second))
	read, err := s.GetConversationTurn(ctx, owner, "p", c.ConversationID, turn.RequestID)
	if err != nil || read.ContentAvailable {
		t.Fatal("expired evidence available", err, read)
	}
	invalid, err := s.GetConversationPlan(ctx, owner, "p", c.ConversationID, plan.PlanID)
	if err != nil || invalid.Eligible || invalid.Payload != nil {
		t.Fatal("expired patch available", err)
	}
}
func TestCopilotHistoryDeploymentPlanNeverReservesOrCallsCluster(t *testing.T) {
	s, db, p, c, in := historyFixture(t)
	turn := historySend(t, s, c, in)
	if err := db.AutoMigrate(&model.DeploymentEnvironment{}, &model.DeploymentRelease{}, &model.ApprovalRequest{}); err != nil {
		t.Fatal(err)
	}
	db.Model(&model.PipelineRun{}).Where("run_id = ?", "run").Updates(map[string]any{"status": model.PipelineStatusSuccess, "definition_commit_sha": strings.Repeat("b", 40)})
	a := historyArtifact(t, s, db, "release.json", `{"image":"registry.example/app@sha256:`+strings.Repeat("a", 64)+`"}`)
	env := &model.DeploymentEnvironment{EnvironmentID: "environment", ProjectID: "p", TargetRef: "staging", Namespace: "stage", Deployment: "app", Container: "web"}
	if err := db.Create(env).Error; err != nil {
		t.Fatal(err)
	}
	adapter := &releaseFakeAdapter{}
	deployment := NewDeploymentService(s.repos.Deployment, s.repos.Pipeline, s.repos.Artifact, s.artifacts, s.repos.Approval, DeploymentTargets{"staging": {ProjectID: "p", Target: cluster.Target{Namespace: "stage", Deployment: "app", Container: "web"}, Adapter: adapter}})
	s.ConfigureHistoryPlans(deployment)
	ctx := context.Background()
	owner := authz.Subject{UserID: "owner"}
	plan, err := s.CreateConversationPlan(ctx, owner, "p", c.ConversationID, CopilotPlanRequest{RequestID: "deployment-plan-123456", TurnID: turn.TurnID, Kind: "deployment_review", Deployment: &CopilotDeploymentReviewInput{EnvironmentID: env.EnvironmentID, ArtifactID: a.ArtifactID}})
	if err != nil || !plan.Eligible || plan.Payload.Deployment.ImageDigest == "" {
		t.Fatal("deployment proposal", err, plan)
	}
	if _, err = s.ReviewConversationPlan(ctx, owner, "p", c.ConversationID, plan.PlanID, CopilotPlanReviewRequest{ExpectedVersion: plan.Version, PlanDigest: plan.PlanDigest, AcknowledgeNotExecuted: true}); err != nil {
		t.Fatal(err)
	}
	var releases int64
	db.Model(&model.DeploymentRelease{}).Count(&releases)
	db.Where("environment_id = ?", env.EnvironmentID).First(env)
	if releases != 0 || env.ActiveReleaseID != "" || adapter.inspect+adapter.apply+adapter.observe != 0 || p.count() != 1 {
		t.Fatal("plan executed domain side effect")
	}
}
func TestCopilotHistoryRetryPlanUsesExistingApprovalWithoutDecision(t *testing.T) {
	s, db, p, c, in := historyFixture(t)
	turn := historySend(t, s, c, in)
	if err := db.AutoMigrate(&model.ApprovalRequest{}, &model.ApprovalDecision{}); err != nil {
		t.Fatal(err)
	}
	policy := retryapproval.Policy{ID: "retry-policy", Candidates: []string{"reviewer"}, Mode: "any", RequiredApproverCount: 1, TimeoutSeconds: 300}
	policy.Fingerprint, _ = policy.Digest()
	capture := retryapproval.RunContext{Version: 1, ProjectID: "p", PipelineID: "pipe-run", PipelineRunID: "run", ActorID: "owner", DefinitionCommitSHA: strings.Repeat("a", 40), DefinitionPath: "pipeline.yaml", ResolvedDefinitionDigest: "v1:" + strings.Repeat("b", 64), GatedJobs: []string{"build"}, Policy: policy}
	runtime := taskqueue.RuntimeRequest{Type: taskqueue.RuntimeContainerdOfflineV1, Image: "example/image@sha256:" + strings.Repeat("a", 64), Resources: taskqueue.RuntimeResources{CPU: "1", Memory: "128Mi"}}
	runtime.BindingDigest, _ = taskqueue.RuntimeBindingDigest("pipe-run", "run", "job", "agent", &runtime)
	identity := taskqueue.JobInputIdentity{PipelineID: "pipe-run", PipelineRunID: "run", JobRunID: "job", AgentID: "agent", ClaimID: "claim", RuntimeBindingDigest: runtime.BindingDigest}
	payload := &taskqueue.JobRunTaskPayload{RetryApprovalRequired: true, RetryApprovalTimeoutSeconds: 300, PipelineID: "pipe-run", PipelineRunID: "run", JobRunID: "job", AgentID: "agent", JobName: "build", Runtime: &runtime, Retry: &pipelinev1.Retry{Mode: "whole_job", Idempotent: true, MaxAttempts: 2, AllowedExitCodes: []int32{1}}, Steps: []taskqueue.StepPayload{{Name: "step", StepRunID: "step", AttemptRunIDs: []string{"step", "step-next"}, Uses: "shell", Args: map[string]any{"run": "exit 1"}}}}
	retry, err := taskqueue.NewJobRetryPlan(payload)
	if err != nil {
		t.Fatal(err)
	}
	retryDigest, _ := retry.Digest("pipe-run", "run", "job", "agent", runtime.BindingDigest)
	manifest, _ := taskqueue.NewJobInputManifest("", nil, nil)
	manifestDigest, _ := manifest.Digest(identity)
	created := time.Now().UTC().Truncate(time.Millisecond)
	expires := created.Add(300 * time.Second)
	evidence := retryapproval.Evidence{Version: 1, Kind: retryapproval.Kind, ApprovalID: "retry-approval", Context: capture, Identity: identity, JobName: "build", RetryPlan: *retry, RetryPlanDigest: retryDigest, Runtime: runtime, Inputs: *manifest, InputManifestDigest: manifestDigest, Artifacts: []retryapproval.Artifact{}, CreatedAtUnixMilli: created.UnixMilli(), ExpiresAtUnixMilli: expires.UnixMilli()}
	evidenceJSON, err := evidence.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	evidenceDigest, _ := evidence.Digest()
	captureJSON, _ := capture.Canonical()
	captureDigest, _ := capture.Digest()
	retryJSON, _ := retry.Canonical()
	manifestJSON, _ := manifest.Canonical()
	db.Model(&model.PipelineRun{}).Where("run_id = ?", "run").Updates(map[string]any{"status": model.PipelineStatusRunning, "retry_approval_context": captureJSON, "retry_approval_context_digest": captureDigest, "definition_commit_sha": capture.DefinitionCommitSHA, "definition_path": capture.DefinitionPath, "triggered_by": "owner"})
	db.Model(&model.JobRun{}).Where("job_run_id = ?", "job").Updates(map[string]any{"job_name": "build", "status": model.JobRunStatusRunning, "retry_approval_required": true, "retry_approval_id": "retry-approval", "retry_approval_evidence_hash": evidenceDigest, "retry_plan": retryJSON, "retry_plan_digest": retryDigest, "input_manifest_state": "pinned", "input_expectation": manifestJSON, "input_manifest": manifestJSON, "input_manifest_digest": manifestDigest, "runtime_type": runtime.Type, "runtime_image": runtime.Image, "runtime_cpu": "1", "runtime_memory": "128Mi", "runtime_binding_digest": runtime.BindingDigest, "source_type": "", "source_repository": "", "source_ref": "", "source_binding_digest": "", "source_resolved_commit": "", "current_generation_id": "generation"})
	jobID := "job"
	approval := &model.ApprovalRequest{BaseModel: model.BaseModel{CreatedAt: created, UpdatedAt: created}, ApprovalID: "retry-approval", RetryJobRunID: &jobID, PipelineRunID: "run", BindingKind: retryapproval.Kind, EvidenceJSON: evidenceJSON, EvidenceHash: evidenceDigest, Version: 1, JobName: "build", Plugin: retryapproval.Plugin, Status: model.ApprovalStatusPending, PolicyID: policy.ID, Mode: "any", RequiredApproverCount: 1, CandidateApprovers: `["reviewer"]`, RequestedBy: "owner", ExpiresAt: &expires}
	if err = db.Create(approval).Error; err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	owner := authz.Subject{UserID: "owner"}
	plan, err := s.CreateConversationPlan(ctx, owner, "p", c.ConversationID, CopilotPlanRequest{RequestID: "retry-plan-12345678", TurnID: turn.TurnID, Kind: "retry_approval_review", RetryApproval: &CopilotRetryReviewInput{ApprovalID: "retry-approval"}})
	if err != nil || !plan.Eligible || plan.Payload.RetryApproval.RetryPlanDigest != retryDigest {
		t.Fatal("retry proposal", err, plan)
	}
	if _, err = s.ReviewConversationPlan(ctx, owner, "p", c.ConversationID, plan.PlanID, CopilotPlanReviewRequest{ExpectedVersion: plan.Version, PlanDigest: plan.PlanDigest, AcknowledgeNotExecuted: true}); err != nil {
		t.Fatal(err)
	}
	var decisions int64
	db.Model(&model.ApprovalDecision{}).Count(&decisions)
	db.Where("approval_id = ?", approval.ApprovalID).First(approval)
	if decisions != 0 || approval.Status != model.ApprovalStatusPending || p.count() != 1 {
		t.Fatal("plan approved or executed retry")
	}
}

func TestCopilotHistoryProviderRotationKeepsCapturedAndCurrentRedactors(t *testing.T) {
	s, db, p, c, in := historyFixture(t)
	p.answer = "registered-old-key newly-rotated-value old-retired-key new-step-value provider-secret replacement-provider-credential"
	p.before = func(context.Context) {
		s.registry.(*historyRegistry).secretValues = []string{"replacement-provider-credential"}
		encrypted, _ := s.secrets.encrypt("newly-rotated-value")
		db.Model(&model.Secret{}).Where("secret_id = ?", "old-secret").Update("secret_value", encrypted)
		db.Model(&model.StepRun{}).Where("step_run_id = ?", "step").Update("secrets", `{"rotated":"new-step-value"}`)
	}
	turn := historySend(t, s, c, in)
	for _, secret := range []string{"registered-old-key", "newly-rotated-value", "old-retired-key", "new-step-value", "provider-secret", "replacement-provider-credential"} {
		if strings.Contains(turn.Text, secret) {
			t.Fatal("rotation leaked prior/current canary", secret)
		}
	}
	var row model.CopilotTurn
	db.Where("turn_id = ?", turn.TurnID).First(&row)
	var saved copilotHistoryEnvelope
	if err := s.historyCipher.open("owner", "p", c.ConversationID, turn.TurnID, "turn", row.PayloadCiphertext, &saved); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"registered-old-key", "newly-rotated-value", "old-retired-key", "new-step-value", "provider-secret", "replacement-provider-credential"} {
		if strings.Contains(saved.Answer, secret) {
			t.Fatal("rotation stored unredacted canary", secret)
		}
	}
}
func TestCopilotHistoryInheritedSecretRotationKeepsCapturedRedactor(t *testing.T) {
	s, db, p, c, in := historyFixture(t)
	first := historySend(t, s, c, in)
	follow := in
	follow.RequestID = "inherited-rotation-123456"
	follow.Context = CopilotSelection{}
	follow.HistoryTurnIDs = []string{first.TurnID}
	p.answer = "old-retired-key new-step-canary"
	p.before = func(context.Context) {
		db.Model(&model.StepRun{}).Where("step_run_id = ?", "step").Update("secrets", `{"rotated":"new-step-canary"}`)
	}
	turn := historySend(t, s, c, follow)
	if strings.Contains(turn.Text, "old-retired-key") || strings.Contains(turn.Text, "new-step-canary") {
		t.Fatal("inherited rotation leaked", turn.Text)
	}
}

func TestCopilotHistoryInheritedSecretRedactsPreviewAndStoredMetadata(t *testing.T) {
	s, db, p, c, in := historyFixture(t)
	first := historySend(t, s, c, in)
	a := historyArtifact(t, s, db, "old-retired-key.go", "package main\n")
	// The newly selected artifact has a different producer, whose current
	// redaction set does not contain the prior turn's retired secret.
	if err := db.Create(&model.JobRun{JobRunID: "other-job", PipelineID: "pipe-run", PipelineRunID: "run"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.StepRun{StepRunID: "other-step", JobRunID: "other-job", PipelineID: "pipe-run", PipelineRunID: "run"}).Error; err != nil {
		t.Fatal(err)
	}
	relative, _ := artifactutil.LocalPath("p", "run", "other-job", "other-step", a.Name)
	full := filepath.Join(s.artifacts.root, relative)
	os.MkdirAll(filepath.Dir(full), 0700)
	os.WriteFile(full, []byte("package main\n"), 0600)
	if err := db.Model(a).Updates(map[string]any{"job_run_id": "other-job", "step_run_id": "other-step", "storage_path": relative}).Error; err != nil {
		t.Fatal(err)
	}
	in.RequestID = "inherited-metadata-123456"
	in.Context = CopilotSelection{ArtifactIDs: []string{a.ArtifactID}}
	in.HistoryTurnIDs = []string{first.TurnID}
	ctx := context.Background()
	owner := authz.Subject{UserID: "owner"}
	preview, err := s.PreviewConversationTurn(ctx, owner, "p", c.ConversationID, in)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(preview)
	if strings.Contains(string(raw), "old-retired-key") {
		t.Fatal("preview metadata leaked inherited secret", string(raw))
	}
	in.ContextDigest = preview.Digest
	turn, err := s.SendConversationTurn(ctx, owner, "p", c.ConversationID, in)
	if err != nil {
		t.Fatal(err)
	}
	var row model.CopilotTurn
	db.Where("turn_id = ?", turn.TurnID).First(&row)
	var saved copilotHistoryEnvelope
	if err = s.historyCipher.open("owner", "p", c.ConversationID, turn.TurnID, "turn", row.PayloadCiphertext, &saved); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(saved.Evidence[0].Name, "old-retired-key") {
		t.Fatal("encrypted metadata retained unredacted canary")
	}
	if p.count() != 2 {
		t.Fatal("unexpected provider calls")
	}
}

func TestCopilotHistoryCurrentSelectionRedactsPriorAnswer(t *testing.T) {
	s, db, p, c, in := historyFixture(t)
	p.answer = "The innocuous historical text is current-step-only-canary"
	first := historySend(t, s, c, in)
	if !strings.Contains(first.Text, "current-step-only-canary") {
		t.Fatal("fixture must retain text before it becomes sensitive")
	}
	if err := db.Create(&model.StepRun{StepRunID: "current-step", JobRunID: "job", PipelineID: "pipe-run", PipelineRunID: "run", Secrets: `{"new":"current-step-only-canary"}`}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.StepRunLog{LogID: "current-log", StepRunID: "current-step", LineNumber: 1, Content: "new failure"}).Error; err != nil {
		t.Fatal(err)
	}
	in.RequestID = "current-selection-123456"
	in.Context = CopilotSelection{StepRunIDs: []string{"current-step"}}
	in.HistoryTurnIDs = []string{first.TurnID}
	preview, err := s.PreviewConversationTurn(context.Background(), authz.Subject{UserID: "owner"}, "p", c.ConversationID, in)
	if err != nil || strings.Contains(preview.Text, "current-step-only-canary") {
		t.Fatal("assembled history escaped current selection redaction", err, preview.Text)
	}
	in.ContextDigest = preview.Digest
	turn, err := s.SendConversationTurn(context.Background(), authz.Subject{UserID: "owner"}, "p", c.ConversationID, in)
	if err != nil || !turn.ContentAvailable || strings.Contains(turn.Text, "current-step-only-canary") {
		t.Fatal("unsafe current answer", err, turn)
	}
	if len(p.calls) != 2 || p.calls[1].User != preview.Text || strings.Contains(p.calls[1].User, "current-step-only-canary") {
		t.Fatal("provider did not receive the exact redacted preview")
	}
}

func TestCopilotHistoryAmbiguousTeamFailsClosed(t *testing.T) {
	s, db, p, c, in := historyFixture(t)
	for _, v := range []any{&model.Team{TeamID: "team", IsEnabled: 1}, &model.TeamMember{TeamID: "team", UserID: "owner"}} {
		if err := db.Create(v).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Model(&model.RoleGrant{}).Where("grant_id = ?", "grant").Updates(map[string]any{"subject_type": "team", "subject_id": "team"}).Error; err != nil {
		t.Fatal(err)
	}
	owner := authz.Subject{UserID: "owner"}
	if _, err := s.PreviewConversationTurn(context.Background(), owner, "p", c.ConversationID, in); err != nil {
		t.Fatal("valid current team denied", err)
	}
	if err := db.Create(&model.Team{TeamID: "team", IsEnabled: 0}).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.liveScope(context.Background(), owner, "p", "run", s.historyPrimary); !errors.Is(err, ErrCopilotDenied) {
		t.Fatal("ambiguous team allowed by primary scope", err)
	}
	if _, err := s.GetConversation(context.Background(), owner, "p", c.ConversationID, 0, 20); !errors.Is(err, ErrCopilotDenied) {
		t.Fatal("ambiguous team allowed by locked authorization", err)
	}
	if p.count() != 0 {
		t.Fatal("authorization failure called provider")
	}
}

func historyOtherProducer(t *testing.T, s *CopilotService, db *gorm.DB, a *model.ProjectArtifact, content string) {
	t.Helper()
	for _, v := range []any{&model.JobRun{JobRunID: "other-job", PipelineID: "pipe-run", PipelineRunID: "run"}, &model.StepRun{StepRunID: "other-step", JobRunID: "other-job", PipelineID: "pipe-run", PipelineRunID: "run"}} {
		if err := db.Create(v).Error; err != nil {
			t.Fatal(err)
		}
	}
	relative, err := artifactutil.LocalPath("p", "run", "other-job", "other-step", a.Name)
	if err != nil {
		t.Fatal(err)
	}
	full := filepath.Join(s.artifacts.root, relative)
	if err = os.MkdirAll(filepath.Dir(full), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(full, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	if err = db.Model(a).Updates(map[string]any{"job_run_id": "other-job", "step_run_id": "other-step", "storage_path": relative}).Error; err != nil {
		t.Fatal(err)
	}
}

func TestCopilotHistoryPlansRejectInheritedAndEscapedSecrets(t *testing.T) {
	for _, canary := range []string{"old-retired-key", `quote"canary`, `slash\canary`, "<html>&canary", "unicode\u2028canary"} {
		t.Run(canary, func(t *testing.T) {
			s, db, _, c, in := historyFixture(t)
			secrets, _ := json.Marshal(map[string]string{"private": canary})
			if err := db.Model(&model.StepRun{}).Where("step_run_id = ?", "step").Update("secrets", string(secrets)).Error; err != nil {
				t.Fatal(err)
			}
			first := historySend(t, s, c, in)
			base := "package main\nfunc old() {}\n"
			a := historyArtifact(t, s, db, "main.go", base)
			historyOtherProducer(t, s, db, a, base)
			in.RequestID = "inherited-plan-parent-123456"
			in.Context = CopilotSelection{ArtifactIDs: []string{a.ArtifactID}}
			in.HistoryTurnIDs = []string{first.TurnID}
			parentTurn := historySend(t, s, c, in)
			ctx := context.Background()
			owner := authz.Subject{UserID: "owner"}
			conv, _, parent, state, err := s.planParent(ctx, owner, "p", c.ConversationID, parentTurn.TurnID)
			if err != nil {
				t.Fatal(err)
			}
			payload := CopilotPlanPayload{SchemaVersion: 1, Kind: "code_patch", CodePatch: &CopilotCodePatchPlan{Patch: "literal " + canary}}
			if err = s.copilotPlanPayloadGuard(ctx, owner, conv, parent, state, payload); !errors.Is(err, ErrCopilotPlanUnavailable) {
				t.Fatal("decoded inherited secret accepted", err)
			}
			patch := "--- a/main.go\n+++ b/main.go\n@@ -1,2 +1,2 @@\n package main\n-func old() {}\n+// " + canary + "\n"
			if _, err = s.CreateConversationPlan(ctx, owner, "p", c.ConversationID, CopilotPlanRequest{RequestID: "inherited-secret-plan-123456", TurnID: parentTurn.TurnID, Kind: "code_patch", CodePatch: &CopilotCodePatchInput{ArtifactID: a.ArtifactID, Patch: patch}}); !errors.Is(err, ErrCopilotPlanUnavailable) {
				t.Fatal("inherited secret patch saved", err)
			}
			// The same decoded guard applies to read-time validation of any typed field.
			payload.CodePatch.Patch = "safe patch"
			payload.CodePatch.Repository = "prefix" + canary
			if err = s.copilotPlanPayloadGuard(ctx, owner, conv, parent, state, payload); !errors.Is(err, ErrCopilotPlanUnavailable) {
				t.Fatal("escaped metadata secret accepted", err)
			}
			var plans int64
			db.Model(&model.CopilotActionPlan{}).Count(&plans)
			if plans != 0 {
				t.Fatal("rejected secret proposal persisted")
			}
		})
	}
}

func TestCopilotHistoryEffectiveContentDeadlineIncludesAncestorsAndArtifacts(t *testing.T) {
	s, db, _, c, in := historyFixture(t)
	a := historyArtifact(t, s, db, "main.go", "package main\n")
	artifactDeadline := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	if err := db.Model(a).Update("expires_at", artifactDeadline).Error; err != nil {
		t.Fatal(err)
	}
	in.Context = CopilotSelection{ArtifactIDs: []string{a.ArtifactID}}
	first := historySend(t, s, c, in)
	if first.ContentAvailableUntil == nil || !first.ContentAvailableUntil.Equal(artifactDeadline) || !first.ExpiresAt.After(artifactDeadline) {
		t.Fatal("artifact display deadline missing", first)
	}
	childDeadline := artifactDeadline.Add(-time.Minute)
	if err := db.Model(&model.CopilotTurn{}).Where("turn_id = ?", first.TurnID).Update("expires_at", childDeadline).Error; err != nil {
		t.Fatal(err)
	}
	in.Context = CopilotSelection{}
	in.RequestID = "effective-child-123456"
	in.HistoryTurnIDs = []string{first.TurnID}
	second := historySend(t, s, c, in)
	if second.ContentAvailableUntil == nil || !second.ContentAvailableUntil.Equal(childDeadline) || !second.ExpiresAt.After(childDeadline) {
		t.Fatal("ancestor display deadline missing", second)
	}
	ctx := context.Background()
	owner := authz.Subject{UserID: "owner"}
	plan, err := s.CreateConversationPlan(ctx, owner, "p", c.ConversationID, CopilotPlanRequest{RequestID: "effective-plan-123456", TurnID: second.TurnID, Kind: "new_run_review"})
	if err != nil || !plan.Eligible || plan.ContentAvailableUntil == nil || !plan.ContentAvailableUntil.Equal(childDeadline) || !plan.ExpiresAt.After(childDeadline) {
		t.Fatal("plan did not inherit display deadline", plan, err)
	}
	// Shortening current evidence availability never extends the stored ancestor bound.
	earlier := childDeadline.Add(-time.Minute)
	if err := db.Model(a).Update("expires_at", earlier).Error; err != nil {
		t.Fatal(err)
	}
	read, err := s.GetConversationTurn(ctx, owner, "p", c.ConversationID, second.RequestID)
	if err != nil || read.ContentAvailableUntil == nil || !read.ContentAvailableUntil.Equal(earlier) {
		t.Fatal("current evidence deadline not propagated", read, err)
	}
	plan, err = s.GetConversationPlan(ctx, owner, "p", c.ConversationID, plan.PlanID)
	if err != nil || plan.ContentAvailableUntil == nil || !plan.ContentAvailableUntil.Equal(earlier) {
		t.Fatal("current plan evidence deadline not propagated", plan, err)
	}
}

func TestCopilotHistoryAncestorRedactionStateBindsPreviewEvenWhenBodyUnchanged(t *testing.T) {
	s, db, p, c, in := historyFixture(t)
	p.answer = "A diagnostic hypothesis without any secret value"
	first := historySend(t, s, c, in)
	in.RequestID = "ancestor-seal-123456"
	in.Context = CopilotSelection{}
	in.HistoryTurnIDs = []string{first.TurnID}
	ctx := context.Background()
	owner := authz.Subject{UserID: "owner"}
	before, err := s.PreviewConversationTurn(ctx, owner, "p", c.ConversationID, in)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Model(&model.StepRun{}).Where("step_run_id = ?", "step").Update("secrets", `{"retired":"new-ancestor-canary"}`).Error; err != nil {
		t.Fatal(err)
	}
	after, err := s.PreviewConversationTurn(ctx, owner, "p", c.ConversationID, in)
	if err != nil {
		t.Fatal(err)
	}
	if before.Text != after.Text || before.Digest == after.Digest {
		t.Fatal("ancestor secret-state change must invalidate otherwise identical preview")
	}
	in.ContextDigest = before.Digest
	if _, err = s.SendConversationTurn(ctx, owner, "p", c.ConversationID, in); !errors.Is(err, ErrCopilotPreview) {
		t.Fatal("stale ancestor preview accepted", err)
	}
	if p.count() != 1 {
		t.Fatal("stale preview reached provider")
	}
}

func TestCopilotHistoryInheritedSecretBaseAndPlanRotationFailClosed(t *testing.T) {
	s, db, _, c, in := historyFixture(t)
	first := historySend(t, s, c, in)
	base := "package main\n// old-retired-key\nfunc old() {}\n"
	a := historyArtifact(t, s, db, "main.go", base)
	historyOtherProducer(t, s, db, a, base)
	in.RequestID = "inherited-base-parent-123456"
	in.Context = CopilotSelection{ArtifactIDs: []string{a.ArtifactID}}
	in.HistoryTurnIDs = []string{first.TurnID}
	turn := historySend(t, s, c, in)
	ctx := context.Background()
	owner := authz.Subject{UserID: "owner"}
	patch := "--- a/main.go\n+++ b/main.go\n@@ -3 +3 @@\n-func old() {}\n+func next() {}\n"
	if _, err := s.CreateConversationPlan(ctx, owner, "p", c.ConversationID, CopilotPlanRequest{RequestID: "inherited-base-plan-123456", TurnID: turn.TurnID, Kind: "code_patch", CodePatch: &CopilotCodePatchInput{ArtifactID: a.ArtifactID, Patch: patch}}); !errors.Is(err, ErrCopilotPlanUnavailable) {
		t.Fatal("secret in untouched inherited base accepted", err)
	}
	conv, _, parent, state, err := s.planParent(ctx, owner, "p", c.ConversationID, turn.TurnID)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Model(&model.StepRun{}).Where("step_run_id = ?", "step").Update("secrets", `{"new":"new-ancestor-canary"}`).Error; err != nil {
		t.Fatal(err)
	}
	if _, err = s.copilotPlanTextGuard(ctx, owner, conv, parent, state, "safe unchanged text"); !errors.Is(err, ErrCopilotPlanUnavailable) {
		t.Fatal("ancestor rotation during patch verification accepted", err)
	}
}
