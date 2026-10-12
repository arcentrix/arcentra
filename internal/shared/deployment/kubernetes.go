// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

// Package deployment 提供受限的 Kubernetes Deployment 镜像发布适配器
package deployment

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/bytedance/sonic"
)

// 发布适配器使用固定错误，避免底层响应泄露地址或认证信息
var (
	ErrUnconfigured  = errors.New("deployment target is not configured")
	ErrUnavailable   = errors.New("Kubernetes API is unavailable")
	ErrSuperseded    = errors.New("Kubernetes workload identity or generation changed")
	ErrConflict      = errors.New("Kubernetes workload changed; automatic overwrite refused")
	ErrInvalidTarget = errors.New("existing unpaused Deployment and named container are required")
)

const releaseAnnotation = "arcentra.io/release-id"

var labelName = regexp.MustCompile(`^[a-z0-9](?:[-a-z0-9]*[a-z0-9])?$`)
var imageDigest = regexp.MustCompile(`^[a-z0-9][a-z0-9._:/-]*@sha256:[a-f0-9]{64}$`)

// Target 只允许操作已绑定的单个 Deployment 容器
type Target struct{ Namespace, Deployment, Container string }

// Receipt 将执行绑定到既有工作负载的 UID 和代次
type Receipt struct {
	UID        string
	Generation int64
}

// Observation 仅从真实控制器状态计算发布结果
type Observation struct {
	Status, Reason                                      string
	ObservedGeneration                                  int64
	DesiredReplicas, UpdatedReplicas, AvailableReplicas int32
}

// Adapter 定义可注入的集群访问契约，Inspect 不执行写入
type Adapter interface {
	Inspect(context.Context, Target) (Receipt, error)
	Apply(context.Context, Target, string, string, Receipt) (Receipt, error)
	Observe(context.Context, Target, string, string, Receipt) (Observation, error)
}

// ValidTarget 校验路径段和容器名称，禁止任意资源与命令
func ValidTarget(t Target) bool {
	if len(t.Namespace) > 63 || len(t.Container) > 63 || len(t.Deployment) > 253 || !labelName.MatchString(t.Namespace) || !labelName.MatchString(t.Container) {
		return false
	}
	for _, part := range strings.Split(t.Deployment, ".") {
		if len(part) > 63 || !labelName.MatchString(part) {
			return false
		}
	}
	return true
}

// ValidImage 只接收固定 SHA256 镜像引用，制品文件摘要不能代替镜像摘要
func ValidImage(image string) bool {
	return len(image) <= 512 && imageDigest.MatchString(image) && !strings.Contains(image, "://") && !strings.Contains(image, "..")
}

// KubernetesAdapter 使用管理员注入的 HTTPS 客户端，不查找本地 kubeconfig 或创建凭据
type KubernetesAdapter struct {
	endpoint string
	client   *http.Client
}

// NewKubernetesAdapter 创建明确指定端点的适配器，拒绝重定向和隐式认证配置
func NewKubernetesAdapter(endpoint string, client *http.Client) (*KubernetesAdapter, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || client == nil {
		return nil, ErrUnconfigured
	}
	clone := *client
	clone.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	clone.Timeout = 20 * time.Second
	return &KubernetesAdapter{endpoint: strings.TrimRight(endpoint, "/"), client: &clone}, nil
}

type kubeDeployment struct {
	Metadata struct {
		UID               string  `json:"uid"`
		ResourceVersion   string  `json:"resourceVersion"`
		Generation        int64   `json:"generation"`
		DeletionTimestamp *string `json:"deletionTimestamp"`
	} `json:"metadata"`
	Spec struct {
		Replicas *int32 `json:"replicas"`
		Paused   bool   `json:"paused"`
		Template struct {
			Metadata struct {
				Annotations map[string]string `json:"annotations"`
			} `json:"metadata"`
			Spec struct {
				Containers []struct {
					Name  string `json:"name"`
					Image string `json:"image"`
				} `json:"containers"`
			} `json:"spec"`
		} `json:"template"`
	} `json:"spec"`
	Status struct {
		ObservedGeneration  int64 `json:"observedGeneration"`
		Replicas            int32 `json:"replicas"`
		UpdatedReplicas     int32 `json:"updatedReplicas"`
		AvailableReplicas   int32 `json:"availableReplicas"`
		UnavailableReplicas int32 `json:"unavailableReplicas"`
		Conditions          []struct {
			Type   string `json:"type"`
			Status string `json:"status"`
			Reason string `json:"reason"`
		} `json:"conditions"`
	} `json:"status"`
}

func (a *KubernetesAdapter) request(ctx context.Context, target Target, method string, body []byte) (*kubeDeployment, error) {
	if a == nil || a.client == nil {
		return nil, ErrUnconfigured
	}
	if !ValidTarget(target) {
		return nil, ErrInvalidTarget
	}
	endpoint := a.endpoint + "/apis/apps/v1/namespaces/" + url.PathEscape(target.Namespace) + "/deployments/" + url.PathEscape(target.Deployment)
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, ErrUnavailable
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json-patch+json")
	}
	response, err := a.client.Do(req)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusConflict || response.StatusCode == http.StatusUnprocessableEntity {
		return nil, ErrConflict
	}
	if response.StatusCode != http.StatusOK {
		return nil, ErrUnavailable
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 2*1024*1024+1))
	if err != nil || len(raw) > 2*1024*1024 {
		return nil, ErrUnavailable
	}
	var value kubeDeployment
	if sonic.Unmarshal(raw, &value) != nil {
		return nil, ErrUnavailable
	}
	if value.Metadata.UID == "" || value.Metadata.ResourceVersion == "" || value.Metadata.Generation <= 0 || value.Metadata.DeletionTimestamp != nil {
		return nil, ErrInvalidTarget
	}
	return &value, nil
}
func containerIndex(d *kubeDeployment, target Target) int {
	for i, c := range d.Spec.Template.Spec.Containers {
		if c.Name == target.Container {
			return i
		}
	}
	return -1
}

// Inspect 读取并校验工作负载，返回必须持久化的提交基线
func (a *KubernetesAdapter) Inspect(ctx context.Context, target Target) (Receipt, error) {
	d, err := a.request(ctx, target, http.MethodGet, nil)
	if err != nil {
		return Receipt{}, err
	}
	if d.Spec.Paused || containerIndex(d, target) < 0 {
		return Receipt{}, ErrInvalidTarget
	}
	return Receipt{d.Metadata.UID, d.Metadata.Generation}, nil
}

// Apply 通过 UID、代次及 resourceVersion 条件补丁更新单一容器，并支持提交后崩溃重放
func (a *KubernetesAdapter) Apply(ctx context.Context, target Target, releaseID, image string, baseline Receipt) (Receipt, error) {
	if !ValidImage(image) || releaseID == "" || len(releaseID) > 64 || baseline.UID == "" || baseline.Generation <= 0 {
		return Receipt{}, ErrInvalidTarget
	}
	d, err := a.request(ctx, target, http.MethodGet, nil)
	if err != nil {
		return Receipt{}, err
	}
	index := containerIndex(d, target)
	if index < 0 || d.Spec.Paused {
		return Receipt{}, ErrInvalidTarget
	}
	if d.Metadata.UID != baseline.UID {
		return Receipt{}, ErrSuperseded
	}
	if d.Spec.Template.Metadata.Annotations[releaseAnnotation] == releaseID {
		if d.Spec.Template.Spec.Containers[index].Image != image || d.Metadata.Generation != baseline.Generation+1 {
			return Receipt{}, ErrSuperseded
		}
		return Receipt{d.Metadata.UID, d.Metadata.Generation}, nil
	}
	if d.Metadata.Generation != baseline.Generation {
		return Receipt{}, ErrSuperseded
	}
	patch := []map[string]any{
		{"op": "test", "path": "/metadata/uid", "value": baseline.UID},
		{"op": "test", "path": "/metadata/resourceVersion", "value": d.Metadata.ResourceVersion},
		{"op": "test", "path": "/metadata/generation", "value": baseline.Generation},
		{"op": "test", "path": "/spec/template/spec/containers/" + strconv.Itoa(index) + "/name", "value": target.Container},
		{"op": "replace", "path": "/spec/template/spec/containers/" + strconv.Itoa(index) + "/image", "value": image},
	}
	if d.Spec.Template.Metadata.Annotations == nil {
		patch = append(patch, map[string]any{"op": "add", "path": "/spec/template/metadata/annotations", "value": map[string]string{}})
	}
	patch = append(patch, map[string]any{"op": "add", "path": "/spec/template/metadata/annotations/arcentra.io~1release-id", "value": releaseID})
	body, err := sonic.Marshal(patch)
	if err != nil {
		return Receipt{}, ErrUnavailable
	}
	if guard, ok := ctx.Value(applyGuardKey{}).(func(context.Context) error); ok {
		if guard == nil {
			return Receipt{}, ErrUnavailable
		}
		if err := guard(ctx); err != nil {
			return Receipt{}, err
		}
	}
	if err := ctx.Err(); err != nil {
		return Receipt{}, err
	}
	applied, err := a.request(ctx, target, http.MethodPatch, body)
	if err != nil {
		return Receipt{}, err
	}
	appliedIndex := containerIndex(applied, target)
	if applied.Metadata.UID != baseline.UID || applied.Metadata.Generation != baseline.Generation+1 || appliedIndex < 0 || applied.Spec.Template.Spec.Containers[appliedIndex].Image != image || applied.Spec.Template.Metadata.Annotations[releaseAnnotation] != releaseID {
		return Receipt{}, ErrSuperseded
	}
	return Receipt{applied.Metadata.UID, applied.Metadata.Generation}, nil
}

// Observe 只在相同发布代次的所有目标副本可用时确认成功，旧状态和 API 错误不算成功
func (a *KubernetesAdapter) Observe(ctx context.Context, target Target, releaseID, image string, receipt Receipt) (Observation, error) {
	d, err := a.request(ctx, target, http.MethodGet, nil)
	if err != nil {
		return Observation{}, err
	}
	index := containerIndex(d, target)
	if d.Metadata.UID != receipt.UID || d.Metadata.Generation != receipt.Generation || index < 0 || d.Spec.Template.Spec.Containers[index].Image != image || d.Spec.Template.Metadata.Annotations[releaseAnnotation] != releaseID {
		return Observation{Status: "superseded", Reason: "Workload identity or release generation changed"}, nil
	}
	desired := int32(1)
	if d.Spec.Replicas != nil {
		desired = *d.Spec.Replicas
	}
	observation := Observation{Status: "observing", Reason: "Waiting for Kubernetes rollout", ObservedGeneration: d.Status.ObservedGeneration, DesiredReplicas: desired, UpdatedReplicas: d.Status.UpdatedReplicas, AvailableReplicas: d.Status.AvailableReplicas}
	if d.Spec.Paused {
		observation.Reason = "Deployment is paused"
		return observation, nil
	}
	if d.Status.ObservedGeneration < receipt.Generation {
		return observation, nil
	}
	for _, condition := range d.Status.Conditions {
		if condition.Type == "Progressing" && condition.Status == "False" && condition.Reason == "ProgressDeadlineExceeded" {
			observation.Status = "failed"
			observation.Reason = "Kubernetes reported ProgressDeadlineExceeded"
			return observation, nil
		}
	}
	if desired > 0 && d.Status.UpdatedReplicas == desired && d.Status.Replicas == desired && d.Status.AvailableReplicas == desired && d.Status.UnavailableReplicas == 0 {
		observation.Status = "succeeded"
		observation.Reason = "Kubernetes observed all updated replicas available"
	}
	if desired == 0 {
		observation.Reason = "Deployment is scaled to zero; workload availability cannot be confirmed"
	}
	return observation, nil
}

// TargetKey 返回用于诊断的无凭据目标名称
func TargetKey(t Target) string {
	return fmt.Sprintf("%s/%s/%s", t.Namespace, t.Deployment, t.Container)
}

// applyGuardKey 仅携带本次请求的最终写入校验，不存储执行上下文
type applyGuardKey struct{}

// WithApplyGuard 在读取工作负载之后、发送 PATCH 之前重新检查当次授权与取消状态
func WithApplyGuard(ctx context.Context, guard func(context.Context) error) context.Context {
	return context.WithValue(ctx, applyGuardKey{}, guard)
}
