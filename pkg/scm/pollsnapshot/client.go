// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package pollsnapshot

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/arcentrix/arcentra/pkg/scm"
)

type Reader struct {
	base     *url.URL
	token    string
	client   *http.Client
	limits   Limits
	provider string
}

// NewReader only accepts explicit GitHub.com or a configured HTTPS Gitea API.
// Custom RoundTrippers are a trusted testing seam, never supplied from user input.
func NewReader(cfg scm.ProviderConfig, client *http.Client, limits Limits) (*Reader, error) {
	provider := string(cfg.Kind)
	if provider != "github" && provider != "gitea" || len(cfg.Token) > 8192 || strings.ContainsAny(cfg.Token, "\r\n\x00") {
		return nil, failure(ReasonConfiguration)
	}
	raw := strings.TrimRight(cfg.BaseURL, "/")
	base, err := url.Parse(raw)
	if err != nil || raw == "" || raw != strings.TrimSpace(raw) || base.Scheme != "https" || base.Hostname() == "" || base.User != nil || base.RawQuery != "" || base.ForceQuery || base.Fragment != "" || base.RawPath != "" || strings.ContainsAny(raw, "\\%\r\n\x00") || base.Path != "" {
		return nil, failure(ReasonConfiguration)
	}
	base.Host = strings.ToLower(base.Host)
	if provider == "github" {
		if base.String() != "https://github.com" {
			return nil, failure(ReasonConfiguration)
		}
		base, _ = url.Parse("https://api.github.com")
	} else {
		base.Path += "/api/v1"
	}
	if cfg.APIBaseURL != "" && strings.TrimRight(cfg.APIBaseURL, "/") != base.String() {
		return nil, failure(ReasonConfiguration)
	}
	limits, err = normalizeLimits(limits)
	if err != nil {
		return nil, err
	}
	var copied http.Client
	if client != nil {
		copied = *client
	}
	transport := copied.Transport
	if transport == nil {
		transport = &http.Transport{
			Proxy: nil, DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
			TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12},
			TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 5 * time.Second,
			MaxResponseHeaderBytes: 32 << 10, DisableCompression: true,
			MaxIdleConns: 4, MaxIdleConnsPerHost: 2, IdleConnTimeout: 30 * time.Second,
		}
	} else if t, ok := transport.(*http.Transport); ok {
		t = t.Clone()
		if t.TLSClientConfig != nil && t.TLSClientConfig.InsecureSkipVerify {
			return nil, failure(ReasonConfiguration)
		}
		if t.TLSClientConfig == nil {
			t.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
		} else {
			t.TLSClientConfig = t.TLSClientConfig.Clone()
			if t.TLSClientConfig.MinVersion < tls.VersionTLS12 {
				t.TLSClientConfig.MinVersion = tls.VersionTLS12
			}
		}
		t.Proxy = nil
		t.DisableCompression = true
		t.MaxResponseHeaderBytes = 32 << 10
		transport = t
	}
	copied.Transport, copied.Jar = transport, nil
	copied.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if copied.Timeout <= 0 || copied.Timeout > limits.RequestTimeout {
		copied.Timeout = limits.RequestTimeout
	}
	return &Reader{base: base, token: strings.TrimSpace(cfg.Token), client: &copied, limits: limits, provider: provider}, nil
}

type scan struct {
	reader *Reader
	pages  int
	bytes  int64
}

func (s *scan) get(ctx context.Context, endpoint string) ([]byte, http.Header, error) {
	reqCtx, cancel := context.WithTimeout(ctx, s.reader.limits.RequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, nil, failure(ReasonTransport)
	}
	if s.reader.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.reader.token)
	}
	req.Header.Set("Accept", "application/json")
	if s.reader.provider == "github" {
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2026-03-10")
	} else if s.reader.token != "" {
		req.Header.Set("Authorization", "token "+s.reader.token)
	}
	resp, err := s.reader.client.Do(req)
	if err != nil {
		return nil, nil, failure(ReasonTransport)
	}
	if resp == nil || resp.Body == nil {
		return nil, nil, failure(ReasonTransport)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {

		failure := responseFailure(resp.StatusCode, resp.Header, time.Now())
		return nil, nil, failure
	}
	headerBytes := 0
	for key, values := range resp.Header {
		headerBytes += len(key)
		for _, value := range values {
			headerBytes += len(value)
		}
	}
	if headerBytes > 32<<10 || (resp.Header.Get("Content-Encoding") != "" && resp.Header.Get("Content-Encoding") != "identity") {
		return nil, nil, failure(ReasonTransport)
	}
	remaining := s.reader.limits.MaxTotalBytes - s.bytes
	limit := s.reader.limits.MaxResponseBytes
	if remaining < limit {
		limit = remaining
	}
	if limit <= 0 || resp.ContentLength > limit {
		return nil, nil, failure(ReasonTransport)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || reqCtx.Err() != nil {
		return nil, nil, failure(ReasonTransport)
	}
	if int64(len(body)) > limit {
		return nil, nil, failure(ReasonTransport)
	}
	s.bytes += int64(len(body))
	if !utf8.Valid(body) {
		return nil, nil, failure(ReasonTransport)
	}
	return body, resp.Header, nil
}
