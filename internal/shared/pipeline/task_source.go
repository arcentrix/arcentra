package pipeline

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"

	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
	"github.com/arcentrix/arcentra/pkg/sourceprovenance"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
)

func (tf *TaskFramework) resolveSourceExpectation(job *spec.Job) (*taskqueue.SourcePayload, *sourceprovenance.Binding, error) {
	if job.Source == nil {
		return nil, nil, nil
	}
	source := job.Source
	payload := &taskqueue.SourcePayload{Type: source.Type, Repo: tf.execCtx.ResolveVariable(source.Repo), Branch: tf.execCtx.ResolveVariable(source.Branch)}
	if source.Auth != nil {
		payload.Auth = map[string]string{"username": tf.execCtx.ResolveVariable(source.Auth.Username), "password": tf.execCtx.ResolveVariable(source.Auth.Password), "token": tf.execCtx.ResolveVariable(source.Auth.Token)}
	}
	// 与 Agent 的克隆条件保持一致：空类型禁用检出，非法配置在排队前拒绝
	if payload.Type == "" {
		return payload, nil, nil
	}
	if !strings.EqualFold(payload.Type, "git") || payload.Repo == "" {
		return nil, nil, fmt.Errorf("source requires a supported Git type and repository")
	}
	values := []string{payload.Type, payload.Repo, payload.Branch}
	secrets := []string{payload.Auth["password"], payload.Auth["token"]}
	for name, value := range tf.execCtx.Env {
		if strings.HasPrefix(name, "secrets.") {
			secrets = append(secrets, value)
		}
	}
	for _, value := range values {
		candidates := []string{value}
		if decoded, err := url.PathUnescape(value); err == nil {
			candidates = append(candidates, decoded)
		}
		if decoded, err := url.QueryUnescape(value); err == nil {
			candidates = append(candidates, decoded)
		}
		for _, secret := range secrets {
			if secret == "" {
				continue
			}
			variants := []string{secret, base64.StdEncoding.EncodeToString([]byte(secret)), base64.RawStdEncoding.EncodeToString([]byte(secret)), base64.URLEncoding.EncodeToString([]byte(secret)), base64.RawURLEncoding.EncodeToString([]byte(secret))}
			for _, candidate := range candidates {
				for _, variant := range variants {
					if strings.Contains(candidate, variant) {
						return nil, nil, fmt.Errorf("source identity must not contain known credentials")
					}
				}
			}
		}
	}
	binding, err := sourceprovenance.NewBinding(tf.execCtx.PipelineIDRef, tf.execCtx.PipelineRunID, tf.execCtx.JobRunID, payload.Type, payload.Repo, payload.Branch)
	if err != nil {
		return nil, nil, err
	}
	payload.BindingDigest = binding.Digest
	return payload, binding, nil
}
