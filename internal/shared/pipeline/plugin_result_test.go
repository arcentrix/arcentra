package pipeline

import "testing"

func TestCheckPluginResultRejectsExplicitFailure(t *testing.T) {
	for _, raw := range []string{`{"success":false}`, `{"success":true,"exitCode":2}`, `{"exit_code":12,"error":"deployment failed"}`, `not-json`} {
		if err := checkPluginResult([]byte(raw)); err == nil {
			t.Errorf("failed result accepted: %s", raw)
		}
	}
	for _, raw := range []string{"", `{}`, `{"success":true}`, `{"artifact":"result.tar"}`} {
		if err := checkPluginResult([]byte(raw)); err != nil {
			t.Errorf("success rejected: %s: %v", raw, err)
		}
	}
}
