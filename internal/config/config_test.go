package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("写临时文件 %s: %v", p, err)
	}
	return p
}

func TestLoadDefaultsWhenSettingMissing(t *testing.T) {
	// .env 提供密码，setting.json 缺失 → 全部使用默认值。
	dir := t.TempDir()
	env := writeFile(t, dir, ".env", "AUTH_PASSWORD=secret\n")

	cfg, err := Load(filepath.Join(dir, "setting.json"), env)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Port != 8002 || cfg.ListenHost != "0.0.0.0" {
		t.Errorf("默认端口/监听地址不符: %+v", cfg)
	}
	if cfg.RefreshIntervalSeconds != 2 || cfg.HistoryPoints != 4 || cfg.SessionTTLMinutes != 120 {
		t.Errorf("默认刷新/曲线/会话参数不符: %+v", cfg)
	}
	if !cfg.AuthEnabled || cfg.AuthPassword != "secret" {
		t.Errorf("鉴权默认开启且应读到密码: %+v", cfg)
	}
	for _, f := range []bool{cfg.EnableCPU, cfg.EnableMemory, cfg.EnableDisk, cfg.EnableNetwork, cfg.EnableHost, cfg.EnableGPU, cfg.EnableProc, cfg.EnableFS} {
		if !f {
			t.Errorf("默认 enable_* 应全部为 true")
		}
	}
}

func TestLoadPartialSettingOverrides(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "setting.json", `{
		"port": 9000,
		"auth_enabled": false,
		"enable_disk": false,
		"unknown_key": "忽略"
	}`)
	cfg, err := Load(filepath.Join(dir, "setting.json"), filepath.Join(dir, ".env"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Port != 9000 || cfg.AuthEnabled || cfg.EnableDisk {
		t.Errorf("覆盖项未生效: %+v", cfg)
	}
	if cfg.EnableCPU != true || cfg.RefreshIntervalSeconds != 2 {
		t.Errorf("未覆盖项应保留默认值: %+v", cfg)
	}
}

func TestLoadAuthEnabledMissingPasswordUsesDefault(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "setting.json", `{"auth_enabled": true}`)
	cfg, err := Load(filepath.Join(dir, "setting.json"), filepath.Join(dir, ".env"))
	if err != nil {
		t.Fatalf("Load 不应报错: %v", err)
	}
	if cfg.AuthPassword != DefaultPassword {
		t.Errorf("未配置密码时应使用出厂默认 %q, got %q", DefaultPassword, cfg.AuthPassword)
	}
	if cfg.EnvPath != filepath.Join(dir, ".env") {
		t.Errorf("EnvPath 应记录 .env 路径: %q", cfg.EnvPath)
	}
}

func TestLoadAuthDisabledNoPasswordOK(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "setting.json", `{"auth_enabled": false}`)
	if _, err := Load(filepath.Join(dir, "setting.json"), filepath.Join(dir, ".env")); err != nil {
		t.Fatalf("关闭鉴权且无密码应可加载: %v", err)
	}
}

func TestUpdateAuthPasswordReplaces(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, ".env", "# 注释保留\nAUTH_PASSWORD=old123\nEXTRA=keep\n")
	if err := UpdateAuthPassword(p, "new-pass"); err != nil {
		t.Fatalf("UpdateAuthPassword: %v", err)
	}
	values, err := parseEnvFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if values["AUTH_PASSWORD"] != "new-pass" {
		t.Errorf("AUTH_PASSWORD 未更新: %v", values)
	}
	if values["EXTRA"] != "keep" {
		t.Errorf("其它键应保留: %v", values)
	}
	data, _ := os.ReadFile(p)
	if !strings.Contains(string(data), "# 注释保留") {
		t.Error("注释行应保留")
	}
}

func TestUpdateAuthPasswordCreatesFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".env")
	if err := UpdateAuthPassword(p, "created-pass"); err != nil {
		t.Fatalf("UpdateAuthPassword: %v", err)
	}
	values, _ := parseEnvFile(p)
	if values["AUTH_PASSWORD"] != "created-pass" {
		t.Errorf("应创建 .env 并写入: %v", values)
	}
	if err := UpdateAuthPassword(p, ""); err == nil {
		t.Error("空密码应报错")
	}
}

func TestUpdateSettingsMerge(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "setting.json", `{"port": 8002, "enable_disk": false, "refresh_interval_seconds": 4}`)
	if err := UpdateSettings(p, map[string]any{
		"history_points":           10,
		"refresh_interval_seconds": 3,
		"enable_disk":              true,
	}); err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}
	cfg, err := Load(p, filepath.Join(dir, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HistoryPoints != 10 || cfg.RefreshIntervalSeconds != 3 || !cfg.EnableDisk {
		t.Errorf("补丁键应生效: %+v", cfg)
	}
	if cfg.Port != 8002 {
		t.Error("未在补丁中的键应保留")
	}
	if err := UpdateSettings(p, nil); err == nil {
		t.Error("空补丁应报错")
	}
}

func TestLoadMalformedSettingFails(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "setting.json", `{bad json`)
	if _, err := Load(filepath.Join(dir, "setting.json"), filepath.Join(dir, ".env")); err == nil {
		t.Fatal("畸形 setting.json 应报错")
	}
}

func TestLoadInvalidPortFails(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "setting.json", `{"auth_enabled": false, "port": 99999}`)
	if _, err := Load(filepath.Join(dir, "setting.json"), filepath.Join(dir, ".env")); err == nil {
		t.Fatal("端口越界应报错")
	}
}

func TestParseEnv(t *testing.T) {
	in := "\ufeff# 注释\nAUTH_PASSWORD= s3cret \n\nEMPTY=\nQUOTED=\"a b\"\nSINGLE='x=y'\nNOSEPARATOR\n"
	got := parseEnv([]byte(in))
	want := map[string]string{
		"AUTH_PASSWORD": "s3cret",
		"EMPTY":         "",
		"QUOTED":        "a b",
		"SINGLE":        "x=y",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("parseEnv[%s] = %q, want %q", k, got[k], v)
		}
	}
	if _, ok := got["NOSEPARATOR"]; ok {
		t.Error("无 = 的行不应被解析")
	}
}
