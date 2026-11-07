// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"

	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
	tmpl "github.com/arcentrix/arcentra/internal/shared/pipeline/template"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/validation"
	"github.com/arcentrix/arcentra/pkg/retryapproval"
	"github.com/arcentrix/arcentra/pkg/sourceprovenance"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

var errCopilotDefinitionCapture = errors.New("prospective whole-job definition capture unavailable")

// This unexported receipt is produced only by the server loader below. It has
// no browser/model constructor. Deterministic bytes own the resolved definition;
// the scheduling copy returned to the trigger cannot mutate this receipt.
type copilotDefinitionLoadReceipt struct {
	projectID, pipelineID, repository, path, revision, configDigest string
	originalContentDigest, resolvedContentDigest                    string
	resolvedSpec                                                    []byte
	triggerVariables                                                map[string]string
}

type copilotDefinitionEnvelope struct {
	Version                uint32            `json:"version"`
	ProjectID              string            `json:"projectId"`
	PipelineID             string            `json:"pipelineId"`
	Repository             string            `json:"repository"`
	Path                   string            `json:"path"`
	Revision               string            `json:"revision"`
	RepositoryConfigDigest string            `json:"repositoryConfigDigest"`
	OriginalContentDigest  string            `json:"originalContentDigest"`
	ResolvedContentDigest  string            `json:"resolvedContentDigest"`
	ResolvedSpec           []byte            `json:"resolvedSpec"`
	TriggerVariables       map[string]string `json:"triggerVariables"`
	SecretReferences       []string          `json:"secretReferences"`
}

func definitionBytesDigest(domain string, body []byte) string {
	sum := sha256.Sum256(append([]byte(domain+"\x00"), body...))
	return "v1:" + hex.EncodeToString(sum[:])
}

// Internal opt-in hook only: no feature flag, initializer, route or trigger is
// changed here. Loading, includes resolution and parsing each happen once.
func (s *PipelineServiceImpl) loadDefinitionForRerunCapture(ctx context.Context, pipeline *model.Pipeline, project *model.Project, variables map[string]string) (*copilotDefinitionLoadReceipt, *pipelinev1.Spec, bool, error) {
	if s == nil || pipeline == nil || project == nil || ctx.Err() != nil {
		return nil, nil, false, errCopilotDefinitionCapture
	}
	p, q := *pipeline, *project
	variables = maps.Clone(variables)
	config, err := repo.CopilotDefinitionRepositoryConfigDigest(&p, &q)
	if err != nil {
		return nil, nil, false, errCopilotDefinitionCapture
	}
	vars, err := json.Marshal(variables)
	if err != nil {
		return nil, nil, false, errCopilotDefinitionCapture
	}
	if variables == nil {
		variables = map[string]string{}
		vars = []byte("{}")
	}
	if _, err = repo.CopilotRerunTriggerVariablesDigest(string(vars)); err != nil {
		return nil, nil, false, errCopilotDefinitionCapture
	}
	content, revision, err := s.loadDefinitionFromRepo(ctx, &p, &q)
	if err != nil || len(content) == 0 || len(content) > repo.CopilotRerunMaxDefinitionBytes || len(revision) != 40 || strings.ToLower(revision) != revision || !sourceprovenance.ValidCommit(revision) {
		return nil, nil, false, errCopilotDefinitionCapture
	}
	var resolver tmpl.ITemplateResolver = copilotMissingTemplateResolver{}
	if s.templateResolver != nil {
		resolver = s.templateResolver
	}
	resolved, err := tmpl.ResolveIncludes(ctx, content, resolver, model.TemplateScopeProject, p.ProjectID)
	if err != nil || len(resolved) > repo.CopilotRerunMaxDefinitionBytes {
		return nil, nil, false, errCopilotDefinitionCapture
	}
	parsed, err := spec.ParseContentToProto(resolved, pipelinev1.SpecFormat_SPEC_FORMAT_UNSPECIFIED)
	if err != nil {
		return nil, nil, false, errCopilotDefinitionCapture
	}
	if err = validation.NewSchemaValidatorWithRuntimePolicy(s.offlineRuntimePolicy).Validate(parsed); err != nil {
		return nil, nil, false, errCopilotDefinitionCapture
	}
	encoded, err := (proto.MarshalOptions{Deterministic: true}).Marshal(parsed)
	if err != nil || len(encoded) > repo.CopilotRerunMaxDefinitionBytes || ctx.Err() != nil {
		return nil, nil, false, errCopilotDefinitionCapture
	}
	repository, err := sourceprovenance.CanonicalRepository(p.RepoURL)
	if err != nil {
		return nil, nil, false, errCopilotDefinitionCapture
	}
	receipt := &copilotDefinitionLoadReceipt{projectID: p.ProjectID, pipelineID: p.PipelineID, repository: repository, path: p.PipelineFilePath, revision: revision, configDigest: config, originalContentDigest: definitionBytesDigest("arcentra-definition-loaded-v1", []byte(content)), resolvedContentDigest: definitionBytesDigest("arcentra-definition-resolved-v1", []byte(resolved)), resolvedSpec: bytes.Clone(encoded), triggerVariables: variables}
	return receipt, proto.Clone(parsed).(*pipelinev1.Spec), content != resolved, nil
}

// The general loader historically skips includes when its resolver is absent.
// A prospective immutable receipt must instead fail if expansion was needed.
type copilotMissingTemplateResolver struct{}

func (copilotMissingTemplateResolver) ResolveTemplate(context.Context, string, string, string, map[string]any, string, string) (string, error) {
	return "", errCopilotDefinitionCapture
}

func copilotDefinitionLayout(parsed *pipelinev1.Spec, policy taskqueue.OfflineRuntimePolicy, timeout int64) (repo.CopilotRerunLayout, error) {
	invalid := repo.CopilotRerunLayout{}
	if parsed == nil || copilotDefinitionHasUnknownFields(parsed.ProtoReflect()) || len(parsed.Jobs) != 1 || parsed.Jobs[0] == nil {
		return invalid, errCopilotDefinitionCapture
	}
	if validation.NewSchemaValidatorWithRuntimePolicy(policy).Validate(parsed) != nil {
		return invalid, errCopilotDefinitionCapture
	}
	job := parsed.Jobs[0]
	if len(job.DependsOn) != 0 || job.When != "" || job.Concurrency != "" || job.Target != nil || job.Notify != nil || len(job.Triggers) != 0 || !validation.IsDurableRetryApproval(job, policy) || taskqueue.ValidateWholeJobRetryPolicy(job.Retry) != nil || job.Source == nil || job.Source.Type != "git" {
		return invalid, errCopilotDefinitionCapture
	}
	// The initial rerun slice has no fresh SCM credential-resolution seam.
	// Historical literal credentials are not authority to reuse them later.
	if auth := job.Source.Auth; auth != nil && (auth.Username != "" || auth.Password != "" || auth.Token != "") {
		return invalid, errCopilotDefinitionCapture
	}
	canonical, err := sourceprovenance.CanonicalRepository(job.Source.Repo)
	if err != nil || canonical != job.Source.Repo {
		return invalid, errCopilotDefinitionCapture
	}
	runtime, err := validation.ResolveRuntime(parsed, policy)
	if err != nil || runtime == nil || runtime.Type != taskqueue.RuntimeContainerdOfflineV1 {
		return invalid, errCopilotDefinitionCapture
	}
	layout := repo.CopilotRerunLayout{Version: 1, JobName: job.Name, Runtime: *runtime, Retry: proto.Clone(job.Retry).(*pipelinev1.Retry), ApprovalTimeoutSeconds: timeout}
	for i, step := range job.Steps {
		if step == nil || step.When != "" || !step.RunOnAgent {
			return invalid, errCopilotDefinitionCapture
		}
		layout.Steps = append(layout.Steps, repo.CopilotRerunStepLayout{Name: step.Name, Index: int32(i), Uses: step.Uses, Action: step.Action, ContinueOnError: step.ContinueOnError})
	}
	if _, err = layout.Canonical(); err != nil {
		return invalid, errCopilotDefinitionCapture
	}
	return layout, nil
}

// Unknown wire fields cannot become latent execution semantics after an upgrade.
func copilotDefinitionHasUnknownFields(m protoreflect.Message) bool {
	if len(m.GetUnknown()) != 0 {
		return true
	}
	unknown := false
	m.Range(func(f protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		inspect := func(kind protoreflect.Kind, item protoreflect.Value) {
			if kind == protoreflect.MessageKind || kind == protoreflect.GroupKind {
				unknown = unknown || copilotDefinitionHasUnknownFields(item.Message())
			}
		}
		switch {
		case f.IsMap():
			v.Map().Range(func(_ protoreflect.MapKey, item protoreflect.Value) bool {
				inspect(f.MapValue().Kind(), item)
				return !unknown
			})
		case f.IsList():
			for i := 0; i < v.List().Len() && !unknown; i++ {
				inspect(f.Kind(), v.List().Get(i))
			}
		default:
			inspect(f.Kind(), v)
		}
		return !unknown
	})
	return unknown
}

// Match the execution coordinator's reference syntax on actual protobuf strings,
// not JSON-escaped text. These are names only, never resolved secret values.
var copilotDefinitionSecretReference = regexp.MustCompile(`\${{?\s*secrets\.(\w+)\s*}}?`)

func copilotDefinitionSecretReferences(parsed *pipelinev1.Spec, variables map[string]string) []string {
	names := map[string]bool{}
	inspect := func(text string) {
		for _, m := range copilotDefinitionSecretReference.FindAllStringSubmatch(text, -1) {
			names[m[1]] = true
		}
	}
	for _, value := range variables {
		inspect(value)
	}
	var message func(protoreflect.Message)
	var value func(protoreflect.Kind, protoreflect.Value)
	value = func(kind protoreflect.Kind, v protoreflect.Value) {
		switch kind {
		case protoreflect.StringKind:
			inspect(v.String())
		case protoreflect.MessageKind, protoreflect.GroupKind:
			message(v.Message())
		}
	}
	message = func(m protoreflect.Message) {
		m.Range(func(f protoreflect.FieldDescriptor, v protoreflect.Value) bool {
			switch {
			case f.IsList():
				for i := 0; i < v.List().Len(); i++ {
					value(f.Kind(), v.List().Get(i))
				}
			case f.IsMap():
				v.Map().Range(func(_ protoreflect.MapKey, v protoreflect.Value) bool { value(f.MapValue().Kind(), v); return true })
			default:
				value(f.Kind(), v)
			}
			return true
		})
	}
	if parsed != nil {
		message(parsed.ProtoReflect())
	}
	result := make([]string, 0, len(names))
	for name := range names {
		result = append(result, name)
	}
	slices.Sort(result)
	return result
}
func copilotDefinitionReferencesDigest(refs []string) string {
	b, _ := json.Marshal(refs)
	return definitionBytesDigest("arcentra-definition-secret-references-v1", b)
}
func copilotDefinitionCipherBinding(snapshot *model.CopilotDefinitionSnapshot) copilotRerunCipherBinding {
	return copilotRerunCipherBinding{Version: 1, Role: "definition", ProjectID: snapshot.ProjectID, PipelineID: snapshot.PipelineID, RunID: snapshot.RunID, ObjectID: snapshot.SnapshotID, ContentBinding: snapshot.ContentBinding}
}

func buildCopilotDefinitionSnapshot(receipt *copilotDefinitionLoadReceipt, pipeline *model.Pipeline, project *model.Project, run *model.PipelineRun, parsed *pipelinev1.Spec, policy taskqueue.OfflineRuntimePolicy, secretService *SecretService, now time.Time) (*model.CopilotDefinitionSnapshot, error) {
	if receipt == nil || pipeline == nil || project == nil || run == nil || parsed == nil || secretService == nil {
		return nil, errCopilotDefinitionCapture
	}
	cipher, err := newCopilotRerunCipher(secretService.encryptKey)
	if err != nil {
		return nil, errCopilotDefinitionCapture
	}
	config, err := repo.CopilotDefinitionRepositoryConfigDigest(pipeline, project)
	if err != nil || config != receipt.configDigest || receipt.projectID != project.ProjectID || receipt.pipelineID != pipeline.PipelineID || run.PipelineID != receipt.pipelineID || run.DefinitionCommitSha != receipt.revision || run.DefinitionPath != receipt.path {
		return nil, errCopilotDefinitionCapture
	}
	encoded, err := (proto.MarshalOptions{Deterministic: true}).Marshal(parsed)
	if err != nil || !bytes.Equal(encoded, receipt.resolvedSpec) {
		return nil, errCopilotDefinitionCapture
	}
	variables, err := json.Marshal(receipt.triggerVariables)
	if err != nil {
		return nil, errCopilotDefinitionCapture
	}
	variableDigest, err := repo.CopilotRerunTriggerVariablesDigest(string(variables))
	if err != nil {
		return nil, errCopilotDefinitionCapture
	}
	actualDigest, err := repo.CopilotRerunTriggerVariablesDigest(run.Env)
	if err != nil || actualDigest != variableDigest {
		return nil, errCopilotDefinitionCapture
	}
	context, err := retryapproval.ParseRunContext(run.RetryApprovalContext)
	if err != nil {
		return nil, errCopilotDefinitionCapture
	}
	layout, err := copilotDefinitionLayout(parsed, policy, context.Policy.TimeoutSeconds)
	if err != nil {
		return nil, errCopilotDefinitionCapture
	}
	specDigest, err := validation.ResolvedDefinitionDigest(parsed)
	if err != nil {
		return nil, errCopilotDefinitionCapture
	}
	refs := copilotDefinitionSecretReferences(parsed, receipt.triggerVariables)
	now = now.UTC().Truncate(time.Millisecond)
	metadata, err := repo.NewCopilotDefinitionSnapshotMetadata(run, repo.CopilotDefinitionSnapshotInput{ProjectID: receipt.projectID, DefinitionRepository: receipt.repository, DefinitionPath: receipt.path, DefinitionRevision: receipt.revision, RepositoryConfigDigest: receipt.configDigest, TriggerVariablesDigest: variableDigest, PayloadSchemaVersion: 1, ResolvedSpecDigest: specDigest, SecretReferencesDigest: copilotDefinitionReferencesDigest(refs), Layout: layout, CipherVersion: 1, ExpiresAt: now.Add(repo.CopilotRerunDefinitionTTL)}, now)
	if err != nil {
		return nil, errCopilotDefinitionCapture
	}
	envelope := copilotDefinitionEnvelope{Version: 1, ProjectID: receipt.projectID, PipelineID: receipt.pipelineID, Repository: receipt.repository, Path: receipt.path, Revision: receipt.revision, RepositoryConfigDigest: receipt.configDigest, OriginalContentDigest: receipt.originalContentDigest, ResolvedContentDigest: receipt.resolvedContentDigest, ResolvedSpec: bytes.Clone(receipt.resolvedSpec), TriggerVariables: maps.Clone(receipt.triggerVariables), SecretReferences: refs}
	plain, err := json.Marshal(envelope)
	if err != nil {
		return nil, errCopilotDefinitionCapture
	}
	sealed, err := cipher.seal(copilotDefinitionCipherBinding(metadata), plain)
	if err != nil {
		return nil, errCopilotDefinitionCapture
	}
	result, err := repo.FinalizeCopilotDefinitionSnapshot(metadata, sealed)
	if err != nil {
		return nil, errCopilotDefinitionCapture
	}
	return result, nil
}

// Decrypt and verify against the stored immutable binding; never reload HEAD or
// templates. This is content validation only, not rerun admission or execution.
func openCopilotDefinitionSnapshot(snapshot *model.CopilotDefinitionSnapshot, policy taskqueue.OfflineRuntimePolicy, secretService *SecretService) (*pipelinev1.Spec, map[string]string, []string, error) {
	fail := func() (*pipelinev1.Spec, map[string]string, []string, error) {
		return nil, nil, nil, errCopilotDefinitionCapture
	}
	if repo.ValidateCopilotDefinitionSnapshot(snapshot) != nil || secretService == nil {
		return fail()
	}
	cipher, err := newCopilotRerunCipher(secretService.encryptKey)
	if err != nil {
		return fail()
	}
	plain, err := cipher.open(copilotDefinitionCipherBinding(snapshot), snapshot.PayloadCiphertext)
	if err != nil {
		return fail()
	}
	var e copilotDefinitionEnvelope
	decoder := json.NewDecoder(bytes.NewReader(plain))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&e) != nil || decoder.Decode(new(any)) != io.EOF || e.Version != 1 || e.ProjectID != snapshot.ProjectID || e.PipelineID != snapshot.PipelineID || e.Repository != snapshot.DefinitionRepository || e.Path != snapshot.DefinitionPath || e.Revision != snapshot.DefinitionRevision || e.RepositoryConfigDigest != snapshot.RepositoryConfigDigest || !sourceprovenance.ValidBindingDigest(e.OriginalContentDigest) || !sourceprovenance.ValidBindingDigest(e.ResolvedContentDigest) {
		return fail()
	}
	parsed := new(pipelinev1.Spec)
	if proto.Unmarshal(e.ResolvedSpec, parsed) != nil {
		return fail()
	}
	encoded, err := (proto.MarshalOptions{Deterministic: true}).Marshal(parsed)
	if err != nil || !bytes.Equal(encoded, e.ResolvedSpec) {
		return fail()
	}
	digest, err := validation.ResolvedDefinitionDigest(parsed)
	if err != nil || digest != snapshot.ResolvedSpecDigest {
		return fail()
	}
	variables, err := json.Marshal(e.TriggerVariables)
	if err != nil {
		return fail()
	}
	digest, err = repo.CopilotRerunTriggerVariablesDigest(string(variables))
	if err != nil || digest != snapshot.TriggerVariablesDigest {
		return fail()
	}
	refs := copilotDefinitionSecretReferences(parsed, e.TriggerVariables)
	if !slices.Equal(refs, e.SecretReferences) || copilotDefinitionReferencesDigest(refs) != snapshot.SecretReferencesDigest {
		return fail()
	}
	context, err := retryapproval.ParseRunContext(snapshot.RetryApprovalContext)
	if err != nil {
		return fail()
	}
	layout, err := copilotDefinitionLayout(parsed, policy, context.Policy.TimeoutSeconds)
	if err != nil {
		return fail()
	}
	canonical, err := layout.Canonical()
	if err != nil || canonical != snapshot.LayoutJSON {
		return fail()
	}
	return parsed, maps.Clone(e.TriggerVariables), slices.Clone(refs), nil
}
