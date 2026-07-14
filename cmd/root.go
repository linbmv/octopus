package cmd

import (
	"os"

	"github.com/bestruirui/octopus/internal/conf"
	"github.com/bestruirui/octopus/internal/utils/log"
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   conf.APP_NAME,
	Short: conf.APP_DESC,
	// 启动失败时错误已通过结构化日志输出，无需 cobra 再打印 Error/usage。
	SilenceUsage:  true,
	SilenceErrors: true,
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		log.Errorf("startup failed: %v", err)
		os.Exit(1)
	}
}
