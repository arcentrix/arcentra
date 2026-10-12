package executor

import (
	"sort"
	"strings"
)

type secretRedactor struct {
	values  []string
	longest int
	pending map[string]string
}

func newSecretRedactor(env map[string]string) *secretRedactor {
	r := &secretRedactor{pending: map[string]string{}}
	seen := map[string]bool{}
	for key, value := range env {
		if strings.HasPrefix(key, "secrets.") && value != "" && !seen[value] {
			r.values = append(r.values, value)
			seen[value] = true
			r.longest = max(r.longest, len(value))
		}
	}
	sort.Slice(r.values, func(i, j int) bool { return len(r.values[i]) > len(r.values[j]) })
	return r
}
func (r *secretRedactor) redact(value string) string {
	for _, secret := range r.values {
		value = strings.ReplaceAll(value, secret, "***")
	}
	return value
}
func (r *secretRedactor) chunk(stream, content string, final bool) string {
	if r.longest == 0 {
		return content
	}
	raw := r.pending[stream] + content
	n := len(raw)
	if !final {
		n = max(0, len(raw)-r.longest+1)
	}
	// 保留跨分块的完整秘密，不能提前发布其前缀
	for _, secret := range r.values {
		start := 0
		for {
			offset := strings.Index(raw[start:], secret)
			if offset < 0 {
				break
			}
			offset += start
			if offset < n && offset+len(secret) > n {
				n = offset
			}
			start = offset + 1
			if start >= len(raw) {
				break
			}
		}
	}
	r.pending[stream] = raw[n:]
	return r.redact(raw[:n])
}

type redactedExecutionError struct {
	cause   error
	message string
}

func (e redactedExecutionError) Error() string { return e.message }
func (e redactedExecutionError) Unwrap() error { return e.cause }
