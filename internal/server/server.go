package server

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/bestruirui/octopus/internal/conf"
	"github.com/bestruirui/octopus/internal/relay"
	_ "github.com/bestruirui/octopus/internal/server/handlers"
	"github.com/bestruirui/octopus/internal/server/middleware"
	"github.com/bestruirui/octopus/internal/server/resp"
	"github.com/bestruirui/octopus/internal/server/router"
	"github.com/bestruirui/octopus/internal/utils/log"
	"github.com/bestruirui/octopus/static"
	"github.com/gin-gonic/gin"
	"github.com/looplj/axonhub/llm"
)

var httpSrv http.Server

func Start() error {
	if conf.IsDebug() {
		gin.SetMode(gin.DebugMode)
	} else {
		gin.SetMode(gin.ReleaseMode)
	}

	r := gin.New()
	r.Use(gin.CustomRecovery(func(c *gin.Context, recovered interface{}) {
		log.Logger.Errorw("panic recovered",
			"error", recovered,
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"client_ip", c.ClientIP(),
			"user_agent", c.Request.UserAgent(),
		)
		resp.Error(c, http.StatusInternalServerError, resp.ErrInternalServer)
		c.Abort()
	}))

	if conf.IsDebug() {
		r.Use(middleware.Logger())
	}
	r.Use(middleware.Cors())
	r.Use(middleware.StaticEmbed("/", static.StaticFS))

	registerRelayRoutes(r)
	if err := router.RegisterAll(r); err != nil {
		return fmt.Errorf("register routes: %w", err)
	}

	httpSrv.Addr = fmt.Sprintf("%s:%d", conf.AppConfig.Server.Host, conf.AppConfig.Server.Port)
	httpSrv.Handler = r

	// 先同步绑定端口，端口占用等错误立刻返回给启动链；
	// 绑定成功后再交给 goroutine Serve，不再用固定 sleep 猜测启动结果。
	ln, err := net.Listen("tcp", httpSrv.Addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", httpSrv.Addr, err)
	}
	go func() {
		if err := httpSrv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Errorf("http server serve error: %v", err)
		}
	}()
	return nil
}

func Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return httpSrv.Shutdown(ctx)
}

func registerRelayRoutes(r *gin.Engine) {
	v1 := r.Group("/v1", middleware.APIKeyAuth())
	v1.POST("/chat/completions", middleware.RequireJSON(), relay.Handler(llm.APIFormatOpenAIChatCompletion))
	v1.POST("/responses", middleware.RequireJSON(), relay.Handler(llm.APIFormatOpenAIResponse))
	v1.POST("/responses/compact", middleware.RequireJSON(), relay.Handler(llm.APIFormatOpenAIResponseCompact))
	v1.POST("/messages", middleware.RequireJSON(), relay.Handler(llm.APIFormatAnthropicMessage))
	v1.POST("/embeddings", middleware.RequireJSON(), relay.Handler(llm.APIFormatOpenAIEmbedding))
	v1.POST("/images/generations", middleware.RequireJSON(), relay.Handler(llm.APIFormatOpenAIImageGeneration))
	v1.POST("/images/edits", relay.Handler(llm.APIFormatOpenAIImageEdit))
	v1.POST("/images/variations", relay.Handler(llm.APIFormatOpenAIImageVariation))

	v1beta := r.Group("/v1beta", middleware.APIKeyAuth())
	v1beta.POST("/models/*action", middleware.RequireJSON(), geminiContentActionOnly(relay.Handler(llm.APIFormatGeminiContents)))
}

func geminiContentActionOnly(next gin.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		action := c.Param("action")
		if !strings.HasPrefix(action, "/") || (!strings.HasSuffix(action, ":generateContent") && !strings.HasSuffix(action, ":streamGenerateContent")) {
			resp.Error(c, http.StatusNotFound, resp.ErrResourceNotFound)
			c.Abort()
			return
		}
		next(c)
	}
}
