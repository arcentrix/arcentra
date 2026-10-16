// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package scm

import (
	"bytes"
	"context"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	definitionMaxBytes    = 1 << 20
	definitionReadTimeout = 60 * time.Second
)

// GitDefinitionRequest 指定可信 HTTPS 仓库中的分支或完整提交及定义路径
// CommitSHA 非空时只获取该提交，不回退到 Branch 的最新版本
type GitDefinitionRequest struct {
	RepoURL   string
	Branch    string
	CommitSHA string
	FilePath  string
	Auth      GitAuth
}

type definitionGitExecutor func(context.Context, string, []string, ...string) ([]byte, error)

// ReadHTTPSDefinitionContext 在隔离的临时裸仓库中读取有大小限制的普通定义文件并返回实际提交
// 不执行工作树检出、过滤器、钩子、SSH 或重定向，不把凭证放入 URL、参数或错误
func ReadHTTPSDefinitionContext(ctx context.Context, req GitDefinitionRequest) (string, string, error) {
	return readHTTPSDefinitionContext(ctx, req, executeDefinitionGit)
}

func readHTTPSDefinitionContext(ctx context.Context, req GitDefinitionRequest, execute definitionGitExecutor) (string, string, error) {
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	if err := validateDefinitionRequest(req); err != nil {
		return "", "", err
	}
	ctx, cancel := context.WithTimeout(ctx, definitionReadTimeout)
	defer cancel()
	dir, err := os.MkdirTemp("", "arcentra-definition-*")
	if err != nil {
		return "", "", errors.New("cannot create private definition workspace")
	}
	defer func() { _ = os.RemoveAll(dir) }()
	env, err := definitionGitEnvironment(dir, req.Auth)
	if err != nil {
		return "", "", err
	}
	run := func(args ...string) ([]byte, error) {
		out, runErr := execute(ctx, dir, env, args...)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if runErr != nil {
			// Neither remote output nor exec errors may contain credentials or URLs in public errors.
			return nil, errors.New("definition git operation failed")
		}
		return out, nil
	}
	if _, err = run("init", "--bare", "--template=", "--initial-branch=definition", "."); err != nil {
		return "", "", err
	}
	ref := req.CommitSHA
	if ref == "" {
		ref = "refs/heads/" + req.Branch
		if _, err = run("check-ref-format", ref); err != nil {
			if ctx.Err() != nil {
				return "", "", ctx.Err()
			}
			return "", "", errors.New("invalid definition branch")
		}
	}
	if _, err = run("fetch", "--depth=1", "--no-tags", "--no-recurse-submodules", "--", req.RepoURL, ref); err != nil {
		return "", "", err
	}
	actual, err := run("rev-parse", "--verify", "FETCH_HEAD^{commit}")
	if err != nil {
		return "", "", err
	}
	sha := strings.TrimSpace(string(actual))
	if !fullDefinitionSHA(sha) || (req.CommitSHA != "" && sha != req.CommitSHA) {
		return "", "", errors.New("definition commit does not match requested revision")
	}
	if _, err = run("update-ref", "HEAD", sha); err != nil {
		return "", "", err
	}
	head, err := run("rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return "", "", err
	}
	if strings.TrimSpace(string(head)) != sha {
		return "", "", errors.New("definition HEAD verification failed")
	}
	blob, err := definitionBlob(run, sha, req.FilePath)
	if err != nil {
		return "", "", err
	}
	sizeOutput, err := run("cat-file", "-s", blob)
	if err != nil {
		return "", "", err
	}
	size, err := strconv.ParseInt(strings.TrimSpace(string(sizeOutput)), 10, 64)
	if err != nil || size < 0 || size > definitionMaxBytes {
		return "", "", errors.New("pipeline definition exceeds size limit")
	}
	content, err := run("cat-file", "blob", blob)
	if err != nil {
		return "", "", err
	}
	if len(content) > definitionMaxBytes || int64(len(content)) != size {
		return "", "", errors.New("pipeline definition size verification failed")
	}
	return string(content), sha, nil
}

func validateDefinitionRequest(req GitDefinitionRequest) error {
	if err := validateDefinitionRepositoryURL(req.RepoURL); err != nil {
		return err
	}
	if req.CommitSHA != "" && !fullDefinitionSHA(req.CommitSHA) {
		return errors.New("definition revision must be a full lowercase SHA-1 commit")
	}
	if req.CommitSHA == "" && (req.Branch == "" || len(req.Branch) > 1024 || strings.HasPrefix(req.Branch, "-") ||
		strings.HasPrefix(req.Branch, "refs/") || strings.ContainsAny(req.Branch, "\x00\r\n")) {
		return errors.New("invalid definition branch")
	}
	if err := validateDefinitionFilePath(req.FilePath); err != nil {
		return err
	}
	if req.Auth.SSHKey != "" {
		return errors.New("SSH credentials are unsupported for HTTPS definition loading")
	}
	for _, value := range []string{req.Auth.Username, req.Auth.Token, req.Auth.Password} {
		if len(value) > 65536 || strings.ContainsAny(value, "\x00\r\n") {
			return errors.New("invalid definition authentication")
		}
	}
	return nil
}

func validateDefinitionRepositoryURL(repository string) error {
	u, err := url.Parse(repository)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Opaque != "" ||
		u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" || u.RawPath != "" ||
		strings.ContainsAny(repository, "\x00\r\n\t ") || strings.ContainsAny(u.Path, "%\\") {
		return errors.New("definition repository must be a credential-free HTTPS URL")
	}
	parts := strings.Split(strings.TrimSuffix(strings.TrimPrefix(u.Path, "/"), "/"), "/")
	if len(parts) < 2 || len(repository) > 4096 {
		return errors.New("definition repository requires its full namespace and name")
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return errors.New("invalid definition repository path")
		}
	}
	return nil
}

func validateDefinitionFilePath(filePath string) error {
	if filePath == "" || len(filePath) > 1024 || strings.ContainsAny(filePath, "\\\x00\r\n\t") {
		return errors.New("invalid pipeline definition path")
	}
	for _, part := range strings.Split(filePath, "/") {
		if part == "" || part == "." || part == ".." || strings.EqualFold(part, ".git") {
			return errors.New("pipeline definition must be a safe relative repository path")
		}
	}
	return nil
}

func fullDefinitionSHA(value string) bool {
	if len(value) != 40 {
		return false
	}
	for _, c := range value {
		if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func definitionBlob(run func(...string) ([]byte, error), commit, filePath string) (string, error) {
	object := commit
	parts := strings.Split(filePath, "/")
	for i, part := range parts {
		out, err := run("ls-tree", "-z", object, "--", part)
		if err != nil {
			return "", err
		}
		entry := strings.TrimSuffix(string(out), "\x00")
		meta, name, ok := strings.Cut(entry, "\t")
		fields := strings.Fields(meta)
		if !ok || name != part || len(fields) != 3 || !fullDefinitionSHA(fields[2]) {
			return "", errors.New("pipeline definition is absent or not a regular repository file")
		}
		if i < len(parts)-1 {
			if fields[0] != "040000" || fields[1] != "tree" {
				return "", errors.New("pipeline definition parent must be a repository tree")
			}
		} else if (fields[0] != "100644" && fields[0] != "100755") || fields[1] != "blob" {
			return "", errors.New("pipeline definition must be a regular repository file")
		}
		object = fields[2]
	}
	return object, nil
}

func definitionGitEnvironment(dir string, auth GitAuth) ([]string, error) {
	env := make([]string, 0, len(os.Environ())+10)
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		upper := strings.ToUpper(key)
		if strings.HasPrefix(upper, "GIT_") || strings.HasPrefix(upper, "SSH_") ||
			strings.HasPrefix(upper, "LD_") || strings.HasPrefix(upper, "DYLD_") ||
			strings.HasPrefix(upper, "CURL_") || strings.HasPrefix(upper, "SSL_") ||
			upper == "HOME" || upper == "XDG_CONFIG_HOME" || upper == "ENV" || upper == "BASH_ENV" {
			continue
		}
		env = append(env, item)
	}
	askpass := filepath.Join(dir, "askpass")
	script := "#!/bin/sh\ncase \"$1\" in\n*Username*) printf '%s\\n' \"$GIT_USERNAME\" ;;\n*Password*) printf '%s\\n' \"$GIT_PASSWORD\" ;;\n*) exit 1 ;;\nesac\n"
	if err := os.WriteFile(askpass, []byte(script), 0o700); err != nil {
		return nil, errors.New("cannot prepare definition authentication")
	}
	username := auth.Username
	if username == "" {
		username = "oauth2"
	}
	token := auth.Token
	if token == "" {
		token = auth.Password
	}
	return append(env, "HOME="+dir, "XDG_CONFIG_HOME="+dir,
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_SYSTEM="+os.DevNull, "GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS="+askpass, "GIT_USERNAME="+username, "GIT_PASSWORD="+token,
		"GIT_LITERAL_PATHSPECS=1", "GIT_PROTOCOL_FROM_USER=0", "LC_ALL=C"), nil
}

func executeDefinitionGit(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
	secure := []string{
		"-c", "credential.helper=", "-c", "credential.useHttpPath=true",
		"-c", "http.followRedirects=false", "-c", "http.sslVerify=true", "-c", "http.proxy=", "-c", "http.extraHeader=",
		"-c", "protocol.allow=never", "-c", "protocol.https.allow=always", "-c", "core.hooksPath=" + os.DevNull,
		"-c", "core.fsmonitor=false", "-c", "core.attributesFile=" + os.DevNull,
	}
	cmd := exec.CommandContext(ctx, "git", append(secure, args...)...)
	cmd.Dir, cmd.Env = dir, env
	cmd.WaitDelay = time.Second
	configureDefinitionCancellation(cmd)
	stdout, stderr := &definitionOutput{limit: definitionMaxBytes}, &definitionOutput{limit: 64 << 10}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil || stdout.exceeded || stderr.exceeded {
		return nil, errors.New("definition git operation failed")
	}
	return stdout.Bytes(), nil
}

type definitionOutput struct {
	buffer   bytes.Buffer
	limit    int
	exceeded bool
}

func (out *definitionOutput) Len() int { return out.buffer.Len() }

func (out *definitionOutput) Bytes() []byte { return out.buffer.Bytes() }

func (out *definitionOutput) Write(p []byte) (int, error) {
	remaining := out.limit - out.Len()
	if len(p) > remaining {
		out.exceeded = true
		_, _ = out.buffer.Write(p[:remaining])
		return len(p), nil
	}
	return out.buffer.Write(p)
}
