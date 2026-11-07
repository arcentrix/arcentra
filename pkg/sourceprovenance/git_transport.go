// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package sourceprovenance

import (
	"fmt"
	"net"
	"net/url"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

// GitCheckoutTransport classifies the existing hardened Agent checkout transports.
// It performs no network access. Native git is anonymous and provides no transport encryption.
// Callers implementing an offline runtime must additionally reject host-local file sources.
func GitCheckoutTransport(raw string, authenticated bool) (string, error) {
	if filepath.IsAbs(raw) {
		return "file", nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Opaque != "" || u.Fragment != "" || u.RawQuery != "" || u.Hostname() == "" {
		return "", fmt.Errorf("unsupported source transport")
	}
	switch u.Scheme {
	case "git":
		// Native anonymous upload-pack only. Do not admit helper syntax, credentials,
		// ambiguous URL components or inherited proxy/config transport selection.
		if authenticated || strings.ContainsAny(raw, "%?#\\ \t\r\n") || u.ForceQuery || u.RawPath != "" || u.Path == "" || u.Path == "/" || !strings.HasPrefix(u.Path, "/") || path.Clean(u.Path) != u.Path || strings.HasSuffix(u.Host, ":") {
			return "", fmt.Errorf("unsupported source transport")
		}
		host := u.Hostname()
		if strings.Contains(host, ":") {
			if net.ParseIP(host) == nil || !strings.HasPrefix(u.Host, "[") {
				return "", fmt.Errorf("unsupported source transport")
			}
		} else {
			for _, label := range strings.Split(host, ".") {
				if label == "" || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
					return "", fmt.Errorf("unsupported source transport")
				}
				for _, c := range label {
					if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
						return "", fmt.Errorf("unsupported source transport")
					}
				}
			}
		}
		if port := u.Port(); port != "" {
			n, err := strconv.Atoi(port)
			if err != nil || n < 1 || n > 65535 {
				return "", fmt.Errorf("unsupported source transport")
			}
		}
		return "git", nil
	case "https":
		return "https", nil
	case "http":
		host := u.Hostname()
		ip := net.ParseIP(host)
		if (ip != nil && ip.IsLoopback()) && !authenticated {
			return "http", nil
		}
	}
	return "", fmt.Errorf("unsupported source transport")
}
