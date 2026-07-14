package handlers

import (
	"net/http"
	"time"

	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/server/auth"
	"github.com/bestruirui/octopus/internal/server/middleware"
	"github.com/bestruirui/octopus/internal/server/resp"
	"github.com/bestruirui/octopus/internal/server/router"
	"github.com/gin-gonic/gin"
)

func init() {
	router.NewGroupRouter("/api/v1/user").
		Use(middleware.RequireJSON()).
		Use(middleware.RateLimit(5, time.Minute)).
		AddRoute(
			router.NewRoute("/login", http.MethodPost).
				Handle(login),
		)
	router.NewGroupRouter("/api/v1/user").
		Use(middleware.Auth()).
		Use(middleware.RequireJSON()).
		AddRoute(
			router.NewRoute("/change-password", http.MethodPost).
				Handle(changePassword),
		).
		AddRoute(
			router.NewRoute("/change-username", http.MethodPost).
				Handle(changeUsername),
		).
		AddRoute(
			router.NewRoute("/status", http.MethodGet).
				Handle(status),
		)
}

func login(c *gin.Context) {
	var user model.UserLogin
	if err := c.ShouldBindJSON(&user); err != nil {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidJSON)
		return
	}
	if err := op.UserVerify(user.Username, user.Password); err != nil {
		middleware.AuditLog(c, middleware.EventUserLoginFailed, map[string]interface{}{
			"username": user.Username,
			"reason":   "invalid_credentials",
		})
		resp.Error(c, http.StatusUnauthorized, resp.ErrUnauthorized)
		return
	}
	token, expire, err := auth.GenerateJWTToken(user.Expire)
	if err != nil {
		resp.Error(c, http.StatusInternalServerError, resp.ErrInternalServer)
		return
	}
	middleware.AuditLog(c, middleware.EventUserLogin, map[string]interface{}{
		"username":   user.Username,
		"expire_sec": user.Expire,
	})
	resp.Success(c, model.UserLoginResponse{
		Token:              token,
		ExpireAt:           expire,
		MustChangePassword: op.UserMustChangePassword(),
	})
}

func changePassword(c *gin.Context) {
	var user model.UserChangePassword
	if err := c.ShouldBindJSON(&user); err != nil {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidJSON)
		return
	}

	username, _ := c.Get("username")
	usernameStr, _ := username.(string)

	// 二次确认：验证当前密码
	if err := op.UserVerify(usernameStr, user.OldPassword); err != nil {
		middleware.AuditLog(c, middleware.EventSensitiveOperationDenied, map[string]interface{}{
			"username":  usernameStr,
			"operation": "password_change",
			"reason":    "invalid_current_password",
		})
		resp.Error(c, http.StatusUnauthorized, resp.ErrUnauthorized)
		return
	}

	if err := op.UserChangePassword(user.OldPassword, user.NewPassword); err != nil {
		resp.Error(c, http.StatusInternalServerError, resp.ErrDatabase)
		return
	}
	middleware.AuditLog(c, middleware.EventPasswordChange, map[string]interface{}{
		"username": username,
	})
	resp.Success(c, "password changed successfully")
}

func changeUsername(c *gin.Context) {
	var user model.UserChangeUsername
	if err := c.ShouldBindJSON(&user); err != nil {
		resp.Error(c, http.StatusBadRequest, resp.ErrInvalidJSON)
		return
	}
	oldUsername, _ := c.Get("username")
	usernameStr, _ := oldUsername.(string)
	if err := op.UserVerify(usernameStr, user.CurrentPassword); err != nil {
		middleware.AuditLog(c, middleware.EventUsernameChangeFailed, map[string]interface{}{
			"username": usernameStr,
			"reason":   "invalid_password",
		})
		resp.Error(c, http.StatusUnauthorized, resp.ErrUnauthorized)
		return
	}
	if err := op.UserChangeUsername(user.NewUsername); err != nil {
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	middleware.AuditLog(c, middleware.EventUsernameChange, map[string]interface{}{
		"old_username": oldUsername,
		"new_username": user.NewUsername,
	})
	resp.Success(c, "username changed successfully")
}

func status(c *gin.Context) {
	resp.Success(c, "ok")
}
