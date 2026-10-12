package router

import (
	apphttp "github.com/arcentrix/arcentra/pkg/http"
	"github.com/bytedance/sonic"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/arcentrix/arcentra/pkg/http/jwt"
	"github.com/gofiber/fiber/v2"
)

func TestPipelineTriggerRejectsForgedAuditActorBeforeExecution(t *testing.T) {
	for _, authenticated := range []bool{false, true} {
		app := fiber.New()
		rt := &Router{} // Any execution/service access would panic: rejection must precede it.
		app.Post("/pipelines/:pipelineID/trigger", func(c *fiber.Ctx) error {
			if authenticated {
				c.Locals("claims", &jwt.AuthClaims{UserID: "actual-user"})
			}
			return rt.triggerPipeline(c)
		})
		req := httptest.NewRequest("POST", "/pipelines/example/trigger", strings.NewReader(`{"triggeredBy":"forged-admin"}`))
		req.Header.Set("Content-Type", "application/json")
		response, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		raw, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		var result apphttp.ResponseErr
		if err := sonic.Unmarshal(raw, &result); err != nil {
			t.Fatal(err)
		}
		want := apphttp.Unauthorized.Code
		if authenticated {
			want = apphttp.BadRequest.Code
		}
		if result.ErrCode != want {
			t.Fatalf("unexpected rejection: %s", raw)
		}
	}
}
