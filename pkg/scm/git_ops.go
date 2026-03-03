package scm

import (
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
func Clone(req GitCloneRequest) error {
	args := []string{"clone", "--depth", "1"}
	if strings.TrimSpace(req.Branch) != "" {
		args = append(args, "--branch", req.Branch)
	}
	args = append(args, req.RepoURL, ".")
	return runGit(req.Workdir, req.Auth, args...)
}

// HeadSHA returns current repository HEAD commit sha.
func HeadSHA(req GitHeadSHARequest) (string, error) {
	out, err := runGitOutput(req.Workdir, GitAuth{}, "rev-parse", "HEAD")
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
	args := []string{"commit", "-m", req.Message}
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
	if err != nil {
		return "", fmt.Errorf("git %s failed: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}
