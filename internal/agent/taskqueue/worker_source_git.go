// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package taskqueue

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/arcentrix/arcentra/pkg/sourceprovenance"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
)

// Checkout runs before independent execution approval. Only system Git and its
// trusted system helpers may run; no ambient executable/configuration selection.
const sourceGitPath = "/usr/bin/git"
const sourceGitPATH = "/usr/bin:/bin"
const sourceGitOutputLimit = 4 << 20

// Inspect complete small pointer blobs, never prose mentioning the LFS format.
var sourceLFSPointer = regexp.MustCompile(`\Aversion https://git-lfs.github.com/spec/v1\n(?:ext-[0-9]+-[A-Za-z0-9.-]+ [^\r\n]+\n)*oid sha256:[a-f0-9]{64}\nsize (?:0|[1-9][0-9]*)\n?\z`)

type sourceGit struct {
	workspace, private, protocol string
	env, options                 []string
}
type sourceGitOutput struct {
	bytes.Buffer
	overflow bool
}

func (b *sourceGitOutput) Write(p []byte) (int, error) {
	n := len(p)
	if b.Len()+n > sourceGitOutputLimit {
		b.overflow = true
		return n, nil
	}
	_, _ = b.Buffer.Write(p)
	return n, nil
}

func sourceGitTransport(raw string, authenticated bool) (string, error) {
	return sourceprovenance.GitCheckoutTransport(raw, authenticated)
}

func newSourceGit(src *taskqueue.SourcePayload, workspace string) (*sourceGit, error) {
	username, password := src.Auth["username"], src.Auth["password"]
	if password == "" {
		password = src.Auth["token"]
		if password != "" && username == "" {
			username = "oauth2"
		}
	}
	protocol, err := sourceGitTransport(src.Repo, password != "" || username != "")
	if err != nil {
		return nil, err
	}
	if protocol == "git" && len(src.Auth) != 0 {
		return nil, fmt.Errorf("native git source must be anonymous")
	}
	binary, err := os.Stat(sourceGitPath)
	if err != nil || !binary.Mode().IsRegular() || binary.Mode().Perm()&0111 == 0 || binary.Mode().Perm()&0022 != 0 {
		return nil, fmt.Errorf("trusted system Git unavailable")
	}
	absolute, err := filepath.Abs(workspace)
	if err != nil {
		return nil, fmt.Errorf("source workspace unavailable")
	}
	if err = sourceNoSymlinkAncestors(absolute); err != nil {
		return nil, fmt.Errorf("source requires a clean workspace")
	}
	if err = os.MkdirAll(absolute, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(absolute)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("source requires a clean workspace")
	}
	entries, err := os.ReadDir(absolute)
	if err != nil || len(entries) != 0 {
		return nil, fmt.Errorf("source requires a clean workspace")
	}
	private, err := os.MkdirTemp("", "arcentra-source-git-")
	if err != nil {
		return nil, err
	}
	if protocol == "file" {
		username, password = "", ""
	}
	g := &sourceGit{workspace: absolute, private: private, protocol: protocol}
	ok := false
	defer func() {
		if !ok {
			g.close()
		}
	}()
	templates := filepath.Join(private, "templates")
	if err = os.Mkdir(templates, 0700); err != nil {
		return nil, err
	}
	askpass := filepath.Join(private, "askpass")
	script := "#!/bin/sh\ncase \"$1\" in *Username*) printf '%s\\n' \"$ARCENTRA_GIT_USERNAME\" ;; *Password*) printf '%s\\n' \"$ARCENTRA_GIT_PASSWORD\" ;; *) exit 1 ;; esac\n"
	if err = os.WriteFile(askpass, []byte(script), 0700); err != nil {
		return nil, err
	}
	g.env = []string{"PATH=" + sourceGitPATH, "HOME=" + private, "XDG_CONFIG_HOME=" + private, "TMPDIR=" + private, "LC_ALL=C", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_SYSTEM=" + os.DevNull, "GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=" + askpass, "GIT_PROTOCOL_FROM_USER=0", "GIT_NO_LAZY_FETCH=1", "GIT_NO_REPLACE_OBJECTS=1", "ARCENTRA_GIT_USERNAME=" + username, "ARCENTRA_GIT_PASSWORD=" + password}
	g.options = []string{"-c", "credential.helper=", "-c", "credential.useHttpPath=true", "-c", "http.followRedirects=false", "-c", "http.sslVerify=true", "-c", "http.proxy=", "-c", "http.extraHeader=", "-c", "protocol.allow=never", "-c", "protocol." + protocol + ".allow=always", "-c", "core.hooksPath=" + os.DevNull, "-c", "core.fsmonitor=false", "-c", "core.attributesFile=" + os.DevNull, "-c", "init.templateDir=" + templates, "-c", "submodule.recurse=false"}
	if protocol == "file" {
		if err := g.validateLocalSource(src.Repo); err != nil {
			return nil, err
		}
	}
	ok = true
	return g, nil
}
func (g *sourceGit) close() { _ = os.RemoveAll(g.private) }
func (g *sourceGit) run(ctx context.Context, args ...string) ([]byte, error) {
	if g.protocol == "file" && len(args) > 0 && (args[0] == "clone" || args[0] == "fetch") {
		// Override the upload-pack process too: local repositories may carry
		// config, but cannot select a pack-objects command or hook.
		helper := "/usr/bin/git -c uploadpack.packObjectsHook=/usr/bin/env -c core.hooksPath=/dev/null -c core.fsmonitor=false upload-pack"
		args = append([]string{args[0], "--upload-pack=" + helper}, args[1:]...)
	}
	cmd := exec.CommandContext(ctx, sourceGitPath, append(append([]string{}, g.options...), args...)...)
	configureSourceGitCancellation(cmd)
	cmd.Dir, cmd.Env = g.workspace, g.env
	cmd.WaitDelay = 2 * time.Second
	out := &sourceGitOutput{}
	cmd.Stdout = out
	err := cmd.Run()
	if out.overflow {
		return nil, fmt.Errorf("source metadata exceeds bound")
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("git source operation failed: %w", err)
	}
	return out.Bytes(), nil
}
func (g *sourceGit) validateTree(ctx context.Context, commit string) error {
	tree, err := g.run(ctx, "ls-tree", "-r", "-z", commit)
	if err != nil {
		return err
	}
	objects := make(map[string]string)
	for _, entry := range bytes.Split(tree, []byte{0}) {
		if len(entry) == 0 {
			continue
		}
		header, path, ok := bytes.Cut(entry, []byte{'\t'})
		if !ok {
			return fmt.Errorf("invalid source tree")
		}
		fields := strings.Fields(string(header))
		if len(fields) != 3 {
			return fmt.Errorf("invalid source tree")
		}
		objects[string(path)] = fields[2]
		if fields[0] == "160000" || filepath.Base(string(path)) == ".gitmodules" {
			return fmt.Errorf("source submodules unsupported")
		}
		if filepath.Base(string(path)) == ".lfsconfig" {
			return fmt.Errorf("source LFS unsupported")
		}
		if filepath.Base(string(path)) == ".gitattributes" {
			body, err := g.run(ctx, "cat-file", "blob", fields[2])
			if err != nil {
				return err
			}
			for _, line := range strings.Split(string(body), "\n") {
				if strings.HasPrefix(strings.TrimSpace(line), "#") {
					continue
				}
				for _, word := range strings.Fields(line) {
					if word == "filter" || strings.HasPrefix(word, "filter=") {
						return fmt.Errorf("source checkout filters unsupported")
					}
				}
			}
		}
	}
	candidates, err := g.run(ctx, "grep", "-I", "-l", "-z", "-F", "-e", "version https://git-lfs.github.com/spec/v1", commit, "--")
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 {
			return err
		}
		return nil
	}
	for _, candidate := range bytes.Split(candidates, []byte{0}) {
		if len(candidate) == 0 {
			continue
		}
		path, ok := strings.CutPrefix(string(candidate), commit+":")
		object, exists := objects[path]
		if !ok || !exists {
			return fmt.Errorf("invalid source pointer candidate")
		}
		sizeText, err := g.run(ctx, "cat-file", "-s", object)
		if err != nil {
			return err
		}
		size, err := strconv.ParseUint(strings.TrimSpace(string(sizeText)), 10, 64)
		if err != nil {
			return fmt.Errorf("invalid source object size")
		}
		if size > 1024 {
			continue
		}
		body, err := g.run(ctx, "cat-file", "blob", object)
		if err != nil {
			return err
		}
		if sourceLFSPointer.Match(body) {
			return fmt.Errorf("source LFS unsupported")
		}
	}
	return nil
}
func (g *sourceGit) resolveCommit(ctx context.Context) (string, error) {
	readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	output, err := g.run(readCtx, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return "", err
	}
	commit := strings.TrimSpace(string(output))
	if !sourceprovenance.ValidCommit(commit) {
		return "", fmt.Errorf("initial source commit is not a full Git object ID")
	}
	return commit, nil
}

// Local transport exists for operator-controlled repositories and synthetic CI.
// Reject indirection that could make upload-pack consult an alternate store or
// perform a lazy network fetch using source-controlled remote/helper settings.
func (g *sourceGit) validateLocalSource(source string) error {
	if err := sourceNoSymlinkAncestors(source); err != nil {
		return err
	}
	gitdir := filepath.Join(source, ".git")
	info, err := os.Lstat(gitdir)
	if os.IsNotExist(err) {
		gitdir = source
		info, err = os.Lstat(gitdir)
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("local source must be a direct repository")
	}
	for _, path := range []string{"config", "HEAD", "objects", "objects/info", "objects/pack"} {
		info, err := os.Lstat(filepath.Join(gitdir, path))
		if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("local source metadata unavailable")
		}
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("local source indirection unsupported")
		}
	}
	for _, path := range []string{"commondir", "config.worktree", "objects/info/alternates", "objects/info/http-alternates"} {
		if _, err := os.Lstat(filepath.Join(gitdir, path)); !os.IsNotExist(err) {
			return fmt.Errorf("local source alternates unsupported")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	body, err := g.run(ctx, "--git-dir="+gitdir, "config", "--local", "--no-includes", "--null", "--list")
	if err != nil {
		return err
	}
	for _, record := range bytes.Split(body, []byte{0}) {
		key, _, _ := bytes.Cut(record, []byte{'\n'})
		name := strings.ToLower(string(key))
		if strings.HasPrefix(name, "include.") || strings.HasPrefix(name, "includeif.") || name == "extensions.partialclone" || name == "extensions.worktreeconfig" || strings.HasPrefix(name, "remote.") && strings.HasSuffix(name, ".promisor") {
			return fmt.Errorf("local source indirect configuration unsupported")
		}
	}
	return nil
}

// The Agent-owned workspace ancestors must remain trusted for the operation.
// Reject pre-existing symlinks; this is not a sandbox against concurrent host
// administrators replacing directories while Git is running.
func sourceNoSymlinkAncestors(path string) error {
	for current := filepath.Clean(path); ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("source path unavailable")
		}
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("source path symlink unsupported")
		}
		if filepath.Dir(current) == current {
			return nil
		}
	}
}
