package cmd

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/bestruirui/octopus/internal/capability"
	"github.com/bestruirui/octopus/internal/conf"
	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/metrics"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/relay"
	appruntime "github.com/bestruirui/octopus/internal/runtime"
	"github.com/bestruirui/octopus/internal/server"
	"github.com/bestruirui/octopus/internal/task"
	"github.com/bestruirui/octopus/internal/tracing"
	"github.com/bestruirui/octopus/internal/utils/log"
	"github.com/bestruirui/octopus/internal/utils/shutdown"
	"github.com/spf13/cobra"
)

var cfgFile string

var startCmd = &cobra.Command{
	Use:   "start",
	Short: "Start " + conf.APP_NAME,
	PreRunE: func(cmd *cobra.Command, args []string) error {
		conf.PrintBanner()
		if err := conf.Load(cfgFile); err != nil {
			return fmt.Errorf("load config: %w", err)
		}
		config := conf.Current()
		log.Configure(config.Log.Level, config.Log.Format)
		return nil
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		config := conf.Current()
		shutdown.Init(log.Logger)
		if err := db.InitDB(config.Database.Type, config.Database.Path, conf.IsDebug()); err != nil {
			return fmt.Errorf("database init: %w", err)
		}
		shutdown.Register(db.Close)
		sqlDB, err := db.GetDB().DB()
		if err != nil {
			return fmt.Errorf("database metrics init: %w", err)
		}
		if err := metrics.RegisterDB(sqlDB); err != nil {
			return fmt.Errorf("database metrics register: %w", err)
		}

		if err := tracing.Init(cmd.Context(), tracing.Config{
			Enabled:     config.Observability.Tracing.Enabled,
			ServiceName: conf.APP_NAME,
			Endpoint:    config.Observability.Tracing.Endpoint,
			Insecure:    config.Observability.Tracing.Insecure,
			SampleRatio: config.Observability.Tracing.SampleRatio,
		}); err != nil {
			return fmt.Errorf("tracing init: %w", err)
		}
		shutdown.Register(func() error {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			return tracing.Shutdown(ctx)
		})

		if err := op.InitCache(); err != nil {
			return fmt.Errorf("cache init: %w", err)
		}

		if err := op.UserInit(); err != nil {
			return fmt.Errorf("user init: %w", err)
		}

		task.Init()
		runtimeManager := appruntime.NewManager()
		capabilityWorker, err := capability.InstallDefault(config.CapabilityProbe)
		if err != nil {
			return fmt.Errorf("configure capability probe worker: %w", err)
		}
		if err := runtimeManager.Register("capability_probe", capabilityWorker); err != nil {
			return fmt.Errorf("register capability probe worker: %w", err)
		}
		if err := runtimeManager.Register("async_persistence", op.DefaultAsyncWorker()); err != nil {
			return fmt.Errorf("register persistence workers: %w", err)
		}
		if err := runtimeManager.Register("health_persistence", relay.DefaultHealthPersistenceWorker()); err != nil {
			return fmt.Errorf("register health persistence worker: %w", err)
		}
		if err := runtimeManager.Register("scheduled_tasks", task.DefaultRuntimeWorker()); err != nil {
			return fmt.Errorf("register scheduled workers: %w", err)
		}
		shutdown.Register(func() error {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			return runtimeManager.Stop(ctx)
		})
		if err := runtimeManager.Start(cmd.Context()); err != nil {
			return fmt.Errorf("runtime workers start: %w", err)
		}

		if err := server.Start(); err != nil {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			cleanupErr := runtimeManager.Stop(cleanupCtx)
			cancel()
			if cleanupErr != nil {
				return fmt.Errorf("server start: %w; runtime cleanup: %v", err, cleanupErr)
			}
			return fmt.Errorf("server start: %w", err)
		}
		shutdown.Register(server.Close)

		configWatcher, err := conf.Watch(cfgFile)
		if err != nil {
			return fmt.Errorf("config watcher start: %w", err)
		}
		shutdown.Register(configWatcher.Close)
		configUpdates := configWatcher.Subscribe()
		go applyConfigUpdates(cmd.Context(), config, configUpdates)

		// 所有初始化成功后才进入信号等待；任一步骤失败都会在上面 return，
		// 由 cobra 传播到 Execute() 的非零退出，且不会阻塞在此。
		shutdown.Listen()
		return nil
	},
}

func applyConfigUpdates(ctx context.Context, previous conf.Config, updates <-chan conf.Config) {
	for {
		select {
		case <-ctx.Done():
			return
		case config, ok := <-updates:
			if !ok {
				return
			}
			log.SetLevel(config.Log.Level)
			if previous.Log.Format != config.Log.Format {
				log.Warnf("log format changed; restart required for that field")
			}
			if err := tracing.Init(ctx, tracing.Config{
				Enabled:     config.Observability.Tracing.Enabled,
				ServiceName: conf.APP_NAME,
				Endpoint:    config.Observability.Tracing.Endpoint,
				Insecure:    config.Observability.Tracing.Insecure,
				SampleRatio: config.Observability.Tracing.SampleRatio,
			}); err != nil {
				log.Errorf("apply tracing config: %v", err)
			}
			if previous.Server.Host != config.Server.Host ||
				previous.Server.Port != config.Server.Port ||
				previous.Server.SessionCookieSecure != config.Server.SessionCookieSecure ||
				!slices.Equal(previous.Server.TrustedProxies, config.Server.TrustedProxies) ||
				previous.Database != config.Database {
				log.Warnf("server or database config changed; restart required for those fields")
			}
			if !metricsConfigEqual(previous.Observability.Metrics, config.Observability.Metrics) {
				log.Warnf("metrics listener configuration changed; restart required for that field")
			}
			if !webAuthnConfigEqual(previous.WebAuthn, config.WebAuthn) {
				log.Warnf("WebAuthn configuration changed; restart active login ceremonies before relying on the new values")
			}
			if previous.CapabilityProbe != config.CapabilityProbe {
				log.Warnf("capability probe worker configuration changed; restart required for that field")
			}
			log.Infof("config reloaded")
			previous = config
		}
	}
}

func webAuthnConfigEqual(left, right conf.WebAuthn) bool {
	return left.Enabled == right.Enabled &&
		left.RPID == right.RPID &&
		left.RPDisplayName == right.RPDisplayName &&
		slices.Equal(left.RPOrigins, right.RPOrigins)
}

func metricsConfigEqual(left, right conf.Metrics) bool {
	return left.Enabled == right.Enabled &&
		left.Host == right.Host &&
		left.Port == right.Port &&
		left.BearerToken == right.BearerToken &&
		slices.Equal(left.Allowlist, right.Allowlist)
}

func init() {
	startCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default is ./data/config.json)")
	rootCmd.AddCommand(startCmd)
}
