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
	"os"
	"slices"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/containerd/containerd"
	introspectionapi "github.com/containerd/containerd/api/services/introspection/v1"
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
	client, err := containerd.New("", containerd.WithServices(
		containerd.WithContentStore(store), containerd.WithImageStore(&fixtureImages{image: images.Image{Name: "fixture:local", Target: manifest}}),
		containerd.WithNamespaceService(fixtureNamespaces{}), containerd.WithSnapshotters(map[string]snapshots.Snapshotter{"fixture": snapshotter}),
		containerd.WithIntrospectionService(fixtureIntrospection{}), containerd.WithContainerStore(containerStore)))
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
	container, err := backend.create(ctx, "fixture-container", "fixture:local", options, map[string]string{"owner": "test"})
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
	// 仅 snapshot 存在但 metadata 不存在的恢复路径必须使用相同 snapshotter
	orphan := &containerdContainer{client: client, id: "orphan", snapshotter: "fixture"}
	snapshotter.prepared["orphan-snapshot"] = true
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
}

func (s *fixtureSnapshotter) Stat(_ context.Context, key string) (snapshots.Info, error) {
	return snapshots.Info{Name: key, Kind: snapshots.KindCommitted}, nil
}
func (s *fixtureSnapshotter) Prepare(_ context.Context, key, _ string, _ ...snapshots.Opt) ([]mount.Mount, error) {
	s.prepared[key] = true
	return nil, nil
}
func (s *fixtureSnapshotter) Mounts(_ context.Context, key string) ([]mount.Mount, error) {
	if !s.prepared[key] {
		return nil, fmt.Errorf("image config requested rootfs before snapshot creation")
	}
	s.mountReads++
	return nil, nil
}
func (s *fixtureSnapshotter) Remove(_ context.Context, key string) error {
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
