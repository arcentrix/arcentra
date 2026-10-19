// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package service

import (
	"github.com/arcentrix/arcentra/pkg/scm/sourcefile"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

var copilotHunk = regexp.MustCompile(`^@@ -([0-9]+)(?:,([0-9]+))? \+([0-9]+)(?:,([0-9]+))? @@(?: .*)?$`)

// verifyCopilotPatch accepts a conservative unified-diff subset and applies it
// to bounded memory only. No shell, working tree, filesystem or SCM is involved.
func verifyCopilotPatch(base []byte, path, patch string) ([]byte, error) {
	fail := func() ([]byte, error) { return nil, ErrCopilotPlanUnavailable }
	if len(base) > 8192 || len(patch) > 16384 || len(patch) == 0 || !utf8.Valid(base) || !utf8.ValidString(patch) || strings.ContainsRune(string(base), 0) || strings.ContainsRune(patch, 0) || !sourcefile.ValidPath(path) || strings.ContainsAny(path, "\t\r\n") || !strings.HasSuffix(patch, "\n") {
		return fail()
	}
	// Reject non-terminated base lines rather than silently changing newline
	// semantics or accepting malformed no-newline sentinel metadata.
	if len(base) > 0 && !strings.HasSuffix(string(base), "\n") {
		return fail()
	}
	lines := strings.Split(strings.TrimSuffix(patch, "\n"), "\n")
	if len(lines) < 3 {
		return fail()
	}
	if !((lines[0] == "--- a/"+path && lines[1] == "+++ b/"+path) || (lines[0] == "--- "+path && lines[1] == "+++ "+path)) {
		return fail()
	}
	original := []string{}
	if len(base) > 0 {
		original = strings.Split(strings.TrimSuffix(string(base), "\n"), "\n")
	}
	result := []string{}
	cursor := 0
	hunks := 0
	for i := 2; i < len(lines); {
		m := copilotHunk.FindStringSubmatch(lines[i])
		if m == nil {
			return fail()
		}
		i++
		hunks++
		number := func(v string, defaultValue int) (int, bool) {
			if v == "" {
				return defaultValue, true
			}
			n, e := strconv.Atoi(v)
			return n, e == nil && n >= 0 && n <= 16384
		}
		oldStart, a := number(m[1], 0)
		oldCount, b := number(m[2], 1)
		newStart, c := number(m[3], 0)
		newCount, d := number(m[4], 1)
		if !a || !b || !c || !d || oldCount == 0 && newCount == 0 {
			return fail()
		}
		oldIndex := oldStart - 1
		if oldCount == 0 {
			oldIndex = oldStart
		}
		newIndex := newStart - 1
		if newCount == 0 {
			newIndex = newStart
		}
		if oldIndex < cursor || oldIndex < 0 || oldIndex > len(original) || newIndex != len(result)+oldIndex-cursor {
			return fail()
		}
		result = append(result, original[cursor:oldIndex]...)
		cursor = oldIndex
		consumed, produced := 0, 0
		for i < len(lines) && !strings.HasPrefix(lines[i], "@@ ") {
			line := lines[i]
			i++
			if len(line) == 0 {
				return fail()
			}
			content := line[1:]
			if strings.Contains(content, "[REDACTED") || strings.Contains(content, "***MASKED***") {
				return fail()
			}
			switch line[0] {
			case ' ':
				if cursor >= len(original) || original[cursor] != content {
					return fail()
				}
				result = append(result, content)
				cursor++
				consumed++
				produced++
			case '-':
				if cursor >= len(original) || original[cursor] != content {
					return fail()
				}
				cursor++
				consumed++
			case '+':
				result = append(result, content)
				produced++
			default:
				return fail()
			}
			if consumed > oldCount || produced > newCount {
				return fail()
			}
		}
		if consumed != oldCount || produced != newCount {
			return fail()
		}
	}
	if hunks == 0 {
		return fail()
	}
	result = append(result, original[cursor:]...)
	out := ""
	if len(result) > 0 {
		out = strings.Join(result, "\n") + "\n"
	}
	if len(out) > 16384 || len(out) == 0 {
		return fail()
	}
	return []byte(out), nil
}
