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

package git

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func Clone(req GitCloneRequest) error {
	args := []string{"clone", "--depth", "1"}
	if strings.TrimSpace(req.Branch) != "" {
		args = append(args, "--branch", req.Branch)
	}
	args = append(args, req.RepoURL, ".")
	return runGit(req.Workdir, req.Auth, args...)
}

func HeadSHA(req GitHeadSHARequest) (string, error) {
	out, err := runGitOutput(req.Workdir, GitAuth{}, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func Add(req GitAddRequest) error {
	return runGit(req.Workdir, GitAuth{}, "add", req.FilePath)
}

func Commit(req GitCommitRequest) error {
	args := []string{"commit", "-m", req.Message}
	if strings.TrimSpace(req.Author) != "" {
		args = append(args, fmt.Sprintf("--author=%s <noreply@arcentra.local>", req.Author))
	}
	return runGit(req.Workdir, GitAuth{}, args...)
}

func CheckoutNewBranch(req GitCheckoutBranchRequest) error {
	return runGit(req.Workdir, GitAuth{}, "checkout", "-b", req.Branch)
}

func Push(req GitPushRequest) error {
	return runGit(req.Workdir, req.Auth, "push", req.Remote, req.Branch)
}

func runGit(workdir string, auth GitAuth, args ...string) error {
	_, err := runGitOutput(workdir, auth, args...)
	return err
}

func runGitOutput(workdir string, auth GitAuth, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
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
		script := "#!/bin/sh\ncase \"$1\" in\n  *Username*) echo \"$GIT_USERNAME\" ;;\n  *Password*) echo \"$GIT_PASSWORD\" ;;\n  *) echo \"\" ;;\nesac\n"
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
	if err != nil {
		return "", fmt.Errorf("git %s failed: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}
