package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/server/auth"
	"github.com/bestruirui/octopus/internal/server/resp"
	"github.com/gin-gonic/gin"
)

func TestAuthAllowsStatusAndPasswordChangeWhileBlockingOtherRoutes(t *testing.T) {
	token := setupForcedPasswordUser(t)
	gin.SetMode(gin.TestMode)
	router := gin.New()
	blockedHandlerCalled := false
	router.GET(userStatusPath, Auth(), func(c *gin.Context) {
		if username, _ := c.Get("username"); username != "admin" {
			t.Fatalf("username context = %v, want admin", username)
		}
		c.Status(http.StatusNoContent)
	})
	router.POST(changePasswordPath, Auth(), func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})
	router.GET("/api/v1/channel/list", Auth(), func(c *gin.Context) {
		blockedHandlerCalled = true
		c.Status(http.StatusNoContent)
	})

	for _, test := range []struct {
		method string
		path   string
		want   int
	}{
		{method: http.MethodGet, path: userStatusPath, want: http.StatusNoContent},
		{method: http.MethodPost, path: changePasswordPath, want: http.StatusNoContent},
		{method: http.MethodGet, path: "/api/v1/channel/list", want: http.StatusForbidden},
	} {
		request := httptest.NewRequest(test.method, test.path, nil)
		request.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if got := response.Header().Get("Cache-Control"); got != "no-store" {
			t.Fatalf("%s %s Cache-Control = %q, want no-store", test.method, test.path, got)
		}
		if response.Code != test.want {
			t.Fatalf("%s %s status = %d, want %d; body=%s", test.method, test.path, response.Code, test.want, response.Body.String())
		}
		if test.want == http.StatusForbidden {
			if blockedHandlerCalled {
				t.Fatal("protected handler ran while password change was required")
			}
			var body resp.ResponseStruct
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode forbidden response: %v", err)
			}
			if body.Error == nil || body.Error.Code != resp.CodePasswordChangeRequired {
				t.Fatalf("forbidden error = %#v, want %s", body.Error, resp.CodePasswordChangeRequired)
			}
		}
	}
}

func setupForcedPasswordUser(t *testing.T) string {
	t.Helper()
	if err := db.InitDB("sqlite", filepath.Join(t.TempDir(), "auth.db"), false); err != nil {
		t.Fatalf("init db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	user := model.User{
		Username:           "admin",
		Password:           "initial-password",
		MustChangePassword: true,
		JWTSecret:          "middleware-test-jwt-secret",
	}
	if err := user.HashPassword(); err != nil {
		t.Fatalf("hash password: %v", err)
	}
	if err := db.GetDB().Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := op.UserInit(); err != nil {
		t.Fatalf("init user service: %v", err)
	}
	token, _, err := auth.GenerateJWTToken(60)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	return token
}
