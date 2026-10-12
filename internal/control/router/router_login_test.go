package router

import (
	"github.com/gofiber/fiber/v2"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPublicLoginRequiresCredentialsBeforeService(t *testing.T) {
	app := fiber.New()
	rt := &Router{}
	app.Post("/login", rt.login)
	for _, body := range []string{`null`, `{}`, `{"username":"root"}`, `{"email":"admin@example.com","password":""}`} {
		request := httptest.NewRequest("POST", "/login", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		response, err := app.Test(request)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode >= 500 {
			t.Fatalf("invalid login reached uninitialized service: %d", response.StatusCode)
		}
		response.Body.Close()
	}
}
