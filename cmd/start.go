package cmd

import (
	"fmt"

	"github.com/bestruirui/octopus/internal/conf"
	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/relay"
	"github.com/bestruirui/octopus/internal/server"
	"github.com/bestruirui/octopus/internal/task"
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
		log.SetLevel(conf.AppConfig.Log.Level)
		return nil
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		shutdown.Init(log.Logger)
		if err := db.InitDB(conf.AppConfig.Database.Type, conf.AppConfig.Database.Path, conf.IsDebug()); err != nil {
			return fmt.Errorf("database init: %w", err)
		}
		shutdown.Register(db.Close)

		if err := op.InitCache(); err != nil {
			return fmt.Errorf("cache init: %w", err)
		}
		shutdown.Register(op.SaveCache)
		op.StartAsyncWorkers()
		shutdown.Register(op.StopAsyncWorkers)

		if err := op.UserInit(); err != nil {
			return fmt.Errorf("user init: %w", err)
		}

		if err := relay.StartHealthPersistence(); err != nil {
			return fmt.Errorf("health persistence init: %w", err)
		}
		shutdown.Register(relay.StopHealthPersistence)

		task.Init()
		shutdown.Register(task.Close)

		if err := server.Start(); err != nil {
			return fmt.Errorf("server start: %w", err)
		}
		shutdown.Register(server.Close)

		go task.RUN()

		// 所有初始化成功后才进入信号等待；任一步骤失败都会在上面 return，
		// 由 cobra 传播到 Execute() 的非零退出，且不会阻塞在此。
		shutdown.Listen()
		return nil
	},
}

func init() {
	startCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default is ./data/config.json)")
	rootCmd.AddCommand(startCmd)
}
