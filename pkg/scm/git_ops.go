// Copyright 2026 Arcentra Authors.
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

package scm

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// NewGitAuthFromMap builds GitAuth from generic credential map.
func NewGitAuthFromMap(auth map[string]string) GitAuth {
	token := strings.TrimSpace(auth["token"])
	if token == "" {
		token = strings.TrimSpace(auth["password"])
	}
	return GitAuth{
		Username: strings.TrimSpace(auth["username"]),
		Token:    token,
		Password: strings.TrimSpace(auth["password"]),
		SSHKey:   strings.TrimSpace(auth["ssh_key"]),
	}
}

// Clone clones repository into workdir and optionally checks out branch.
func Clone(req GitCloneRequest) error { return CloneContext(context.Background(), req) }

// CloneContext 在可取消的上下文中克隆代码仓库
func CloneContext(ctx context.Context, req GitCloneRequest) error {
	args := []string{"clone", "--depth", "1"}
	if strings.TrimSpace(req.Branch) != "" {
		args = append(args, "--branch", req.Branch)
	}
	args = append(args, "--", req.RepoURL, ".")
	_, err := runGitOutputContext(ctx, req.Workdir, req.Auth, args...)
	return err
}

// HeadSHA returns current repository HEAD commit sha.
func HeadSHA(req GitHeadSHARequest) (string, error) { return HeadSHAContext(context.Background(), req) }

// HeadSHAContext 在可取消的上下文中读取当前提交
func HeadSHAContext(ctx context.Context, req GitHeadSHARequest) (string, error) {
	out, err := runGitOutputContext(ctx, req.Workdir, GitAuth{}, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// Add stages file path into git index.
func Add(req GitAddRequest) error {
	return runGit(req.Workdir, GitAuth{}, "add", req.FilePath)
}

// Commit creates a commit in current workdir.
func Commit(req GitCommitRequest) error {
	// A service checkout must not depend on an operator HOME/global Git identity.
	// These process-local defaults do not write repository or host configuration.
	args := []string{"-c", "user.name=Arcentra", "-c", "user.email=noreply@arcentra.local", "commit", "-m", req.Message}
	if strings.TrimSpace(req.Author) != "" {
		args = append(args, fmt.Sprintf("--author=%s <noreply@arcentra.local>", req.Author))
	}
	return runGit(req.Workdir, GitAuth{}, args...)
}

// CheckoutNewBranch creates and switches to a new branch.
func CheckoutNewBranch(req GitCheckoutBranchRequest) error {
	return runGit(req.Workdir, GitAuth{}, "checkout", "-b", req.Branch)
}

// Push pushes branch to remote with provided credentials.
func Push(req GitPushRequest) error {
	return runGit(req.Workdir, req.Auth, "push", req.Remote, req.Branch)
}

func runGit(workdir string, auth GitAuth, args ...string) error {
	_, err := runGitOutput(workdir, auth, args...)
	return err
}

func runGitOutput(workdir string, auth GitAuth, args ...string) (string, error) {
	return runGitOutputContext(context.Background(), workdir, auth, args...)
}

func runGitOutputContext(ctx context.Context, workdir string, auth GitAuth, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = workdir
	cmd.Env = os.Environ()
	cmd.Env = append(cmd.Env, "GIT_TERMINAL_PROMPT=0")

	tmpFiles := []string{}
	cleanup := func() {
		for _, one := range tmpFiles {
			_ = os.Remove(one)
		}
	}
	defer cleanup()

	if auth.Token != "" {
		username := auth.Username
		if username == "" {
			username = "oauth2"
		}
		askPass, err := os.CreateTemp("", "arcentra-git-askpass-*")
		if err != nil {
			return "", err
		}
		askPassPath := askPass.Name()
		_ = askPass.Close()
		tmpFiles = append(tmpFiles, askPassPath)
		script := strings.Join([]string{
			"#!/bin/sh",
			"case \"$1\" in",
			"  *Username*) echo \"$GIT_USERNAME\" ;;",
			"  *Password*) echo \"$GIT_PASSWORD\" ;;",
			"  *) echo \"\" ;;",
			"esac",
			"",
		}, "\n")
		if err := os.WriteFile(askPassPath, []byte(script), 0o700); err != nil {
			return "", err
		}
		cmd.Env = append(cmd.Env, "GIT_ASKPASS="+askPassPath, "GIT_USERNAME="+username, "GIT_PASSWORD="+auth.Token)
	}
	if auth.SSHKey != "" {
		keyFile, err := os.CreateTemp("", "arcentra-git-sshkey-*")
		if err != nil {
			return "", err
		}
		keyPath := keyFile.Name()
		_ = keyFile.Close()
		tmpFiles = append(tmpFiles, keyPath)
		if err := os.WriteFile(keyPath, []byte(auth.SSHKey), 0o600); err != nil {
			return "", err
		}
		cmd.Env = append(cmd.Env, fmt.Sprintf("GIT_SSH_COMMAND=ssh -i %s -o IdentitiesOnly=yes -o StrictHostKeyChecking=no", keyPath))
	}

	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err != nil {
		return "", fmt.Errorf("git %s failed: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}
