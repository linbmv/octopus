package cmd

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestStartFailsFastOnBrokenConfig 验证 P0-2 / spec [启动] 规范：
// 配置加载失败时，start 命令必须同步失败并以非零退出，
// 且不得进入信号等待而“假存活”。
//
// 采用子进程方式：编译当前包为二进制，喂入损坏的 config.json 启动，
// 断言进程在超时窗口内自行退出且退出码非零。若进程挂起（超时未退出），
// 说明失败路径仍进入了 shutdown.Listen()，测试判定失败。
func TestStartFailsFastOnBrokenConfig(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping subprocess integration test in -short mode")
	}

	tmp := t.TempDir()

	// 编译二进制（构建的是仓库根的 main 包）。
	bin := filepath.Join(tmp, "octopus-test")
	build := exec.Command("go", "build", "-o", bin, "github.com/bestruirui/octopus")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Fatalf("build test binary: %v", err)
	}

	// 写一个语法损坏的 JSON 配置，触发 conf.Load 返回 error。
	brokenCfg := filepath.Join(tmp, "broken.json")
	if err := os.WriteFile(brokenCfg, []byte("{ this is not valid json "), 0o600); err != nil {
		t.Fatalf("write broken config: %v", err)
	}

	// 给足启动时间；正常失败应远早于此，超时即视为“假存活”。
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, "start", "--config", brokenCfg)
	cmd.Dir = tmp // 隔离工作目录，避免误用仓库的 data/config.json
	out, err := cmd.CombinedOutput()

	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("start hung on broken config instead of exiting (fake-alive); output:\n%s", out)
	}

	// 期望：非零退出。
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected non-zero exit on broken config, got err=%v; output:\n%s", err, out)
	}
	if exitErr.ExitCode() == 0 {
		t.Fatalf("expected non-zero exit code, got 0; output:\n%s", out)
	}

	// 错误应经由结构化日志输出（startup failed）。
	if !strings.Contains(string(out), "startup failed") {
		t.Errorf("expected structured 'startup failed' log in output; got:\n%s", out)
	}
}
