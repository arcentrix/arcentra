// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package admission

import (
	"errors"
	"testing"
)

func TestRejectionClassificationRetainsPersistenceFailure(t *testing.T) {
	cause := errors.New("schema rejected")
	persist := errors.New("persistence unknown")
	rejected := &RejectionError{Cause: cause}
	joined := errors.Join(rejected, persist)
	if !IsRejected(joined) || !errors.Is(joined, cause) || !errors.Is(joined, persist) {
		t.Fatal("joined failure lost classification or cause")
	}
	if IsRejected(nil) || IsRejected(persist) {
		t.Fatal("unknown error treated as rejected")
	}
}
