// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package taskqueue

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/agent/config"
	agentgrpc "github.com/arcentrix/arcentra/internal/shared/grpc"
	"github.com/arcentrix/arcentra/pkg/sourceprovenance"
	taskpayload "github.com/arcentrix/arcentra/pkg/taskqueue"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func sourceTestGit(t *testing.T, directory string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "git", args...)
	cmd.Dir = directory
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.invalid", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.invalid")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("local Git fixture %v failed: %s: %v", args, output, err)
	}
	return strings.TrimSpace(string(output))
}

func sourceTestRepository(t *testing.T, label string) (repository, first, initial string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	repository = t.TempDir()
	sourceTestGit(t, repository, "init", "-b", "main")
	for _, content := range []string{"first", "initial"} {
		artifactTestWrite(t, repository, "source.txt", label+"-"+content)
		sourceTestGit(t, repository, "add", "source.txt")
		sourceTestGit(t, repository, "commit", "-m", label+"-"+content)
		if first == "" {
			first = sourceTestGit(t, repository, "rev-parse", "HEAD")
		}
	}
	return repository, first, sourceTestGit(t, repository, "rev-parse", "HEAD")
}

func sourceTestBind(t *testing.T, payload *taskpayload.JobRunTaskPayload) {
	t.Helper()
	src := payload.Source
	binding, err := sourceprovenance.NewBinding(payload.PipelineID, payload.PipelineRunID, payload.JobRunID, src.Type, src.Repo, src.Branch)
	if err != nil {
		t.Fatal(err)
	}
	src.BindingDigest = binding.Digest
}

func sourceTestPayload(t *testing.T, repository, name string) *taskpayload.JobRunTaskPayload {
	t.Helper()
	payload := &taskpayload.JobRunTaskPayload{
		PipelineID: "pipeline", PipelineRunID: "run", JobRunID: "source-" + name, AgentID: "agent",
		Source: &taskpayload.SourcePayload{Type: "git", Repo: repository, Branch: "main"},
	}
	sourceTestBind(t, payload)
	return payload
}

func sourceTestTerminal(t *testing.T, client *artifactStatusClient) *agentv1.ReportJobRunStatusRequest {
	t.Helper()
	for _, report := range client.jobReports {
		if report.Status < 4 && report.SourceCheckout != nil {
			t.Fatal("source checkout was included in a nonterminal claim")
		}
	}
	if len(client.jobReports) == 0 || client.jobReports[len(client.jobReports)-1].Status < 4 {
		t.Fatal("job did not report a terminal result")
	}
	return client.jobReports[len(client.jobReports)-1]
}

func TestJobInitialSourceCheckoutSurvivesUserCheckoutAndRetries(t *testing.T) {
	repository, changed, initial := sourceTestRepository(t, "job-source")
	_, _, definition := sourceTestRepository(t, "pipeline-definition")
	for _, mode := range []string{"none", "failed_step", "whole_job"} {
		t.Run(mode, func(t *testing.T) {
			payload := sourceTestPayload(t, repository, mode)
			// Definition/event metadata is deliberately unrelated to the actual source checkout.
			payload.Env = map[string]string{"CI_COMMIT_SHA": definition, "EVENT_COMMIT_SHA": definition}
			payload.Source.Auth = map[string]string{"token": "synthetic-private-source-token"}
			command := fmt.Sprintf("git checkout --detach %s; printf x >> executions", changed)
			step := taskpayload.StepPayload{Name: "change-checkout", StepRunID: payload.JobRunID + "-step", Uses: "shell"}
			if mode != "none" {
				payload.Retry = &pipelinev1.Retry{Mode: mode, MaxAttempts: 2, Idempotent: true, AllowedExitCodes: []int32{9}}
				step.AttemptRunIDs = []string{step.StepRunID, step.StepRunID + "-retry"}
				command += "; if [ ! -f attempted ]; then touch attempted; exit 9; fi"
			}
			step.Args = map[string]any{"run": command}
			payload.Steps = []taskpayload.StepPayload{step}
			conf := &config.AgentConfig{Agent: config.AgentInfo{ID: "agent", WorkspaceDir: t.TempDir()}}
			client := &artifactStatusClient{}
			if err := executeJobRun(context.Background(), conf, &agentgrpc.ClientWrapper{AgentClient: client}, nil, payload); err != nil {
				t.Fatal(err)
			}
			report := sourceTestTerminal(t, client)
			if report.Status != 4 || report.SourceCheckout == nil || report.SourceCheckout.BindingDigest != payload.Source.BindingDigest || report.SourceCheckout.ResolvedCommit != initial || initial == definition || initial == changed {
				t.Fatalf("initial source evidence was substituted: %+v", report)
			}
			workspace := filepath.Join(conf.Agent.WorkspaceDir, payload.PipelineRunID, payload.JobRunID)
			if head := sourceTestGit(t, workspace, "rev-parse", "HEAD"); head != changed {
				t.Fatalf("user checkout did not execute: %s", head)
			}
			wantExecutions := "x"
			if mode != "none" {
				wantExecutions = "xx"
			}
			if got := artifactTestRead(t, workspace, "executions"); got != wantExecutions {
				t.Fatalf("retry executions = %q, want %q", got, wantExecutions)
			}
			encoded, err := proto.Marshal(report)
			if err != nil || bytes.Contains(encoded, []byte("synthetic-private-source-token")) || bytes.Contains(encoded, []byte(repository)) {
				t.Fatalf("checkout receipt leaked source metadata: %v", err)
			}
		})
	}
}

func TestJobInitialSourceCheckoutUnknownAndInterruptedCases(t *testing.T) {
	repository, changed, initial := sourceTestRepository(t, "outcomes")
	for _, name := range []string{"no_source", "legacy_source", "clone_failure", "unresolved_head", "cancel_before_clone", "step_failure", "artifact_failure", "artifact_mutates_checkout", "cancel_after_clone"} {
		t.Run(name, func(t *testing.T) {
			payload := sourceTestPayload(t, repository, name)
			conf := &config.AgentConfig{Agent: config.AgentInfo{ID: "agent", WorkspaceDir: t.TempDir()}}
			workspace := filepath.Join(conf.Agent.WorkspaceDir, payload.PipelineRunID, payload.JobRunID)
			client := &artifactStatusClient{}
			wantStatus, wantCommit := int32(4), initial
			switch name {
			case "no_source":
				payload.Source, wantCommit = nil, ""
			case "legacy_source":
				payload.Source.BindingDigest, wantCommit = "", ""
				// Legacy explicit commit pins still execute without inventing a controller binding.
				payload.Source.Commit = changed
			case "clone_failure":
				payload.Source.Repo = filepath.Join(t.TempDir(), "missing-synthetic-repository")
				sourceTestBind(t, payload)
				wantStatus, wantCommit = 5, ""
			case "unresolved_head":
				payload.Source.Repo = t.TempDir()
				sourceTestGit(t, payload.Source.Repo, "init", "-b", "main")
				// Empty repositories can clone successfully, but have no commit to attest.
				payload.Source.Branch = ""
				sourceTestBind(t, payload)
				payload.Steps = []taskpayload.StepPayload{{Name: "must-not-run", StepRunID: payload.JobRunID + "-step", Args: map[string]any{"run": "touch unexpected"}}}
				wantStatus, wantCommit = 5, ""
			case "cancel_before_clone":
				CancelJobRun(payload.JobRunID)
				t.Cleanup(func() { cancelledJobs.Delete(payload.JobRunID) })
				wantStatus, wantCommit = 6, ""
			case "step_failure":
				payload.Steps = []taskpayload.StepPayload{{Name: "fail", StepRunID: payload.JobRunID + "-step", Args: map[string]any{"run": "exit 2"}}}
				wantStatus = 5
			default:
				payload.ArtifactURIs = map[string]string{"producer/input.txt": "fixture-object"}
				st := &memoryArtifactStorage{objects: map[string][]byte{"fixture-object": []byte("artifact")}, download: func(ctx context.Context, _ string) (io.ReadCloser, error) {
					if head := sourceTestGit(t, workspace, "rev-parse", "HEAD"); head != initial {
						t.Fatalf("source was not cloned before artifact download: %s", head)
					}
					switch name {
					case "artifact_failure":
						return nil, errors.New("synthetic artifact download failure")
					case "artifact_mutates_checkout":
						sourceTestGit(t, workspace, "checkout", "--detach", changed)
					case "cancel_after_clone":
						if !CancelJobRun(payload.JobRunID) {
							t.Fatal("job was not cancellable after clone")
						}
						return nil, ctx.Err()
					}
					return io.NopCloser(strings.NewReader("artifact")), nil
				}}
				payload.ArtifactReferences = artifactTestReferences(payload.ArtifactURIs, st.objects)
				SetStorage(st, artifactTestReference())
				t.Cleanup(func() { SetStorage(nil); cancelledJobs.Delete(payload.JobRunID) })
				if name == "artifact_failure" {
					wantStatus = 5
				} else if name == "cancel_after_clone" {
					wantStatus = 6
				}
			}
			err := executeJobRun(context.Background(), conf, &agentgrpc.ClientWrapper{AgentClient: client}, nil, payload)
			if acknowledgeCompleted(err) != nil || wantStatus == 4 && err != nil {
				t.Fatalf("job outcome was not reported: %v", err)
			}
			report := sourceTestTerminal(t, client)
			if report.Status != wantStatus {
				t.Fatalf("terminal status = %d, want %d", report.Status, wantStatus)
			}
			if name == "unresolved_head" {
				if _, err := os.Stat(filepath.Join(workspace, ".git")); err != nil {
					t.Fatalf("fixture did not complete clone: %v", err)
				}
				if _, err := os.Stat(filepath.Join(workspace, "unexpected")); !os.IsNotExist(err) {
					t.Fatalf("unresolved initial HEAD allowed a user command: %v", err)
				}
			}
			if wantCommit == "" {
				if report.SourceCheckout != nil {
					t.Fatalf("unknown checkout was guessed: %+v", report.SourceCheckout)
				}
			} else if report.SourceCheckout == nil || report.SourceCheckout.ResolvedCommit != wantCommit || report.SourceCheckout.BindingDigest != payload.Source.BindingDigest {
				t.Fatalf("successful initial checkout was lost: %+v", report.SourceCheckout)
			}
		})
	}
}

func TestJobSourceBindingRejectsTamperingBeforeWorkspace(t *testing.T) {
	repository, _, _ := sourceTestRepository(t, "binding")
	for name, mutate := range map[string]func(*taskpayload.JobRunTaskPayload){
		"pipeline": func(p *taskpayload.JobRunTaskPayload) { p.PipelineID = "other-pipeline" },
		"run":      func(p *taskpayload.JobRunTaskPayload) { p.PipelineRunID = "other-run" },
		"job":      func(p *taskpayload.JobRunTaskPayload) { p.JobRunID += "-other" },
		"type":     func(p *taskpayload.JobRunTaskPayload) { p.Source.Type = "other" },
		"repo":     func(p *taskpayload.JobRunTaskPayload) { p.Source.Repo += "-other" },
		"ref":      func(p *taskpayload.JobRunTaskPayload) { p.Source.Branch = "other" },
		"commit":   func(p *taskpayload.JobRunTaskPayload) { p.Source.Commit = strings.Repeat("a", 40) },
		"digest":   func(p *taskpayload.JobRunTaskPayload) { p.Source.BindingDigest = "v1:" + strings.Repeat("a", 64) },
		"credential_url": func(p *taskpayload.JobRunTaskPayload) {
			p.Source.Repo = "https://user:synthetic-secret@example.invalid/repo"
		},
	} {
		t.Run(name, func(t *testing.T) {
			payload := sourceTestPayload(t, repository, "tamper-"+name)
			mutate(payload)
			payload.Workspace = filepath.Join(t.TempDir(), "must-not-exist")
			client := &artifactStatusClient{}
			conf := &config.AgentConfig{Agent: config.AgentInfo{ID: "agent"}}
			err := executeJobRun(context.Background(), conf, &agentgrpc.ClientWrapper{AgentClient: client}, nil, payload)
			if err == nil || acknowledgeCompleted(err) != nil || strings.Contains(err.Error(), "synthetic-secret") {
				t.Fatalf("tampered source binding was not safely rejected: %v", err)
			}
			if _, err := os.Stat(payload.Workspace); !os.IsNotExist(err) {
				t.Fatalf("tampered source reached workspace preparation: %v", err)
			}
			report := sourceTestTerminal(t, client)
			if report.Status != 5 || report.SourceCheckout != nil {
				t.Fatalf("tampering produced checkout evidence: %+v", report)
			}
		})
	}
}

func TestInitialSourceCheckoutReceiptSurvivesRestartAndChangedPayload(t *testing.T) {
	repository, changed, initial := sourceTestRepository(t, "durable")
	payload := sourceTestPayload(t, repository, "durable")
	payload.Source.Auth = map[string]string{"token": "synthetic-durable-private-token"}
	payload.Steps = []taskpayload.StepPayload{{Name: "change", StepRunID: payload.JobRunID + "-step", Args: map[string]any{"run": "git checkout --detach " + changed + "; printf x >> executions"}}}
	conf := &config.AgentConfig{Agent: config.AgentInfo{ID: "agent", WorkspaceDir: t.TempDir()}, Grpc: config.GrpcConfig{ServerAddr: "controller:9090"}}
	directory := filepath.Join(t.TempDir(), "reports")
	outbox := openTestTerminalOutbox(t, directory)
	t.Cleanup(func() { _ = outbox.close() })
	ctx := withTerminalOutbox(context.Background(), outbox)
	client := &terminalCaptureClient{failJob: true}
	wrapper := &agentgrpc.ClientWrapper{AgentClient: client}
	if err := executeJobRun(ctx, conf, wrapper, nil, payload); status.Code(err) != codes.Unavailable {
		t.Fatalf("expected unacknowledged terminal receipt: %v", err)
	}
	saved := outbox.entries[terminalRecordName("job", payload.JobRunID)].record
	report := &agentv1.ReportJobRunStatusRequest{}
	if err := proto.Unmarshal(saved.Payload, report); err != nil || report.SourceCheckout == nil || report.SourceCheckout.ResolvedCommit != initial {
		t.Fatalf("durable receipt lost initial checkout: %+v %v", report, err)
	}
	diskBefore, err := os.ReadFile(filepath.Join(directory, terminalRecordName("job", payload.JobRunID)))
	if err != nil || bytes.Contains(diskBefore, []byte("synthetic-durable-private-token")) || bytes.Contains(saved.Payload, []byte("synthetic-durable-private-token")) {
		t.Fatalf("private source data persisted: %v", err)
	}
	// Lose all volatile execution state; only the original durable receipt may be replayed.
	pendingJobReports.Delete(receiptKey(ctx, "job", payload.JobRunID))
	completeExecutionClaim(ctx, "job", payload.JobRunID)
	if err := outbox.close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(repository, repository+"-moved-after-execution"); err != nil {
		t.Fatal(err)
	}
	payload.Source.BindingDigest = "v1:" + strings.Repeat("f", 64)
	payload.Source.Branch = "different-after-terminal"
	outbox = openTestTerminalOutbox(t, directory)
	ctx = withTerminalOutbox(context.Background(), outbox)
	diskAfter, err := os.ReadFile(filepath.Join(directory, terminalRecordName("job", payload.JobRunID)))
	if err != nil || !bytes.Equal(diskBefore, diskAfter) {
		t.Fatalf("restart rewrote immutable receipt: %v", err)
	}
	client.failJob = false
	if err := executeJobRun(ctx, conf, wrapper, nil, payload); err != nil {
		t.Fatal(err)
	}
	jobCalls := 0
	for _, call := range client.calls {
		if call.Kind == "job" {
			jobCalls++
			if !bytes.Equal(call.Payload, saved.Payload) || call.ClaimID != saved.ClaimID {
				t.Fatal("replay changed original source evidence or authenticated claim")
			}
		}
	}
	workspace := filepath.Join(conf.Agent.WorkspaceDir, payload.PipelineRunID, payload.JobRunID)
	if jobCalls != 2 || artifactTestRead(t, workspace, "executions") != "x" || outbox.has("job", payload.JobRunID) {
		t.Fatal("terminal replay repeated execution or failed to acknowledge receipt")
	}
}

func TestInitialSourceCommitLocalReadPreservesCancellationEvidence(t *testing.T) {
	repository, _, initial := sourceTestRepository(t, "cancel-local-read")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	git, err := newSourceGit(&taskpayload.SourcePayload{Repo: repository}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer git.close()
	git.workspace = repository
	commit, err := git.resolveCommit(ctx)
	if err != nil || commit != initial {
		t.Fatalf("completed checkout evidence lost to later cancellation: %s %v", commit, err)
	}
	git.workspace = t.TempDir()
	if _, err := git.resolveCommit(context.Background()); err == nil {
		t.Fatal("nonrepository workspace received an invented source commit")
	}
}

func TestSourceCloneErrorOmitsUntrustedGitOutput(t *testing.T) {
	// An absent local path exercises real Git's error output without contacting a network source.
	secret := "synthetic-undeclared-source-secret"
	src := &taskpayload.SourcePayload{Type: "git", Repo: filepath.Join(t.TempDir(), secret), Auth: map[string]string{"token": "synthetic-known-token"}}
	checkout, err := handleSourceClone(context.Background(), src, t.TempDir())
	if err == nil || checkout != nil || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), src.Auth["token"]) || strings.Contains(err.Error(), src.Repo) {
		t.Fatalf("Git source failure was not sanitized: %v", err)
	}
}
