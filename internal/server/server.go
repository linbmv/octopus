package server

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/bestruirui/octopus/internal/conf"
	"github.com/bestruirui/octopus/internal/metrics"
	"github.com/bestruirui/octopus/internal/relay"
	_ "github.com/bestruirui/octopus/internal/server/handlers"
	"github.com/bestruirui/octopus/internal/server/middleware"
	"github.com/bestruirui/octopus/internal/server/resp"
	"github.com/bestruirui/octopus/internal/server/router"
	"github.com/bestruirui/octopus/internal/tracing"
	"github.com/bestruirui/octopus/internal/utils/log"
	"github.com/bestruirui/octopus/static"
	"github.com/gin-gonic/gin"
	"github.com/looplj/axonhub/llm"
)

var (
	httpSrvMu  sync.Mutex
	httpSrv    *http.Server
	metricsSrv *http.Server
	serveDone  chan struct{}
)

func Start() error {
	config := conf.Current()
	if conf.IsDebug() {
		gin.SetMode(gin.DebugMode)
	} else {
		gin.SetMode(gin.ReleaseMode)
	}

	r := gin.New()
	if err := middleware.ConfigureSessionCookies(config.Server); err != nil {
		return fmt.Errorf("configure session cookies: %w", err)
	}
	// The secure default trusts no proxy. Explicit IP/CIDR entries are validated
	// before this point and let deployments opt into sanitized forwarding chains.
	if err := r.SetTrustedProxies(config.Server.TrustedProxies); err != nil {
		return fmt.Errorf("configure trusted proxies: %w", err)
	}
	r.Use(gin.CustomRecovery(func(c *gin.Context, recovered interface{}) {
		log.WithContext(c.Request.Context()).Errorw("panic recovered",
			"error", recovered,
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"client_ip", c.ClientIP(),
			"user_agent", c.Request.UserAgent(),
		)
		resp.Error(c, http.StatusInternalServerError, resp.ErrInternalServer)
		c.Abort()
	}))

	r.Use(middleware.RequestID())
	r.Use(middleware.SecurityHeaders())
	r.Use(tracing.Middleware())
	r.Use(metrics.Middleware())
	// Metrics are deliberately unavailable on the public application listener.
	// When enabled they are served by a separately bound HTTP server below.
	r.GET("/metrics", func(c *gin.Context) { c.Status(http.StatusNotFound) })
	if conf.IsDebug() {
		r.Use(middleware.Logger())
	}
	r.Use(middleware.Cors())
	r.Use(middleware.StaticEmbed("/", static.StaticFS))

	registerRelayRoutes(r)
	if err := router.RegisterAll(r); err != nil {
		return fmt.Errorf("register routes: %w", err)
	}

	// http.Server 在调用 Shutdown 后不能再次 Serve；每次启动都创建新实例，
	// 使测试、进程内重启和未来热重载路径不会复用已关闭状态。
	srv := &http.Server{
		Addr:              fmt.Sprintf("%s:%d", config.Server.Host, config.Server.Port),
		Handler:           r,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	// 先同步绑定端口，端口占用等错误立刻返回给启动链；
	// 绑定成功后再交给 goroutine Serve，不再用固定 sleep 猜测启动结果。
	ln, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", srv.Addr, err)
	}
	var metricsServer *http.Server
	var metricsListener net.Listener
	if config.Observability.Metrics.Enabled {
		metricsServer = newMetricsServer(config.Observability.Metrics)
		metricsListener, err = net.Listen("tcp", metricsServer.Addr)
		if err != nil {
			_ = ln.Close()
			return fmt.Errorf("listen for metrics on %s: %w", metricsServer.Addr, err)
		}
	}
	httpSrvMu.Lock()
	httpSrv = srv
	metricsSrv = metricsServer
	done := make(chan struct{})
	serveDone = done
	httpSrvMu.Unlock()
	var serveWG sync.WaitGroup
	serveWG.Add(1)
	go func() {
		defer serveWG.Done()
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Errorf("http server serve error: %v", err)
		}
	}()
	if metricsServer != nil {
		serveWG.Add(1)
		go func() {
			defer serveWG.Done()
			if err := metricsServer.Serve(metricsListener); err != nil && err != http.ErrServerClosed {
				log.Errorf("metrics server serve error: %v", err)
			}
		}()
	}
	go func() {
		serveWG.Wait()
		close(done)
	}()
	return nil
}

func newMetricsServer(config conf.Metrics) *http.Server {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/metrics" {
			http.NotFound(w, r)
			return
		}
		if !metricsClientAllowed(r, config.Allowlist) {
			http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
			return
		}
		if !metricsAuthorized(r, config.BearerToken) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="metrics"`)
			http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
			return
		}
		relay.RefreshHealthMetrics()
		metrics.Handler().ServeHTTP(w, r)
	})
	return &http.Server{
		Addr:              net.JoinHostPort(config.Host, fmt.Sprintf("%d", config.Port)),
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
}

func metricsClientAllowed(request *http.Request, allowlist []string) bool {
	if len(allowlist) == 0 {
		return true
	}
	if request == nil {
		return false
	}
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err != nil {
		host = request.RemoteAddr
	}
	client, err := netip.ParseAddr(strings.Trim(host, "[]"))
	if err != nil {
		return false
	}
	client = client.Unmap()
	for _, entry := range allowlist {
		entry = strings.TrimSpace(entry)
		if prefix, prefixErr := netip.ParsePrefix(entry); prefixErr == nil {
			if prefix.Addr().Unmap().BitLen() == client.BitLen() && prefix.Contains(client) {
				return true
			}
			continue
		}
		allowed, addressErr := netip.ParseAddr(strings.Trim(entry, "[]"))
		if addressErr == nil && allowed.Unmap() == client {
			return true
		}
	}
	return false
}

func metricsAuthorized(request *http.Request, token string) bool {
	if token == "" {
		return true
	}
	if request == nil {
		return false
	}
	provided := request.Header.Get("Authorization")
	want := "Bearer " + token
	return len(provided) == len(want) && subtle.ConstantTimeCompare([]byte(provided), []byte(want)) == 1
}

func Close() error {
	httpSrvMu.Lock()
	srv := httpSrv
	metricsServer := metricsSrv
	done := serveDone
	httpSrvMu.Unlock()
	if srv == nil && metricsServer == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var appErr, metricsErr error
	if srv != nil {
		appErr = srv.Shutdown(ctx)
	}
	if metricsServer != nil {
		metricsErr = metricsServer.Shutdown(ctx)
	}
	var waitErr error
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			waitErr = ctx.Err()
		}
	}
	httpSrvMu.Lock()
	if httpSrv == srv {
		httpSrv = nil
	}
	if metricsSrv == metricsServer {
		metricsSrv = nil
	}
	if serveDone == done {
		serveDone = nil
	}
	httpSrvMu.Unlock()
	return errors.Join(appErr, metricsErr, waitErr)
}

func registerRelayRoutes(r *gin.Engine) {
	v1 := r.Group("/v1", middleware.APIKeyAuth())
	v1.POST("/chat/completions", middleware.RequireJSON(), relay.Handler(llm.APIFormatOpenAIChatCompletion))
	v1.POST("/responses", middleware.RequireJSON(), relay.Handler(llm.APIFormatOpenAIResponse))
	v1.POST("/responses/compact", middleware.RequireJSON(), relay.Handler(llm.APIFormatOpenAIResponseCompact))
	v1.POST("/messages", middleware.RequireJSON(), relay.Handler(llm.APIFormatAnthropicMessage))
	v1.POST("/embeddings", middleware.RequireJSON(), relay.Handler(llm.APIFormatOpenAIEmbedding))
	v1.POST("/images/generations", middleware.RequireJSON(), relay.Handler(llm.APIFormatOpenAIImageGeneration))
	v1.POST("/images/edits", middleware.RequireImageMultipart(), relay.Handler(llm.APIFormatOpenAIImageEdit))
	v1.POST("/images/variations", middleware.RequireImageMultipart(), relay.Handler(llm.APIFormatOpenAIImageVariation))

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
