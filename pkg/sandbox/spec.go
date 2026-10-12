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
	"math"
	"math/big"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/containerd/containerd/containers"
	"github.com/containerd/containerd/oci"
	"github.com/opencontainers/runtime-spec/specs-go"
)

var cpuPattern = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+)?m?$`)
var memoryPattern = regexp.MustCompile(`(?i)^([0-9]+)([KMGT](?:I?B?)?|B)?$`)

func parseCPU(value string) (int64, uint64, error) {
	value = strings.TrimSpace(value)
	if !cpuPattern.MatchString(value) {
		return 0, 0, fmt.Errorf("invalid CPU limit %q", value)
	}
	millicores := strings.HasSuffix(value, "m")
	cores, ok := new(big.Rat).SetString(strings.TrimSuffix(value, "m"))
	if !ok {
		return 0, 0, fmt.Errorf("invalid CPU limit %q", value)
	}
	if millicores {
		cores.Quo(cores, big.NewRat(1000, 1))
	}
	quota := new(big.Rat).Mul(cores, big.NewRat(100000, 1))
	if !quota.IsInt() || !quota.Num().IsInt64() || quota.Num().Int64() < 1000 {
		return 0, 0, fmt.Errorf("CPU limit %q must produce an integer quota of at least 1000 microseconds", value)
	}
	return quota.Num().Int64(), 100000, nil
}

func parseMemory(value string) (int64, error) {
	value = strings.TrimSpace(value)
	match := memoryPattern.FindStringSubmatch(value)
	if match == nil {
		return 0, fmt.Errorf("invalid memory limit %q", value)
	}
	size, err := strconv.ParseUint(match[1], 10, 63)
	if err != nil || size == 0 {
		return 0, fmt.Errorf("invalid memory limit %q", value)
	}
	multiplier := uint64(1)
	if match[2] != "" {
		switch strings.ToUpper(match[2][:1]) {
		case "K":
			multiplier = 1 << 10
		case "M":
			multiplier = 1 << 20
		case "G":
			multiplier = 1 << 30
		case "T":
			multiplier = 1 << 40
		}
	}
	if size > math.MaxInt64/multiplier {
		return 0, fmt.Errorf("memory limit %q overflows int64", value)
	}
	return int64(size * multiplier), nil
}

func resourceSpec(resources *Resources) (*specs.LinuxResources, error) {
	result := &specs.LinuxResources{}
	if resources == nil {
		return result, nil
	}
	if resources.CPU != "" {
		quota, period, err := parseCPU(resources.CPU)
		if err != nil {
			return nil, err
		}
		result.CPU = &specs.LinuxCPU{Quota: &quota, Period: &period}
	}
	if resources.CPUShares != 0 {
		if resources.CPUShares < 2 || resources.CPUShares > 262144 {
			return nil, fmt.Errorf("CPU shares must be between 2 and 262144")
		}
		if result.CPU == nil {
			result.CPU = &specs.LinuxCPU{}
		}
		shares := uint64(resources.CPUShares)
		result.CPU.Shares = &shares
	}
	if resources.Memory != "" {
		limit, err := parseMemory(resources.Memory)
		if err != nil {
			return nil, err
		}
		result.Memory = &specs.LinuxMemory{Limit: &limit}
	}
	if resources.MemoryReservation != "" {
		reservation, err := parseMemory(resources.MemoryReservation)
		if err != nil {
			return nil, err
		}
		if result.Memory == nil {
			result.Memory = &specs.LinuxMemory{}
		}
		if result.Memory.Limit != nil && reservation > *result.Memory.Limit {
			return nil, fmt.Errorf("memory reservation exceeds memory limit")
		}
		result.Memory.Reservation = &reservation
	}
	return result, nil
}

func validateNetwork(mode string) error {
	if mode != "" && mode != "none" {
		return fmt.Errorf("network mode %q is unsupported; only none is implemented", mode)
	}
	return nil
}

func validAbsolutePath(value string) bool {
	return path.IsAbs(value) && path.Clean(value) == value && !strings.ContainsRune(value, '\x00')
}

func validateEnv(env map[string]string) error {
	for key, value := range env {
		if key == "" || strings.ContainsAny(key, "=\x00") || strings.ContainsRune(value, '\x00') {
			return fmt.Errorf("invalid environment variable name or value")
		}
	}
	return nil
}

func envList(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	result := make([]string, 0, len(env))
	for _, key := range keys {
		result = append(result, key+"="+env[key])
	}
	return result
}

func createSpecOptions(opts *CreateOptions, resources *Resources) ([]oci.SpecOpts, error) {
	if len(opts.Command) == 0 || strings.TrimSpace(opts.Command[0]) == "" {
		return nil, fmt.Errorf("explicit container init command is required")
	}
	for _, arg := range append(slices.Clone(opts.Command), opts.Args...) {
		if strings.ContainsRune(arg, '\x00') {
			return nil, fmt.Errorf("command contains NUL")
		}
	}
	if opts.Privileged {
		return nil, fmt.Errorf("privileged containers are unsupported")
	}
	if err := validateEnv(opts.Env); err != nil {
		return nil, err
	}
	if opts.WorkingDir != "" && !validAbsolutePath(opts.WorkingDir) {
		return nil, fmt.Errorf("working directory must be a clean absolute container path")
	}
	limits, err := resourceSpec(resources)
	if err != nil {
		return nil, err
	}
	options := []oci.SpecOpts{
		oci.WithProcessArgs(append(slices.Clone(opts.Command), opts.Args...)...),
		oci.WithEnv(envList(opts.Env)), oci.WithNoNewPrivileges,
		oci.WithLinuxNamespace(specs.LinuxNamespace{Type: specs.NetworkNamespace}),
		func(_ context.Context, _ oci.Client, _ *containers.Container, spec *specs.Spec) error {
			if spec.Linux == nil {
				spec.Linux = &specs.Linux{}
			}
			if spec.Linux.Resources == nil {
				spec.Linux.Resources = &specs.LinuxResources{}
			}
			// 保留 OCI 默认的设备限制
			spec.Linux.Resources.CPU, spec.Linux.Resources.Memory = limits.CPU, limits.Memory
			return nil
		},
	}
	if opts.WorkingDir != "" {
		options = append(options, oci.WithProcessCwd(opts.WorkingDir))
	}
	if opts.Hostname != "" {
		options = append(options, oci.WithHostname(opts.Hostname))
	}
	mounts := make([]specs.Mount, 0, len(opts.Mounts))
	for _, mount := range opts.Mounts {
		if mount.Type != "" && mount.Type != "bind" {
			return nil, fmt.Errorf("mount type %q is unsupported; only bind is implemented", mount.Type)
		}
		if !validAbsolutePath(mount.Source) || mount.Source == "/" || !validAbsolutePath(mount.Target) || mount.Target == "/" {
			return nil, fmt.Errorf("bind source and target must be clean absolute paths other than /")
		}
		for _, reserved := range []string{"/proc", "/sys", "/dev"} {
			if mount.Target == reserved || strings.HasPrefix(mount.Target, reserved+"/") {
				return nil, fmt.Errorf("mount target %q is reserved", mount.Target)
			}
		}
		mode := "rw"
		if mount.ReadOnly {
			mode = "ro"
		}
		mounts = append(mounts, specs.Mount{Source: mount.Source, Destination: mount.Target, Type: "bind", Options: []string{"rbind", "rprivate", mode}})
	}
	if len(mounts) > 0 {
		options = append(options, oci.WithMounts(mounts))
	}
	return options, nil
}

func executionSpec(base *specs.Spec, cmd []string, opts *ExecuteOptions) (*specs.Process, error) {
	if base == nil || base.Process == nil {
		return nil, fmt.Errorf("container process spec is unavailable")
	}
	if len(cmd) == 0 || strings.TrimSpace(cmd[0]) == "" {
		return nil, fmt.Errorf("command is required")
	}
	for _, arg := range cmd {
		if strings.ContainsRune(arg, '\x00') {
			return nil, fmt.Errorf("command contains NUL")
		}
	}
	process := *base.Process
	process.Args, process.Env = slices.Clone(cmd), slices.Clone(base.Process.Env)
	process.Terminal = false
	if opts == nil {
		return &process, nil
	}
	if opts.TTY {
		return nil, fmt.Errorf("TTY execution is unsupported")
	}
	if opts.Timeout < 0 {
		return nil, fmt.Errorf("execution timeout cannot be negative")
	}
	if err := validateEnv(opts.Env); err != nil {
		return nil, err
	}
	if opts.WorkingDir != "" {
		if !validAbsolutePath(opts.WorkingDir) {
			return nil, fmt.Errorf("working directory must be a clean absolute container path")
		}
		process.Cwd = opts.WorkingDir
	}
	env := make(map[string]string, len(process.Env)+len(opts.Env))
	for _, entry := range process.Env {
		if key, value, ok := strings.Cut(entry, "="); ok {
			env[key] = value
		}
	}
	for key, value := range opts.Env {
		env[key] = value
	}
	process.Env = envList(env)
	if opts.User != "" {
		parts := strings.Split(opts.User, ":")
		if len(parts) > 2 {
			return nil, fmt.Errorf("user must be numeric UID or UID:GID")
		}
		uid, err := strconv.ParseUint(parts[0], 10, 32)
		if err != nil {
			return nil, fmt.Errorf("user must be numeric UID or UID:GID")
		}
		gid := uid
		if len(parts) == 2 {
			gid, err = strconv.ParseUint(parts[1], 10, 32)
			if err != nil {
				return nil, fmt.Errorf("user must be numeric UID or UID:GID")
			}
		}
		process.User = specs.User{UID: uint32(uid), GID: uint32(gid)}
		process.Capabilities = &specs.LinuxCapabilities{}
	}
	return &process, nil
}
