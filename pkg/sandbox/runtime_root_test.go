// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package sandbox

import (
	"context"
	"testing"

	runcoptions "github.com/containerd/containerd/api/types/runc/options"
	"github.com/containerd/containerd/containers"
	"google.golang.org/protobuf/proto"
)

func TestRuntimeRootValidationAndExactOption(t *testing.T) {
	for _, root := range []string{"relative", "/", "/tmp/../run", "/tmp/a/", "/tmp/a\n"} {
		if _, err := normalizeConfig(&ContainerdConfig{RuntimeRoot: root}); err == nil {
			t.Fatalf("unsafe runtime root accepted: %q", root)
		}
	}
	const root = "/tmp/isolated-acceptance/runc"
	cfg, err := normalizeConfig(&ContainerdConfig{RuntimeRoot: root})
	if err != nil || cfg.RuntimeRoot != root {
		t.Fatalf("explicit root lost: %+v %v", cfg, err)
	}
	b := &containerdBackend{runtimeRoot: root}
	var c containers.Container
	if err := b.runtimeOption()(context.Background(), nil, &c); err != nil {
		t.Fatal(err)
	}
	var options runcoptions.Options
	if c.Runtime.Name != "io.containerd.runc.v2" || c.Runtime.Options == nil {
		t.Fatalf("wrong runtime: %+v", c.Runtime)
	}
	if err := proto.Unmarshal(c.Runtime.Options.GetValue(), &options); err != nil {
		t.Fatal(err)
	}
	if options.Root != root || options.BinaryName != "" || options.SystemdCgroup {
		t.Fatalf("unexpected runtime options: %+v", &options)
	}
	old := c.Runtime
	if err := (&containerdBackend{}).runtimeOption()(context.Background(), nil, &c); err != nil || c.Runtime.Name != old.Name || c.Runtime.Options != old.Options {
		t.Fatal("empty option changed existing runtime defaults")
	}
}
