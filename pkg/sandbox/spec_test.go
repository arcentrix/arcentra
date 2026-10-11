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
	"slices"
	"testing"

	"github.com/containerd/containerd/containers"
	"github.com/containerd/containerd/namespaces"
	"github.com/containerd/containerd/oci"
	"github.com/opencontainers/runtime-spec/specs-go"
)

func TestStrictResourceParsing(t *testing.T) {
	for _, test := range []struct {
		input string
		quota int64
	}{{"1", 100000}, {"0.5", 50000}, {"1000m", 100000}, {"500m", 50000}, {"0.01", 1000}} {
		quota, period, err := parseCPU(test.input)
		if err != nil || quota != test.quota || period != 100000 {
			t.Fatalf("CPU %q: %d/%d %v", test.input, quota, period, err)
		}
	}
	for _, value := range []string{"", "0", "-1", "NaN", "Inf", "1e3", "1000junk", "0.001", "0.010001", "9223372036854775807"} {
		if _, _, err := parseCPU(value); err == nil {
			t.Errorf("invalid CPU accepted: %q", value)
		}
	}
	for _, test := range []struct {
		input string
		bytes int64
	}{{"1G", 1 << 30}, {"512M", 512 << 20}, {"1024K", 1 << 20}, {"1024", 1024}, {"1GiB", 1 << 30}, {"1Gi", 1 << 30}, {"512Mi", 512 << 20}, {"1024m", 1 << 30}} {
		value, err := parseMemory(test.input)
		if err != nil || value != test.bytes {
			t.Fatalf("memory %q: %d %v", test.input, value, err)
		}
	}
	for _, value := range []string{"", "0", "-1", "1.5G", "512Mgarbage", "9223372036854775807T", "1KiBB"} {
		if _, err := parseMemory(value); err == nil {
			t.Errorf("invalid memory accepted: %q", value)
		}
	}
	for _, resources := range []*Resources{{CPU: "junk"}, {Memory: "junk"}, {CPUShares: -1}, {CPUShares: 1}, {CPUShares: 262145}, {Memory: "1M", MemoryReservation: "2M"}} {
		if _, err := resourceSpec(resources); err == nil {
			t.Errorf("invalid resources accepted: %+v", resources)
		}
	}
}

func TestCreateSpecNetworkMountsAndLimits(t *testing.T) {
	options, err := createSpecOptions(&CreateOptions{Command: []string{"sleep"}, Args: []string{"3600"}, Env: map[string]string{"CI": "1"}, WorkingDir: "/workspace", Hostname: "job", Mounts: []Mount{{Source: "/tmp/workspace", Target: "/workspace", ReadOnly: true}}}, &Resources{CPU: "500m", Memory: "64Mi", CPUShares: 512, MemoryReservation: "32Mi"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := namespaces.WithNamespace(context.Background(), "arcentra-test")
	spec, err := oci.GenerateSpec(ctx, nil, &containers.Container{ID: "unit-test"}, options...)
	if err != nil {
		t.Fatal(err)
	}
	networks := 0
	for _, ns := range spec.Linux.Namespaces {
		if ns.Type == specs.NetworkNamespace {
			networks++
			if ns.Path != "" {
				t.Fatal("network namespace attaches to host")
			}
		}
	}
	if networks != 1 {
		t.Fatalf("expected exactly one private network namespace, got %d", networks)
	}
	if !spec.Process.NoNewPrivileges || spec.Process.Cwd != "/workspace" || !slices.Equal(spec.Process.Args, []string{"sleep", "3600"}) {
		t.Fatalf("incorrect process spec: %+v", spec.Process)
	}
	resources := spec.Linux.Resources
	if *resources.CPU.Quota != 50000 || *resources.CPU.Period != 100000 || *resources.CPU.Shares != 512 || *resources.Memory.Limit != 64<<20 || *resources.Memory.Reservation != 32<<20 || len(resources.Devices) == 0 || resources.Devices[0].Allow {
		t.Fatalf("incorrect resource or device limits: %+v", resources)
	}
	mount := spec.Mounts[len(spec.Mounts)-1]
	if mount.Destination != "/workspace" || !slices.Equal(mount.Options, []string{"rbind", "rprivate", "ro"}) {
		t.Fatalf("incorrect mount: %+v", mount)
	}
}

func TestUnsupportedCreateSettingsFailBeforeRuntime(t *testing.T) {
	for _, opts := range []*CreateOptions{
		{}, {Command: []string{"true"}, Privileged: true}, {Command: []string{"true"}, WorkingDir: "relative"},
		{Command: []string{"true"}, Env: map[string]string{"BAD=KEY": "value"}}, {Command: []string{"true"}, NetworkMode: "bridge"}, {Command: []string{"true"}, NetworkMode: "host"},
		{Command: []string{"true"}, Mounts: []Mount{{Source: "/tmp", Target: "/tmp", Type: "volume"}}},
		{Command: []string{"true"}, Mounts: []Mount{{Source: "/", Target: "/host"}}},
		{Command: []string{"true"}, Mounts: []Mount{{Source: "/tmp", Target: "/proc/override"}}},
		{Command: []string{"true"}, Resources: &Resources{Memory: "typo"}},
	} {
		cfg, _ := normalizeConfig(&ContainerdConfig{Namespace: "arcentra-test", DefaultImage: "test:local"})
		backend := &fakeBackend{containers: make(map[string]*fakeContainer)}
		sb := newSandbox(cfg, backend)
		if _, err := sb.Create(context.Background(), opts); err == nil {
			t.Errorf("unsupported options accepted: %+v", opts)
		}
		if len(backend.containers) != 0 {
			t.Fatal("runtime touched before options validated")
		}
	}
	for _, cfg := range []*ContainerdConfig{nil, {NetworkMode: "bridge"}, {NetworkMode: "host"}, {Resources: &Resources{CPU: "invalid"}}} {
		if _, err := normalizeConfig(cfg); err == nil {
			t.Errorf("invalid config accepted: %+v", cfg)
		}
	}
}

func TestExecutionSpecRejectsUnsupportedSettings(t *testing.T) {
	base := &specs.Spec{Process: &specs.Process{Args: []string{"init"}, Env: []string{"BASE=yes"}, User: specs.User{UID: 0, GID: 0, AdditionalGids: []uint32{0, 10}}}}
	for _, opts := range []*ExecuteOptions{{User: "root"}, {User: "1:"}, {User: "1:2:3"}, {User: "-1"}, {User: "4294967296"}, {TTY: true}, {Timeout: -1}, {WorkingDir: "../workspace"}, {Env: map[string]string{"": "value"}}} {
		if _, err := executionSpec(base, []string{"true"}, opts); err == nil {
			t.Errorf("unsupported execution options accepted: %+v", opts)
		}
	}
	if _, err := executionSpec(base, nil, nil); err == nil {
		t.Fatal("empty command accepted")
	}
	process, err := executionSpec(base, []string{"true"}, &ExecuteOptions{User: "1000"})
	if err != nil {
		t.Fatal(err)
	}
	if process.User.UID != 1000 || process.User.GID != 1000 || len(process.User.AdditionalGids) != 0 {
		t.Fatalf("incorrect numeric user: %+v", process.User)
	}
	if len(base.Process.User.AdditionalGids) != 2 {
		t.Fatal("base user mutated")
	}
}
