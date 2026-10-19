// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package pollsnapshot

import (
	"bytes"
	"encoding/json"
	"io"
	"strconv"
	"strings"
)

// Reject duplicate fields and bound structural amplification, including unknown
// fields, before typed decoding. Unknown presentation fields are never retained.
func validateV3JSON(body []byte) error {
	d := json.NewDecoder(bytes.NewReader(body))
	d.UseNumber()
	nodes := 0
	if err := walkV3JSON(d, 0, &nodes); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return failure(ReasonMalformed)
	}
	return nil
}
func walkV3JSON(d *json.Decoder, depth int, nodes *int) error {
	*nodes++
	if depth > 64 || *nodes > 200000 {
		return failure(ReasonLimit)
	}
	t, err := d.Token()
	if err != nil {
		return failure(ReasonMalformed)
	}
	delim, container := t.(json.Delim)
	if !container {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			key, err := d.Token()
			name, ok := key.(string)
			name = strings.ToLower(name)
			if err != nil || !ok || seen[name] {
				return failure(ReasonMalformed)
			}
			if len(seen) >= 256 {
				return failure(ReasonLimit)
			}
			seen[name] = true
			if err := walkV3JSON(d, depth+1, nodes); err != nil {
				return err
			}
		}
		end, err := d.Token()
		if err != nil || end != json.Delim('}') {
			return failure(ReasonMalformed)
		}
	case '[':
		for d.More() {
			if err := walkV3JSON(d, depth+1, nodes); err != nil {
				return err
			}
		}
		end, err := d.Token()
		if err != nil || end != json.Delim(']') {
			return failure(ReasonMalformed)
		}
	default:
		return failure(ReasonMalformed)
	}
	return nil
}
func decodeV3Object(body []byte, out any) error {
	if err := validateV3JSON(body); err != nil {
		return err
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '{' || json.Unmarshal(body, out) != nil {
		return failure(ReasonMalformed)
	}
	return nil
}
func decodeV3Array[T any](d *json.Decoder, limit int) ([]T, error) {
	if limit < 1 || limit > DefaultLimits().PageSize {
		return nil, failure(ReasonLimit)
	}
	token, err := d.Token()
	if err != nil || token != json.Delim('[') {
		return nil, failure(ReasonMalformed)
	}
	out := make([]T, 0, limit)
	for d.More() {
		if len(out) >= limit {
			return nil, failure(ReasonLimit)
		}
		var item T
		if d.Decode(&item) != nil {
			return nil, failure(ReasonMalformed)
		}
		out = append(out, item)
	}
	token, err = d.Token()
	if err != nil || token != json.Delim(']') {
		return nil, failure(ReasonMalformed)
	}
	return out, nil
}
func decodeV3Int(d *json.Decoder, min int) (int, error) {
	token, err := d.Token()
	number, ok := token.(json.Number)
	if err != nil || !ok {
		return 0, failure(ReasonMalformed)
	}
	n, err := strconv.Atoi(string(number))
	if err != nil || n < min || strconv.Itoa(n) != string(number) {
		return 0, failure(ReasonMalformed)
	}
	return n, nil
}
