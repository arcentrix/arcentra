// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package repo

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestCopilotRerunAdmissionNonceIsBoundAndRedacted(t *testing.T) {
	in := CopilotRerunClaimInput{AgentID: "agent", PipelineID: "pipeline", RunID: uuid.NewString(), JobRunID: uuid.NewString(), GateID: uuid.NewString(), ScheduleID: uuid.NewString(), SealID: uuid.NewString(), ClaimID: uuid.NewString(), SealDigest: "v1:" + strings.Repeat("a", 64), PayloadDigest: "v1:" + strings.Repeat("b", 64), AdmissionNonce: []byte(strings.Repeat("synthetic-nonce!", 2))}
	require.Len(t, in.AdmissionNonce, 32)
	digest, err := rerunClaimNonceDigest(in)
	require.NoError(t, err)
	require.True(t, rerunClaimNonceMatches(digest, in))
	for _, which := range []string{"nonce", "agent", "claim", "gate", "seal", "digest-as-nonce"} {
		t.Run(which, func(t *testing.T) {
			changed := in
			changed.AdmissionNonce = append([]byte(nil), in.AdmissionNonce...)
			switch which {
			case "nonce":
				changed.AdmissionNonce[0] ^= 1
			case "agent":
				changed.AgentID = "other"
			case "claim":
				changed.ClaimID = uuid.NewString()
			case "gate":
				changed.GateID = uuid.NewString()
			case "seal":
				changed.SealID = uuid.NewString()
			case "digest-as-nonce":
				changed.AdmissionNonce = []byte(digest)
			}
			require.False(t, rerunClaimNonceMatches(digest, changed))
		})
	}
	for _, format := range []string{"%v", "%+v", "%#v", "%s", "%x"} {
		require.NotContains(t, fmt.Sprintf(format, in), "synthetic-nonce")
	}
	body, err := json.Marshal(in)
	require.NoError(t, err)
	require.NotContains(t, string(body), "AdmissionNonce")
}

func TestCopilotRerunClaimScheduleAllowsOnlyLeaseDrift(t *testing.T) {
	original := model.CopilotRerunSchedule{}
	typ := reflect.TypeOf(original)
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		t.Run(field.Name, func(t *testing.T) {
			changed := original
			value := reflect.ValueOf(&changed).Elem().Field(i)
			switch value.Kind() {
			case reflect.String:
				value.SetString("changed")
			case reflect.Uint64, reflect.Uint32:
				value.SetUint(1)
			case reflect.Bool:
				value.SetBool(true)
			case reflect.Slice:
				value.SetBytes([]byte("changed"))
			case reflect.Struct:
				value.Set(reflect.ValueOf(time.Unix(1, 0).UTC()))
			case reflect.Pointer:
				if value.Type().Elem().Kind() == reflect.String {
					v := "changed"
					value.Set(reflect.ValueOf(&v))
				} else {
					v := time.Unix(1, 0).UTC()
					value.Set(reflect.ValueOf(&v))
				}
			default:
				t.Fatalf("uncovered schedule field %s", field.Name)
			}
			allowed := field.Name == "LeaseOwner" || field.Name == "LeaseEpoch" || field.Name == "LeaseUntil" || field.Name == "Version"
			require.Equal(t, allowed, rerunClaimScheduleUnchanged(original, changed))
		})
	}
}
