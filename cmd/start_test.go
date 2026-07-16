package cmd

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bestruirui/octopus/internal/conf"
)

// buildTestBinary 编译仓库根 main 包为临时二进制，返回其路径。
func buildTestBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "octopus-test")
	build := exec.Command("go", "build", "-o", bin, "github.com/bestruirui/octopus")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Fatalf("build test binary: %v", err)
	}
	return bin
}

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

	bin := buildTestBinary(t)
	tmp := filepath.Dir(bin)

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

// TestStartFailsFastOnPortInUse 验证 P0-4 / spec [启动] 规范：
// 端口被占用时，server.Start 的 net.Listen 应同步返回绑定错误，
// 启动链据此非零退出，而不是靠 100ms sleep 猜测或挂起。
func TestStartFailsFastOnPortInUse(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping subprocess integration test in -short mode")
	}

	bin := buildTestBinary(t)
	tmp := filepath.Dir(bin)

	// 抢占一个端口，让子进程绑定必失败。
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("occupy port: %v", err)
	}
	t.Cleanup(func() {
		if err := ln.Close(); err != nil {
			t.Errorf("close occupied listener: %v", err)
		}
	})
	port := ln.Addr().(*net.TCPAddr).Port

	// 写一份合法配置：host/port 指向被占端口，DB 用临时 sqlite。
	cfg := map[string]any{
		"server":   map[string]any{"host": "127.0.0.1", "port": port},
		"database": map[string]any{"type": "sqlite", "path": filepath.Join(tmp, "test.db")},
		"log":      map[string]any{"level": "info"},
	}
	cfgBytes, _ := json.Marshal(cfg)
	cfgPath := filepath.Join(tmp, "config.json")
	if err := os.WriteFile(cfgPath, cfgBytes, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, "start", "--config", cfgPath)
	cmd.Dir = tmp
	out, err := cmd.CombinedOutput()

	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("start hung on port-in-use instead of exiting; output:\n%s", out)
	}

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected non-zero exit on port-in-use, got err=%v; output:\n%s", err, out)
	}
	if exitErr.ExitCode() == 0 {
		t.Fatalf("expected non-zero exit code, got 0; output:\n%s", out)
	}
	if !strings.Contains(string(out), "startup failed") {
		t.Errorf("expected structured 'startup failed' log; got:\n%s", out)
	}
}

func TestMetricsConfigEqualIncludesAllowlist(t *testing.T) {
	left := conf.Metrics{Enabled: true, Host: "127.0.0.1", Port: 9090, BearerToken: strings.Repeat("test-", 4), Allowlist: []string{"127.0.0.1"}}
	right := left
	right.Allowlist = append([]string(nil), left.Allowlist...)
	if !metricsConfigEqual(left, right) {
		t.Fatal("equivalent metrics configurations were not equal")
	}
	right.Allowlist = []string{"10.0.0.0/8"}
	if metricsConfigEqual(left, right) {
		t.Fatal("different metrics allowlists were considered equal")
	}
}

func TestWebAuthnConfigEqualIncludesOrigins(t *testing.T) {
	left := conf.WebAuthn{Enabled: true, RPID: "example.com", RPDisplayName: "Octopus", RPOrigins: []string{"https://example.com"}}
	right := left
	right.RPOrigins = append([]string(nil), left.RPOrigins...)
	if !webAuthnConfigEqual(left, right) {
		t.Fatal("equivalent WebAuthn configurations were not equal")
	}
	right.RPOrigins = []string{"https://admin.example.com"}
	if webAuthnConfigEqual(left, right) {
		t.Fatal("different WebAuthn origins were considered equal")
	}
}
