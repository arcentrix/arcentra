// Copyright 2025 Arcentra Authors.
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

package sandbox

import (
	"context"
	"fmt"
	"io"
	"strings"
	"syscall"

	"github.com/bytedance/sonic"
	"github.com/containerd/containerd"
	tasks "github.com/containerd/containerd/api/services/tasks/v1"
	runcoptions "github.com/containerd/containerd/api/types/runc/options"
	"github.com/containerd/containerd/cio"
	"github.com/containerd/containerd/containers"
	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/defaults"
	"github.com/containerd/containerd/errdefs"
	"github.com/containerd/containerd/images"
	"github.com/containerd/containerd/oci"
	"github.com/containerd/containerd/snapshots"
	"github.com/opencontainers/go-digest"
	imagev1 "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/opencontainers/runtime-spec/specs-go"
)

// 运行时边界使生命周期测试不依赖 daemon、FIFO 或宿主权限
type runtimeBackend interface {
	snapshotter(context.Context) (string, error)
	inspectOwned(context.Context, ResourceRef) (runtimeContainer, ResourceState, error)
	create(context.Context, string, string, []oci.SpecOpts, map[string]string, bool) (runtimeContainer, error)
	close() error
}

type runtimeContainer interface {
	spec(context.Context) (*specs.Spec, error)
	newTask(context.Context, io.Writer, io.Writer) (runtimeTask, error)
	loadTask(context.Context) (runtimeTask, error)
	remove(context.Context) error
}

type runtimeProcess interface {
	Start(context.Context) error
	Wait(context.Context) (<-chan containerd.ExitStatus, error)
	Kill(context.Context, syscall.Signal, ...containerd.KillOpts) error
	Delete(context.Context, ...containerd.ProcessDeleteOpts) (*containerd.ExitStatus, error)
	Status(context.Context) (containerd.Status, error)
}

type runtimeTask interface {
	runtimeProcess
	Pid() uint32
	Pids(context.Context) ([]containerd.ProcessInfo, error)
	exec(context.Context, string, *specs.Process, io.Reader, io.Writer, io.Writer) (runtimeProcess, error)
}

type containerdBackend struct {
	client      *containerd.Client
	runtimeRoot string
}

func (b *containerdBackend) runtimeOption() containerd.NewContainerOpts {
	if b.runtimeRoot == "" {
		return func(context.Context, *containerd.Client, *containers.Container) error { return nil }
	}
	return containerd.WithRuntime("io.containerd.runc.v2", &runcoptions.Options{Root: b.runtimeRoot})
}

func (b *containerdBackend) create(ctx context.Context, id, image string, options []oci.SpecOpts, labels map[string]string, explicitUser bool) (runtimeContainer, error) {
	ref := ResourceRef{ID: id, Owner: labels[ownerLabel], Namespace: labels[namespaceLabel], Snapshotter: labels[snapshotterLabel]}
	snapshotter := ref.Snapshotter
	if snapshotter == "" {
		return nil, fmt.Errorf("snapshotter must be resolved before create")
	}
	_, existing, err := b.inspectOwned(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("verify resource absence before create: %w", err)
	}
	if existing.ContainerExists || existing.SnapshotExists || existing.TaskExists {
		return nil, fmt.Errorf("resource already exists: %w", errdefs.ErrAlreadyExists)
	}
	img, err := b.client.GetImage(ctx, image)
	if errdefs.IsNotFound(err) {
		img, err = b.client.Pull(ctx, image, containerd.WithPullUnpack, containerd.WithPullSnapshotter(snapshotter))
	}
	if err != nil {
		return nil, fmt.Errorf("resolve image %s: %w", image, err)
	}
	unpacked, err := img.IsUnpacked(ctx, snapshotter)
	if err != nil {
		return nil, fmt.Errorf("inspect image unpack: %w", err)
	}
	if !unpacked {
		if err := img.Unpack(ctx, snapshotter); err != nil {
			return nil, fmt.Errorf("unpack image: %w", err)
		}
	}
	// 显式数字用户路径不读取 rootfs 的 passwd/group，也不在 Agent 进程中挂载 snapshot
	imageOptions := []oci.SpecOpts{oci.WithImageConfig(img)}
	if explicitUser {
		imageOptions, err = imageEnvironmentOptions(ctx, img)
		if err != nil {
			return nil, err
		}
	}
	// 旧版镜像用户语义仍要求 snapshot 先于 WithImageConfig 分配
	options = append(imageOptions, options...)
	container, err := b.client.NewContainer(ctx, id,
		containerd.WithImage(img), containerd.WithSnapshotter(snapshotter), containerd.WithNewSnapshot(id+"-snapshot", img, snapshots.WithLabels(ownershipLabels(ref))),
		containerd.WithNewSpec(options...), containerd.WithContainerLabels(labels), b.runtimeOption())
	if err != nil {
		// RPC 失败可能发生在 daemon 已创建资源之后，保留按 ID 恢复的句柄
		return &containerdContainer{client: b.client, id: id, snapshotter: snapshotter, ref: ref}, fmt.Errorf("create container %s: %w", id, err)
	}
	return &containerdContainer{container: container, client: b.client, id: id, snapshotter: snapshotter, ref: ref}, nil
}

// 仅读取镜像配置内容；不继承用户、附加组、entrypoint 或命令
func imageEnvironmentOptions(ctx context.Context, img containerd.Image) ([]oci.SpecOpts, error) {
	descriptor, err := img.Config(ctx)
	if err != nil {
		return nil, fmt.Errorf("read image config descriptor: %w", err)
	}
	if descriptor.MediaType != imagev1.MediaTypeImageConfig && descriptor.MediaType != images.MediaTypeDockerSchema2Config {
		return nil, fmt.Errorf("unsupported image config media type %q", descriptor.MediaType)
	}
	if descriptor.Size <= 0 || descriptor.Size > 4*1024*1024 {
		return nil, fmt.Errorf("image configuration exceeds supported size")
	}
	// content blobs 按 digest 不可变，先核对实际长度，避免错误描述符放大内存分配
	if len(descriptor.Data) > 0 {
		if int64(len(descriptor.Data)) != descriptor.Size || digest.FromBytes(descriptor.Data) != descriptor.Digest {
			return nil, fmt.Errorf("inline image configuration identity mismatch")
		}
	} else {
		info, infoErr := img.ContentStore().Info(ctx, descriptor.Digest)
		if infoErr != nil {
			return nil, fmt.Errorf("inspect image config blob: %w", infoErr)
		}
		if info.Size != descriptor.Size {
			return nil, fmt.Errorf("image configuration size mismatch")
		}
	}
	data, err := content.ReadBlob(ctx, img.ContentStore(), descriptor)
	if err != nil {
		return nil, fmt.Errorf("read image config blob: %w", err)
	}
	if int64(len(data)) != descriptor.Size {
		return nil, fmt.Errorf("image configuration size mismatch")
	}
	var config imagev1.Image
	if err = sonic.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("decode image config: %w", err)
	}
	if config.OS != "linux" {
		return nil, fmt.Errorf("explicit numeric-user profile requires a Linux image")
	}
	for _, entry := range config.Config.Env {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || validateEnv(map[string]string{key: value}) != nil {
			return nil, fmt.Errorf("invalid image environment variable")
		}
	}
	return []oci.SpecOpts{oci.WithDefaultPathEnv, oci.WithEnv(config.Config.Env)}, nil
}

func (b *containerdBackend) close() error { return b.client.Close() }

type containerdContainer struct {
	container   containerd.Container
	client      *containerd.Client
	id          string
	snapshotter string
	ref         ResourceRef
}

func (c *containerdContainer) resolve(ctx context.Context) (containerd.Container, error) {
	if c.container != nil {
		return c.container, nil
	}
	container, err := c.client.LoadContainer(ctx, c.id)
	if err == nil {
		c.container = container
	}
	return container, err
}
func (c *containerdContainer) spec(ctx context.Context) (*specs.Spec, error) {
	container, err := c.resolve(ctx)
	if err != nil {
		return nil, err
	}
	return container.Spec(ctx)
}
func (c *containerdContainer) newTask(ctx context.Context, stdout, stderr io.Writer) (runtimeTask, error) {
	container, err := c.resolve(ctx)
	if err != nil {
		return nil, err
	}
	task, err := container.NewTask(ctx, cio.NewCreator(cio.WithStreams(nil, stdout, stderr)))
	if err != nil {
		return nil, err
	}
	return &containerdTask{task}, nil
}
func (c *containerdContainer) loadTask(ctx context.Context) (runtimeTask, error) {
	container, err := c.resolve(ctx)
	if err != nil {
		return nil, err
	}
	task, err := container.Task(ctx, nil)
	if err != nil {
		return nil, err
	}
	return &containerdTask{task}, nil
}
func (c *containerdContainer) remove(ctx context.Context) error {
	_, state, err := (&containerdBackend{client: c.client}).inspectOwned(ctx, c.ref)
	if err != nil {
		return err
	}
	if state.TaskExists {
		return fmt.Errorf("task still exists: %w", errdefs.ErrFailedPrecondition)
	}
	// 每次重新读取 metadata，缓存句柄的 NotFound 也必须继续清理已验证的 snapshot
	if state.SnapshotExists {
		if err = c.client.SnapshotService(c.ref.Snapshotter).Remove(ctx, c.ref.ID+"-snapshot"); err != nil && !errdefs.IsNotFound(err) {
			return err
		}
	}
	if state.ContainerExists {
		if err = c.client.ContainerService().Delete(ctx, c.ref.ID); err != nil && !errdefs.IsNotFound(err) {
			return err
		}
	}
	return nil
}

func (b *containerdBackend) snapshotter(ctx context.Context) (string, error) {
	name, err := b.client.GetLabel(ctx, defaults.DefaultSnapshotterNSLabel)
	if err != nil {
		return "", fmt.Errorf("resolve namespaced snapshotter: %w", err)
	}
	if name == "" {
		name = containerd.DefaultSnapshotter
	}
	return name, nil
}

func (b *containerdBackend) inspectOwned(ctx context.Context, ref ResourceRef) (runtimeContainer, ResourceState, error) {
	state := ResourceState{}
	handle := &containerdContainer{client: b.client, id: ref.ID, snapshotter: ref.Snapshotter, ref: ref}
	record, err := b.client.ContainerService().Get(ctx, ref.ID)
	if err != nil && !errdefs.IsNotFound(err) {
		return nil, state, fmt.Errorf("inspect container ownership: %w", err)
	}
	if err == nil {
		if err = verifyOwnership(record.Labels, ref); err != nil {
			return nil, state, err
		}
		if record.Snapshotter != ref.Snapshotter || record.SnapshotKey != ref.ID+"-snapshot" {
			return nil, state, fmt.Errorf("container snapshot identity mismatch: %w", errdefs.ErrFailedPrecondition)
		}
		state.ContainerExists = true
	}
	info, err := b.client.SnapshotService(ref.Snapshotter).Stat(ctx, ref.ID+"-snapshot")
	if err != nil && !errdefs.IsNotFound(err) {
		return nil, state, fmt.Errorf("inspect snapshot ownership: %w", err)
	}
	if err == nil {
		if err = verifyOwnership(info.Labels, ref); err != nil {
			return nil, state, err
		}
		if info.Name != ref.ID+"-snapshot" || info.Kind != snapshots.KindActive {
			return nil, state, fmt.Errorf("snapshot identity or kind mismatch: %w", errdefs.ErrFailedPrecondition)
		}
		state.SnapshotExists = true
	}
	task, err := b.client.TaskService().Get(ctx, &tasks.GetRequest{ContainerID: ref.ID})
	if err != nil && !errdefs.IsNotFound(err) && !errdefs.IsNotFound(errdefs.FromGRPC(err)) {
		return nil, state, fmt.Errorf("inspect task existence: %w", err)
	}
	state.TaskExists = err == nil
	if state.TaskExists && (task == nil || task.Process == nil || task.Process.ID != ref.ID) {
		return nil, state, fmt.Errorf("task identity mismatch: %w", errdefs.ErrFailedPrecondition)
	}
	if state.TaskExists && !state.ContainerExists {
		return nil, state, fmt.Errorf("task exists without verifiable container metadata: %w", errdefs.ErrFailedPrecondition)
	}
	return handle, state, nil
}

type containerdTask struct{ containerd.Task }

func (t *containerdTask) exec(ctx context.Context, id string, process *specs.Process, stdin io.Reader, stdout, stderr io.Writer) (runtimeProcess, error) {
	return t.Task.Exec(ctx, id, process, cio.NewCreator(cio.WithStreams(stdin, stdout, stderr)))
}
