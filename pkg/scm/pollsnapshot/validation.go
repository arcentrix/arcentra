// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package pollsnapshot

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type Limits struct {
	PageSize         int
	MaxPages         int
	MaxItems         int
	MaxResponseBytes int64
	MaxTotalBytes    int64
	RequestTimeout   time.Duration
	ScanTimeout      time.Duration
}

// DefaultLimits 返回有界的默认读取配置
func DefaultLimits() Limits {
	return Limits{
		PageSize: 100, MaxPages: 64, MaxItems: 2000,
		MaxResponseBytes: 2 << 20, MaxTotalBytes: 16 << 20,
		RequestTimeout: 10 * time.Second, ScanTimeout: 45 * time.Second,
	}
}

func normalizeLimits(v Limits) (Limits, error) {
	d := DefaultLimits()
	ints := [][2]*int{{&v.PageSize, &d.PageSize}, {&v.MaxPages, &d.MaxPages}, {&v.MaxItems, &d.MaxItems}}
	for _, p := range ints {
		if *p[0] == 0 {
			*p[0] = *p[1]
		}
		if *p[0] < 1 || *p[0] > *p[1] {
			return v, failure(ReasonConfiguration)
		}
	}
	for _, p := range [][2]*int64{{&v.MaxResponseBytes, &d.MaxResponseBytes}, {&v.MaxTotalBytes, &d.MaxTotalBytes}} {
		if *p[0] == 0 {
			*p[0] = *p[1]
		}
		if *p[0] < 1 || *p[0] > *p[1] {
			return v, failure(ReasonConfiguration)
		}
	}
	for _, p := range [][2]*time.Duration{{&v.RequestTimeout, &d.RequestTimeout}, {&v.ScanTimeout, &d.ScanTimeout}} {
		if *p[0] == 0 {
			*p[0] = *p[1]
		}
		if *p[0] <= 0 || *p[0] > *p[1] {
			return v, failure(ReasonConfiguration)
		}
	}
	return v, nil
}

var snapshotPathPart = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,254}$`)
var snapshotTimestamp = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]{1,9})?(Z|[+-]([01][0-9]|2[0-3]):[0-5][0-9])$`)
var snapshotSHA = regexp.MustCompile(`^[a-f0-9]{40}([a-f0-9]{24})?$`)

func snapshotParseTime(raw string) (time.Time, error) {
	if !snapshotTimestamp.MatchString(raw) {
		return time.Time{}, failure(ReasonMalformed)
	}
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil || !validSnapshotTime(parsed) || strings.Contains(raw, ",") {
		return time.Time{}, failure(ReasonMalformed)
	}
	return parsed.UTC(), nil
}

func validSnapshotBranch(name string) bool {
	if name == "" || len(name) > 1024 || !utf8.ValidString(name) || strings.HasPrefix(name, "-") || strings.HasPrefix(name, "/") || strings.HasSuffix(name, "/") || strings.HasSuffix(name, ".") || strings.Contains(name, "..") || strings.Contains(name, "@{") || strings.ContainsAny(name, " ~^:?*[\\") {
		return false
	}
	for _, c := range name {
		if unicode.IsControl(c) {
			return false
		}
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	return true
}

func snapshotDigest(parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		_, _ = fmt.Fprintf(h, "%d:", len(part))
		_, _ = h.Write([]byte(part))
	}
	return hex.EncodeToString(h.Sum(nil))
}
func validSnapshotTime(t time.Time) bool {
	year := t.UTC().Year()
	return !t.IsZero() && year >= 1 && year <= 9999
}

func validSnapshotRepository(name string) bool {
	parts := strings.Split(name, "/")
	return len(parts) == 2 && snapshotPathPart.MatchString(parts[0]) && snapshotPathPart.MatchString(parts[1]) && !strings.HasSuffix(parts[0], ".") && !strings.HasSuffix(parts[1], ".") && !strings.HasSuffix(parts[1], ".git")
}
