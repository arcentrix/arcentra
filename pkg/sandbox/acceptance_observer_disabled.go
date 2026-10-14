//go:build !sandbox_acceptance

package sandbox

import "github.com/containerd/containerd"

func acceptanceContainerdClientOptions() []containerd.ClientOpt { return nil }
