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

// V3Reader deliberately does not share wire decoders or pagination with v1/v2.
// Its only supported services are public Gitee.com v5 and Bitbucket Cloud 2.0.
// Custom RoundTrippers are a trusted synthetic-test seam, not user input.
type V3Reader struct {
	base     *url.URL
	token    string
	client   *http.Client
	limits   Limits
	provider string
}

func NewV3Reader(cfg scm.ProviderConfig, client *http.Client, limits Limits) (*V3Reader, error) {
	provider := string(cfg.Kind)
	public, api := "", ""
	switch provider {
	case "gitee":
		public, api = "https://gitee.com", "https://gitee.com/api/v5"
	case "bitbucket":
		public, api = "https://bitbucket.org", "https://api.bitbucket.org/2.0"
	default:
		return nil, failure(ReasonConfiguration)
	}
	if cfg.BaseURL != public && cfg.BaseURL != public+"/" || cfg.APIBaseURL != "" && cfg.APIBaseURL != api && cfg.APIBaseURL != api+"/" || len(cfg.Token) > 8192 || strings.ContainsAny(cfg.Token, "\r\n\x00") || strings.TrimSpace(cfg.Token) != cfg.Token {
		return nil, failure(ReasonConfiguration)
	}
	var err error
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
		transport = &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 5 * time.Second, MaxResponseHeaderBytes: 32 << 10, DisableCompression: true, MaxIdleConns: 4, MaxIdleConnsPerHost: 2, IdleConnTimeout: 30 * time.Second}
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
		if t.TLSHandshakeTimeout <= 0 || t.TLSHandshakeTimeout > 5*time.Second {
			t.TLSHandshakeTimeout = 5 * time.Second
		}
		if t.ResponseHeaderTimeout <= 0 || t.ResponseHeaderTimeout > 5*time.Second {
			t.ResponseHeaderTimeout = 5 * time.Second
		}
		transport = t
	}
	copied.Transport, copied.Jar = transport, nil
	copied.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if copied.Timeout <= 0 || copied.Timeout > limits.RequestTimeout {
		copied.Timeout = limits.RequestTimeout
	}
	base, _ := url.Parse(api)
	return &V3Reader{base: base, token: cfg.Token, client: &copied, limits: limits, provider: provider}, nil
}

type v3Scan struct {
	reader   *V3Reader
	requests int
	bytes    int64
}

// All requests, including both repository binding reads, share one budget.
// Credential-bearing Gitee URLs are assembled only here and never returned.
func (s *v3Scan) get(ctx context.Context, endpoint string) ([]byte, http.Header, error) {
	if s.requests >= s.reader.limits.MaxPages {
		return nil, nil, failure(ReasonLimit)
	}
	s.requests++
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != s.reader.base.Scheme || u.Host != s.reader.base.Host || u.User != nil || u.Fragment != "" || u.RawPath != "" || u.Opaque != "" || u.ForceQuery || !strings.HasPrefix(u.Path, s.reader.base.Path+"/") {
		return nil, nil, failure(ReasonConfiguration)
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil || q.Has("access_token") {
		return nil, nil, failure(ReasonConfiguration)
	}
	if s.reader.provider == "gitee" && s.reader.token != "" {
		q.Set("access_token", s.reader.token)
		u.RawQuery = q.Encode()
	}
	reqCtx, cancel := context.WithTimeout(ctx, s.reader.limits.RequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, nil, failure(ReasonTransport)
	}
	req.Header.Set("Accept", "application/json")
	if s.reader.provider == "bitbucket" && s.reader.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.reader.token)
	}
	resp, err := s.reader.client.Do(req)
	if err != nil {
		return nil, nil, failure(ReasonTransport)
	}
	if resp == nil || resp.Body == nil {
		return nil, nil, failure(ReasonTransport)
	}
	defer resp.Body.Close()
	headerBytes := 0
	for key, values := range resp.Header {
		headerBytes += len(key)
		for _, value := range values {
			headerBytes += len(value)
		}
	}
	if headerBytes > 32<<10 {
		return nil, nil, failure(ReasonTransport)
	}
	if resp.StatusCode != http.StatusOK {
		h := resp.Header
		if s.reader.provider == "bitbucket" {
			h = http.Header{}
			h.Set("Retry-After", resp.Header.Get("Retry-After"))
		}
		return nil, nil, responseFailure(resp.StatusCode, h, time.Now())
	}
	if resp.Header.Get("Content-Encoding") != "" && resp.Header.Get("Content-Encoding") != "identity" {
		return nil, nil, failure(ReasonTransport)
	}
	limit := s.reader.limits.MaxResponseBytes
	if remaining := s.reader.limits.MaxTotalBytes - s.bytes; remaining < limit {
		limit = remaining
	}
	if limit <= 0 || resp.ContentLength > limit {
		return nil, nil, failure(ReasonTransport)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || reqCtx.Err() != nil || int64(len(body)) > limit || !utf8.Valid(body) {
		return nil, nil, failure(ReasonTransport)
	}
	s.bytes += int64(len(body))
	return body, resp.Header, nil
}
