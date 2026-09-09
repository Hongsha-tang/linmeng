// 配置命令与快照摘要的基本自测（复用 internal/config 的真实文件操作）。
package cli

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFile 写测试文件。
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newTestAppIn 以指定目录构造 App（输入注入便于命令内 readLine）。
func newTestAppIn(dir, input string, exec Executor) (*App, *bytes.Buffer) {
	out := &bytes.Buffer{}
	a := &App{
		Dir:     dir,
		Svc:     "linmeng",
		Version: "test",
		Exec:    exec,
		In:      strings.NewReader(input),
		Out:     out,
		Err:     &bytes.Buffer{},
		root:    true,
	}
	a.in = bufio.NewReader(a.In)
	return a, out
}

func TestCmdConfigModifyRuntimeKey(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "setting.json"),
		`{"refresh_interval_seconds": 2, "history_points": 20, "port": 8002}`)
	writeFile(t, filepath.Join(dir, ".env"), "AUTH_PASSWORD=abc123\n")

	a, out := newTestAppIn(dir, "refresh_interval_seconds\n3\n", &fakeExec{})
	a.cmdConfigModify()

	data, _ := os.ReadFile(filepath.Join(dir, "setting.json"))
	if !strings.Contains(string(data), `"refresh_interval_seconds": 3`) {
		t.Fatalf("刷新间隔应回写为 3，文件内容：%s", data)
	}
	if !strings.Contains(out.String(), "配置已写入") {
		t.Error("应提示写入成功")
	}
}

func TestCmdConfigModifyInvalidValue(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "setting.json"),
		`{"refresh_interval_seconds": 2, "port": 8002}`)
	writeFile(t, filepath.Join(dir, ".env"), "")

	a, out := newTestAppIn(dir, "refresh_interval_seconds\n99\n", &fakeExec{})
	a.cmdConfigModify()

	if !strings.Contains(out.String(), "取值范围 1–5") {
		t.Error("越界值应提示取值范围，输出：" + out.String())
	}
	data, _ := os.ReadFile(filepath.Join(dir, "setting.json"))
	if strings.Contains(string(data), `"refresh_interval_seconds": 99`) {
		t.Error("越界值不应被写入")
	}
}

func TestCmdConfigModifyDeployKeyHint(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "setting.json"),
		`{"refresh_interval_seconds": 2, "port": 8002, "listen_host": "0.0.0.0"}`)
	writeFile(t, filepath.Join(dir, ".env"), "AUTH_PASSWORD=abc123\n")

	a, out := newTestAppIn(dir, "port\n9000\n", &fakeExec{})
	a.cmdConfigModify()
	if !strings.Contains(out.String(), "重启服务后生效") {
		t.Error("部署级键应提示重启生效，输出：" + out.String())
	}
	data, _ := os.ReadFile(filepath.Join(dir, "setting.json"))
	if !strings.Contains(string(data), `"port": 9000`) {
		t.Fatalf("port 应回写 9000：%s", data)
	}
}

func TestCmdConfigPassword(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "setting.json"), `{"port": 8002}`)
	writeFile(t, filepath.Join(dir, ".env"), "AUTH_PASSWORD=old\n")

	a, _ := newTestAppIn(dir, "newpw123\nnewpw123\n", &fakeExec{})
	a.cmdConfigPassword()

	data, _ := os.ReadFile(filepath.Join(dir, ".env"))
	if !strings.Contains(string(data), "AUTH_PASSWORD=newpw123") {
		t.Fatalf("密码应回写 .env：%s", data)
	}
}

func TestCmdConfigViewMasked(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "setting.json"),
		`{"refresh_interval_seconds": 2, "history_points": 20, "port": 8002}`)
	writeFile(t, filepath.Join(dir, ".env"), "AUTH_PASSWORD=supersecret\n")

	a, out := newTestAppIn(dir, "", &fakeExec{})
	a.cmdConfigView()

	if strings.Contains(out.String(), "supersecret") {
		t.Fatal("查看配置不得泄露明文密码")
	}
	if !strings.Contains(out.String(), "AUTH_PASSWORD") || !strings.Contains(out.String(), "已设置") {
		t.Error("应显示密码掩码提示，输出：" + out.String())
	}
}

func TestCmdSnapshotSummary(t *testing.T) {
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "linmeng-cache")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(cacheDir, "snapshot.json"), `{
  "timestamp": "2026-09-08T18:00:00.000+08:00",
  "host": {"hostname": "vm-01", "os": "Debian GNU/Linux 12", "kernel": "6.1.0", "uptime_seconds": 3600, "lan_ip": "192.168.1.5"},
  "cpu": {"percent": 12.3, "load_avg": [0.1, 0.2, 0.3], "per_core": [1.0, 2.0], "frequency": 2400.0},
  "memory": {"total": 8589934592, "used": 4294967296, "percent": 50.0, "swap": {"total": 1073741824, "used": 0, "percent": 0.0}, "available": 2147483648},
  "disk": {"disks": [{"index": 0, "name": "sda", "mount": "/", "total": 107374182400, "used": 53687091200, "percent": 50.0, "io_percent": 5.2}]},
  "network": {"tcp_established": 42, "nets": [{"index": 0, "name": "eth0", "rx_rate": 1024.0, "tx_rate": 512.0}]}
}`)

	a, out := newTestAppIn(dir, "", &fakeExec{})
	if err := a.cmdSnapshotSummary(); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	for _, want := range []string{"vm-01", "CPU", "内存", "硬盘#0", "sda", "网络#0", "eth0", "TCP 已建连接（聚合）：42"} {
		if !strings.Contains(s, want) {
			t.Errorf("快照摘要应包含 %q，输出：\n%s", want, s)
		}
	}
}

func TestCmdSnapshotSummaryMissingFile(t *testing.T) {
	a, out := newTestAppIn(t.TempDir(), "", &fakeExec{})
	if err := a.cmdSnapshotSummary(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "未找到快照文件") {
		t.Error("缺文件应友好提示")
	}
}
