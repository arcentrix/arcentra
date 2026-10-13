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
	"bytes"
	"context"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/containerd/containerd"
	introspectionapi "github.com/containerd/containerd/api/services/introspection/v1"
	tasks "github.com/containerd/containerd/api/services/tasks/v1"
	tasktypes "github.com/containerd/containerd/api/types/task"
	"github.com/containerd/containerd/containers"
	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/content/local"
	"github.com/containerd/containerd/defaults"
	"github.com/containerd/containerd/errdefs"
	"github.com/containerd/containerd/images"
	"github.com/containerd/containerd/leases"
	"github.com/containerd/containerd/mount"
	"github.com/containerd/containerd/namespaces"
	"github.com/containerd/containerd/oci"
	"github.com/containerd/containerd/services/introspection"
	"github.com/containerd/containerd/snapshots"
	"github.com/opencontainers/go-digest"
	imagebase "github.com/opencontainers/image-spec/specs-go"
	imagev1 "github.com/opencontainers/image-spec/specs-go/v1"
	"google.golang.org/grpc"
)

// 此 fixture 使用真实 containerd Client / Image / WithNewSpec / WithImageConfig
// 仅存储与 snapshot 服务为内存实现；空 rootfs 不需要 mount 系统调用
func TestContainerdAdapterCreatesSnapshotBeforeImageSpec(t *testing.T) {
	previousTemp := os.Getenv("XDG_RUNTIME_DIR")
	if previousTemp == "" {
		previousTemp = os.TempDir()
	}
	if err := mount.SetTempMountLocation(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := mount.SetTempMountLocation(previousTemp); err != nil {
			t.Errorf("restore SDK temp directory: %v", err)
		}
	})
	ctx := leases.WithLease(namespaces.WithNamespace(context.Background(), "arcentra-test"), "fixture-lease")
	store, err := local.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	writeBlob := func(media string, value any) imagev1.Descriptor {
		data, err := sonic.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		desc := imagev1.Descriptor{MediaType: media, Digest: digest.FromBytes(data), Size: int64(len(data))}
		if err := content.WriteBlob(ctx, store, desc.Digest.String(), bytes.NewReader(data), desc); err != nil {
			t.Fatal(err)
		}
		return desc
	}
	config := writeBlob(imagev1.MediaTypeImageConfig, imagev1.Image{Platform: imagev1.Platform{OS: "linux", Architecture: "amd64"}, Config: imagev1.ImageConfig{Env: []string{"IMAGE=fixture"}, Cmd: []string{"image-default"}, WorkingDir: "/image"}, RootFS: imagev1.RootFS{Type: "layers", DiffIDs: []digest.Digest{digest.FromString("empty-rootfs-fixture")}}})
	manifest := writeBlob(imagev1.MediaTypeImageManifest, imagev1.Manifest{Versioned: imagebase.Versioned{SchemaVersion: 2}, Config: config})
	snapshotter := &fixtureSnapshotter{prepared: make(map[string]bool)}
	containerStore := &fixtureContainerStore{records: make(map[string]containers.Container)}
	imageStore := &fixtureImages{image: images.Image{Name: "fixture:local", Target: manifest}}
	client, err := containerd.New("", containerd.WithServices(
		containerd.WithContentStore(store), containerd.WithImageStore(imageStore),
		containerd.WithNamespaceService(fixtureNamespaces{}), containerd.WithSnapshotters(map[string]snapshots.Snapshotter{"fixture": snapshotter}),
		containerd.WithIntrospectionService(fixtureIntrospection{}), containerd.WithTaskClient(fixtureTasks{}), containerd.WithContainerStore(containerStore)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	options, err := createSpecOptions(&CreateOptions{Command: []string{"requested-init"}, Env: map[string]string{"STEP": "explicit"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// 回归证明：旧顺序确实无法解析镜像组信息
	img, err := client.GetImage(ctx, "fixture:local")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := oci.GenerateSpec(ctx, client, &containers.Container{ID: "before-snapshot"}, oci.WithImageConfig(img)); err == nil {
		t.Fatal("fixture failed to exercise image rootfs dependency")
	}
	backend := &containerdBackend{client: client}
	ref := ResourceRef{ID: "fixture-container", Owner: "test", Namespace: "arcentra-test", Snapshotter: "fixture"}
	container, err := backend.create(ctx, ref.ID, "fixture:local", options, ownershipLabels(ref), false)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := container.spec(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(spec.Process.Args, []string{"requested-init"}) || !slices.Contains(spec.Process.Env, "IMAGE=fixture") || !slices.Contains(spec.Process.Env, "STEP=explicit") || spec.Process.Cwd != "/image" {
		t.Fatalf("image config or override was lost: %+v", spec.Process)
	}
	if snapshotter.mountReads == 0 || !snapshotter.prepared["fixture-container-snapshot"] {
		t.Fatal("image config did not consult prepared rootfs snapshot")
	}
	if container.(*containerdContainer).snapshotter != "fixture" {
		t.Fatal("namespaced snapshotter was not preserved")
	}
	// 显式数字用户不允许 SDK 为镜像用户或附加组执行宿主 mount
	snapshotter.mountErr = fmt.Errorf("client-side rootfs access forbidden")
	namedConfig := writeBlob(imagev1.MediaTypeImageConfig, imagev1.Image{Platform: imagev1.Platform{OS: "linux", Architecture: "amd64"}, Config: imagev1.ImageConfig{Env: []string{"IMAGE=fixture"}, User: "missing-user:missing-group", Entrypoint: []string{"image-entrypoint"}, Cmd: []string{"image-command"}, WorkingDir: "/image-workdir"}, RootFS: imagev1.RootFS{Type: "layers", DiffIDs: []digest.Digest{digest.FromString("empty-rootfs-fixture")}}})
	imageStore.image.Target = writeBlob(imagev1.MediaTypeImageManifest, imagev1.Manifest{Versioned: imagebase.Versioned{SchemaVersion: 2}, Config: namedConfig})
	explicitOptions, err := createSpecOptions(&CreateOptions{Command: []string{"explicit-init"}, User: "1000:1001", WorkingDir: "/chosen", Env: map[string]string{"STEP": "explicit"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	explicitRef := ref
	explicitRef.ID = "explicit-user-container"
	explicit, err := backend.create(ctx, explicitRef.ID, "fixture:local", explicitOptions, ownershipLabels(explicitRef), true)
	if err != nil {
		t.Fatalf("explicit init required rootfs mount: %v", err)
	}
	explicitSpec, err := explicit.spec(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if explicitSpec.Process.User.UID != 1000 || explicitSpec.Process.User.GID != 1001 || explicitSpec.Process.Cwd != "/chosen" || !slices.Equal(explicitSpec.Process.Args, []string{"explicit-init"}) || !slices.Contains(explicitSpec.Process.Env, "IMAGE=fixture") || !slices.Contains(explicitSpec.Process.Env, "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin") {
		t.Fatalf("incorrect nonmounting image configuration: %+v", explicitSpec.Process)
	}
	snapshotter.mountErr = nil
	// 仅 snapshot 存在但 metadata 不存在的恢复路径必须使用相同 snapshotter
	orphanRef := ResourceRef{ID: "orphan", Owner: "test", Namespace: "arcentra-test", Snapshotter: "fixture"}
	orphan := &containerdContainer{client: client, id: "orphan", snapshotter: "fixture", ref: orphanRef}
	snapshotter.prepared["orphan-snapshot"] = true
	snapshotter.labels["orphan-snapshot"] = ownershipLabels(orphanRef)
	if err := orphan.remove(ctx); err != nil {
		t.Fatal(err)
	}
	if snapshotter.prepared["orphan-snapshot"] {
		t.Fatal("orphan snapshot was not removed from namespaced snapshotter")
	}
}

type fixtureImages struct {
	images.Store
	image images.Image
}

func (s *fixtureImages) Get(context.Context, string) (images.Image, error) { return s.image, nil }

type fixtureNamespaces struct{ namespaces.Store }

func (fixtureNamespaces) Labels(ctx context.Context, _ string) (map[string]string, error) {
	if ns, ok := namespaces.Namespace(ctx); !ok || ns != "arcentra-test" {
		return nil, fmt.Errorf("namespace missing")
	}
	return map[string]string{defaults.DefaultSnapshotterNSLabel: "fixture"}, nil
}

type fixtureIntrospection struct{ introspection.Service }

func (fixtureIntrospection) Plugins(context.Context, []string) (*introspectionapi.PluginsResponse, error) {
	return &introspectionapi.PluginsResponse{Plugins: []*introspectionapi.Plugin{{ID: "fixture"}}}, nil
}

type fixtureSnapshotter struct {
	snapshots.Snapshotter
	prepared   map[string]bool
	mountReads int
	mountErr   error
	labels     map[string]map[string]string
	statErr    error
	removeErr  error
}

func (s *fixtureSnapshotter) Stat(_ context.Context, key string) (snapshots.Info, error) {
	if s.statErr != nil {
		return snapshots.Info{}, s.statErr
	}
	if strings.HasSuffix(key, "-snapshot") {
		if !s.prepared[key] {
			return snapshots.Info{}, errdefs.ErrNotFound
		}
		return snapshots.Info{Name: key, Kind: snapshots.KindActive, Labels: maps.Clone(s.labels[key])}, nil
	}
	return snapshots.Info{Name: key, Kind: snapshots.KindCommitted}, nil
}
func (s *fixtureSnapshotter) Prepare(_ context.Context, key, _ string, opts ...snapshots.Opt) ([]mount.Mount, error) {
	info := snapshots.Info{}
	for _, opt := range opts {
		if err := opt(&info); err != nil {
			return nil, err
		}
	}
	if s.labels == nil {
		s.labels = make(map[string]map[string]string)
	}
	s.labels[key] = maps.Clone(info.Labels)
	s.prepared[key] = true
	return nil, nil
}
func (s *fixtureSnapshotter) Mounts(_ context.Context, key string) ([]mount.Mount, error) {
	if s.mountErr != nil {
		return nil, s.mountErr
	}
	if !s.prepared[key] {
		return nil, fmt.Errorf("image config requested rootfs before snapshot creation")
	}
	s.mountReads++
	return nil, nil
}
func (s *fixtureSnapshotter) Remove(_ context.Context, key string) error {
	if s.removeErr != nil {
		return s.removeErr
	}
	if !s.prepared[key] {
		return errdefs.ErrNotFound
	}
	delete(s.prepared, key)
	return nil
}

type fixtureContainerStore struct {
	containers.Store
	records map[string]containers.Container
}

func (s *fixtureContainerStore) Create(_ context.Context, record containers.Container) (containers.Container, error) {
	s.records[record.ID] = record
	return record, nil
}
func (s *fixtureContainerStore) Get(_ context.Context, id string) (containers.Container, error) {
	record, ok := s.records[id]
	if !ok {
		return containers.Container{}, errdefs.ErrNotFound
	}
	return record, nil
}

func (s *fixtureContainerStore) Delete(_ context.Context, id string) error {
	if _, ok := s.records[id]; !ok {
		return errdefs.ErrNotFound
	}
	delete(s.records, id)
	return nil
}

type fixtureTasks struct {
	tasks.TasksClient
	exists bool
	err    error
}

func (f fixtureTasks) Get(_ context.Context, request *tasks.GetRequest, _ ...grpc.CallOption) (*tasks.GetResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.exists {
		return &tasks.GetResponse{Process: &tasktypes.Process{ID: request.ContainerID}}, nil
	}
	return nil, errdefs.ErrNotFound
}

type fixtureConfigImage struct {
	containerd.Image
	descriptor imagev1.Descriptor
	store      content.Store
}

func (f fixtureConfigImage) Config(context.Context) (imagev1.Descriptor, error) {
	return f.descriptor, nil
}
func (f fixtureConfigImage) ContentStore() content.Store { return f.store }

func TestExplicitUserImageConfigurationFailsClosed(t *testing.T) {
	for _, test := range []struct{ name, os, media string }{
		{"unsupported OS", "windows", imagev1.MediaTypeImageConfig},
		{"missing OS", "", imagev1.MediaTypeImageConfig},
		{"unsupported media", "linux", "application/not-an-image-config"},
	} {
		t.Run(test.name, func(t *testing.T) {
			data, err := sonic.Marshal(imagev1.Image{Platform: imagev1.Platform{OS: test.os}, Config: imagev1.ImageConfig{Env: []string{"PATH=/image/path"}}})
			if err != nil {
				t.Fatal(err)
			}
			store, err := local.NewStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			descriptor := imagev1.Descriptor{MediaType: test.media, Digest: digest.FromBytes(data), Size: int64(len(data))}
			if err = content.WriteBlob(context.Background(), store, descriptor.Digest.String(), bytes.NewReader(data), descriptor); err != nil {
				t.Fatal(err)
			}
			if _, err = imageEnvironmentOptions(context.Background(), fixtureConfigImage{descriptor: descriptor, store: store}); err == nil {
				t.Fatal("unsupported image config accepted")
			}
		})
	}
}
