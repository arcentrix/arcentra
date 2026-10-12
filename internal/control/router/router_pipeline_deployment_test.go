package router

import "testing"

func TestDeploymentRecoveryRequiresExplicitConfirmationAndScope(t *testing.T) {
	app, _, release := deploymentRouterFixture(t)
	for _, tc := range []struct {
		user, method, path, body string
		want                     int
	}{
		{"", "GET", "/projects/a/releases/" + release + "/inspect", "", 401},
		{"project-user", "GET", "/projects/a/releases/" + release + "/inspect", "", 403},
		{"project-user", "GET", "/projects/b/releases/" + release + "/inspect", "", 403},
		{"deploy-user", "POST", "/projects/a/releases/" + release + "/recover", "{}", 400},
		{"deploy-user", "POST", "/projects/a/releases/" + release + "/recover", `{"confirmRecovery":false}`, 400},
		{"deploy-user", "POST", "/projects/a/releases/" + release + "/recover", `{"confirmRecovery":"true"}`, 400},
		{"project-user", "POST", "/projects/a/releases/" + release + "/recover", `{"confirmRecovery":true}`, 403},
		{"deploy-user", "POST", "/projects/a/releases/missing/recover", `{"confirmRecovery":true}`, 404},
	} {
		t.Run(tc.user+tc.method+tc.path+tc.body, func(t *testing.T) {
			status, response := deploymentRequest(t, app, tc.user, tc.method, tc.path, tc.body)
			if status != tc.want {
				t.Fatalf("status=%d want=%d response=%+v", status, tc.want, response)
			}
		})
	}
}
