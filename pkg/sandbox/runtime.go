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
	"syscall"

	"github.com/containerd/containerd"
	"github.com/containerd/containerd/cio"
	"github.com/containerd/containerd/defaults"
	"github.com/containerd/containerd/errdefs"
	"github.com/containerd/containerd/oci"
	"github.com/opencontainers/runtime-spec/specs-go"
)

// 运行时边界使生命周期测试不依赖 daemon、FIFO 或宿主权限
type runtimeBackend interface {
	create(context.Context, string, string, []oci.SpecOpts, map[string]string) (runtimeContainer, error)
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
	exec(context.Context, string, *specs.Process, io.Reader, io.Writer, io.Writer) (runtimeProcess, error)
}

type containerdBackend struct{ client *containerd.Client }

func (b *containerdBackend) create(ctx context.Context, id, image string, options []oci.SpecOpts, labels map[string]string) (runtimeContainer, error) {
	snapshotter, err := b.client.GetLabel(ctx, defaults.DefaultSnapshotterNSLabel)
	if err != nil {
		return nil, fmt.Errorf("resolve namespaced snapshotter: %w", err)
	}
	if snapshotter == "" {
		snapshotter = containerd.DefaultSnapshotter
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
	// WithImageConfig 需要已分配的 snapshot 来解析镜像用户和附加组
	// 因此完整 spec 必须在 WithNewSnapshot 之后生成
	options = append([]oci.SpecOpts{oci.WithImageConfig(img)}, options...)
	container, err := b.client.NewContainer(ctx, id,
		containerd.WithImage(img), containerd.WithSnapshotter(snapshotter), containerd.WithNewSnapshot(id+"-snapshot", img),
		containerd.WithNewSpec(options...), containerd.WithContainerLabels(labels))
	if err != nil {
		// RPC 失败可能发生在 daemon 已创建资源之后，保留按 ID 恢复的句柄
		return &containerdContainer{client: b.client, id: id, snapshotter: snapshotter}, fmt.Errorf("create container %s: %w", id, err)
	}
	return &containerdContainer{container: container, client: b.client, id: id, snapshotter: snapshotter}, nil
}

func (b *containerdBackend) close() error { return b.client.Close() }

type containerdContainer struct {
	container   containerd.Container
	client      *containerd.Client
	id          string
	snapshotter string
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
	container, err := c.resolve(ctx)
	if err == nil {
		return container.Delete(ctx, containerd.WithSnapshotCleanup)
	}
	if !errdefs.IsNotFound(err) {
		return err
	}
	// metadata 尚未创建或已删除时，仍需删除可能残留的 snapshot
	err = c.client.SnapshotService(c.snapshotter).Remove(ctx, c.id+"-snapshot")
	if errdefs.IsNotFound(err) {
		return nil
	}
	return err
}

type containerdTask struct{ containerd.Task }

func (t *containerdTask) exec(ctx context.Context, id string, process *specs.Process, stdin io.Reader, stdout, stderr io.Writer) (runtimeProcess, error) {
	return t.Task.Exec(ctx, id, process, cio.NewCreator(cio.WithStreams(stdin, stdout, stderr)))
}
