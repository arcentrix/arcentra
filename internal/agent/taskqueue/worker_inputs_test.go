package taskqueue

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	"github.com/arcentrix/arcentra/internal/agent/config"
	agentgrpc "github.com/arcentrix/arcentra/internal/shared/grpc"
	taskpayload "github.com/arcentrix/arcentra/pkg/taskqueue"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type immutableInputClient struct {
	agentv1.AgentServiceClient
	t        *testing.T
	pinned   *agentv1.GetOrRecordJobInputsResponse
	requests []*agentv1.GetOrRecordJobInputsRequest
	failure  error
	loseAck  bool
}

func (c *immutableInputClient) GetOrRecordJobInputs(ctx context.Context, req *agentv1.GetOrRecordJobInputsRequest, _ ...grpc.CallOption) (*agentv1.GetOrRecordJobInputsResponse, error) {
	c.t.Helper()
	md, _ := metadata.FromOutgoingContext(ctx)
	claims := md.Get(executionClaimMetadataKey)
	if len(claims) != 1 || claims[0] != req.OriginalClaimId || req.OriginalClaimId == "" {
		c.t.Fatal("input RPC did not preserve original claim")
	}
	copy := proto.Clone(req).(*agentv1.GetOrRecordJobInputsRequest)
	c.requests = append(c.requests, copy)
	if c.failure != nil {
		return nil, c.failure
	}
	if req.ManifestJson == "" {
		if c.pinned != nil {
			return c.pinned, nil
		}
		return &agentv1.GetOrRecordJobInputsResponse{State: "unknown"}, nil
	}
	m, err := taskpayload.ParseJobInputManifest(req.ManifestJson)
	if err != nil {
		return nil, err
	}
	i := taskpayload.JobInputIdentity{PipelineID: req.PipelineId, PipelineRunID: req.PipelineRunId, JobRunID: req.JobRunId, AgentID: req.AgentId, ClaimID: req.OriginalClaimId, RuntimeBindingDigest: req.RuntimeBindingDigest, SourceBindingDigest: req.SourceBindingDigest}
	digest, err := m.Digest(i)
	if err != nil {
		return nil, err
	}
	if c.pinned != nil && (c.pinned.ManifestJson != req.ManifestJson || c.pinned.ManifestDigest != digest) {
		return nil, status.Error(codes.FailedPrecondition, "input conflict")
	}
	c.pinned = &agentv1.GetOrRecordJobInputsResponse{State: "pinned", ManifestJson: req.ManifestJson, ManifestDigest: digest}
	if c.loseAck {
		c.loseAck = false
		return nil, status.Error(codes.Unavailable, "synthetic lost pin response")
	}
	return c.pinned, nil
}

func inputAgentFixture(t *testing.T, repository string) (context.Context, *config.AgentConfig, *taskpayload.JobRunTaskPayload, *immutableInputClient) {
	t.Helper()
	payload := sourceTestPayload(t, repository, "immutable")
	payload.Runtime = &taskpayload.RuntimeRequest{Type: taskpayload.RuntimeContainerdOfflineV1, Image: "registry.example/build@sha256:" + strings.Repeat("a", 64), Resources: taskpayload.RuntimeResources{CPU: "500m", Memory: "64Mi"}}
	var err error
	payload.Runtime.BindingDigest, err = taskpayload.RuntimeBindingDigest(payload.PipelineID, payload.PipelineRunID, payload.JobRunID, payload.AgentID, payload.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	ctx := withJobExecutionClaim(context.Background(), payload.JobRunID)
	t.Cleanup(func() { completeExecutionClaim(ctx, "job", payload.JobRunID) })
	return ctx, &config.AgentConfig{Agent: config.AgentInfo{ID: payload.AgentID}}, payload, &immutableInputClient{t: t}
}

func TestJobInputsAgentReusesPinnedCommitAfterBranchMovesAndDisappears(t *testing.T) {
	repository, _, initial := sourceTestRepository(t, "immutable")
	ctx, conf, payload, client := inputAgentFixture(t, repository)
	original := *payload.Source
	wrapper := &agentgrpc.ClientWrapper{AgentClient: client}
	checkout, err := checkoutJobInputs(ctx, wrapper, conf, payload, t.TempDir())
	if err != nil || checkout.ResolvedCommit != initial {
		t.Fatalf("initial checkout: %+v %v", checkout, err)
	}
	if client.pinned == nil || len(client.requests) != 2 || client.requests[0].ManifestJson != "" || client.requests[1].ManifestJson == "" {
		t.Fatal("checkout was not surrounded by get then durable record")
	}
	artifactTestWrite(t, repository, "source.txt", "moved")
	sourceTestGit(t, repository, "add", "source.txt")
	sourceTestGit(t, repository, "commit", "-m", "branch moved")
	sourceTestGit(t, repository, "branch", "-m", "main", "renamed")
	t.Setenv("GIT_DEFAULT_HASH", "sha256") // Pin controls format even if host defaults differ
	workspace := t.TempDir()
	checkout, err = checkoutJobInputs(ctx, wrapper, conf, payload, workspace)
	if err != nil || checkout.ResolvedCommit != initial {
		t.Fatalf("reused checkout: %+v %v", checkout, err)
	}
	if got := sourceTestGit(t, workspace, "rev-parse", "HEAD"); got != initial {
		t.Fatalf("mutable ref resolved instead of pin: %s", got)
	}
	if !reflect.DeepEqual(original, *payload.Source) || payload.Source.Commit != "" {
		t.Fatal("worker mutated original v1 source identity")
	}
}

func TestJobInputsAgentLostPinAckDoesNotReresolveSource(t *testing.T) {
	repository, _, initial := sourceTestRepository(t, "ack")
	ctx, conf, payload, client := inputAgentFixture(t, repository)
	client.loseAck = true
	wrapper := &agentgrpc.ClientWrapper{AgentClient: client}
	if checkout, err := checkoutJobInputs(ctx, wrapper, conf, payload, t.TempDir()); err == nil || checkout == nil || checkout.ResolvedCommit != initial || client.pinned == nil {
		t.Fatalf("lost pin response did not fail closed: %+v %v", checkout, err)
	}
	artifactTestWrite(t, repository, "source.txt", "changed")
	sourceTestGit(t, repository, "add", "source.txt")
	sourceTestGit(t, repository, "commit", "-m", "changed")
	if checkout, err := checkoutJobInputs(ctx, wrapper, conf, payload, t.TempDir()); err != nil || checkout.ResolvedCommit != initial {
		t.Fatalf("replayed checkout changed after lost ack: %+v %v", checkout, err)
	}
}

func TestJobInputsAgentRequiresSupportedRPCBeforeCheckoutAndSupportsNoSource(t *testing.T) {
	repository, _, _ := sourceTestRepository(t, "unsupported")
	ctx, conf, payload, client := inputAgentFixture(t, repository)
	client.failure = status.Error(codes.Unimplemented, "old controller")
	workspace := t.TempDir()
	if _, err := checkoutJobInputs(ctx, &agentgrpc.ClientWrapper{AgentClient: client}, conf, payload, workspace); status.Code(err) != codes.Unimplemented {
		t.Fatalf("container silently downgraded: %v", err)
	}
	entries, err := os.ReadDir(workspace)
	if err != nil || len(entries) != 0 {
		t.Fatal("checkout happened before controller input support")
	}
	client.failure = nil
	payload.Source = nil
	if checkout, err := checkoutJobInputs(ctx, &agentgrpc.ClientWrapper{AgentClient: client}, conf, payload, workspace); err != nil || checkout != nil || client.pinned == nil {
		t.Fatalf("source-free explicit pin: %+v %v", checkout, err)
	}
	m, err := taskpayload.ParseJobInputManifest(client.pinned.ManifestJson)
	if err != nil || m.SourceCommit != "" || len(m.Artifacts) != 0 {
		t.Fatal("source-free manifest was not explicit")
	}
}

func TestJobInputsAgentRejectsPinnedDigestAndArtifactMismatchBeforeCheckout(t *testing.T) {
	for _, changed := range []string{"digest", "artifacts"} {
		t.Run(changed, func(t *testing.T) {
			repository, _, _ := sourceTestRepository(t, changed)
			ctx, conf, payload, client := inputAgentFixture(t, repository)
			wrapper := &agentgrpc.ClientWrapper{AgentClient: client}
			if _, err := checkoutJobInputs(ctx, wrapper, conf, payload, t.TempDir()); err != nil {
				t.Fatal(err)
			}
			if changed == "digest" {
				client.pinned.ManifestDigest = "v1:" + strings.Repeat("b", 64)
			} else {
				size := int64(0)
				payload.ArtifactURIs = map[string]string{"new": "artifacts/new"}
				payload.ArtifactReferences = map[string]taskpayload.ArtifactReference{"new": {StorageID: "store", Fingerprint: strings.Repeat("b", 64), SHA256: strings.Repeat("a", 64), Size: &size}}
			}
			workspace := t.TempDir()
			if _, err := checkoutJobInputs(ctx, wrapper, conf, payload, workspace); err == nil {
				t.Fatal("mismatched pinned input accepted")
			}
			entries, _ := os.ReadDir(workspace)
			if len(entries) != 0 {
				t.Fatal("checkout happened before pin verification")
			}
		})
	}
}

func TestJobInputsAgentReusesFullSHA256GitCommit(t *testing.T) {
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	repository := t.TempDir()
	sourceTestGit(t, repository, "init", "--object-format=sha256", "-b", "main")
	artifactTestWrite(t, repository, "source.txt", "sha256 source")
	sourceTestGit(t, repository, "add", "source.txt")
	sourceTestGit(t, repository, "commit", "-m", "original")
	initial := sourceTestGit(t, repository, "rev-parse", "HEAD")
	if len(initial) != 64 {
		t.Fatal("fixture is not SHA256 Git")
	}
	ctx, conf, payload, client := inputAgentFixture(t, repository)
	wrapper := &agentgrpc.ClientWrapper{AgentClient: client}
	if checkout, err := checkoutJobInputs(ctx, wrapper, conf, payload, t.TempDir()); err != nil || checkout.ResolvedCommit != initial {
		t.Fatalf("SHA256 first checkout: %+v %v", checkout, err)
	}
	sourceTestGit(t, repository, "branch", "-m", "main", "renamed")
	workspace := t.TempDir()
	if checkout, err := checkoutJobInputs(ctx, wrapper, conf, payload, workspace); err != nil || checkout.ResolvedCommit != initial {
		t.Fatalf("SHA256 pinned checkout: %+v %v", checkout, err)
	}
	if sourceTestGit(t, workspace, "rev-parse", "--show-object-format") != "sha256" {
		t.Fatal("pinned repository lost object format")
	}
}
