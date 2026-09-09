// Package config 负责读取 setting.json（非机密可调参数）与 .env（机密 AUTH_PASSWORD）。
// 配置项全集见 docs/architecture.md#配置表（单一信息源）。
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
)

// Config 是 setting.json + .env 合并后的运行配置。
// 注意：含内部读写锁，必须始终以 *Config（指针）使用，禁止按值拷贝。
type Config struct {
	mu                     sync.RWMutex // 保护运行期可变的字段（密码/刷新间隔/曲线点数/模块开关）
	RefreshIntervalSeconds int          `json:"refresh_interval_seconds"`
	HistoryPoints          int          `json:"history_points"`
	Port                   int          `json:"port"`
	ListenHost             string       `json:"listen_host"`
	AuthEnabled            bool         `json:"auth_enabled"`
	SessionTTLMinutes      int          `json:"session_ttl_minutes"`
	EnableCPU              bool         `json:"enable_cpu"`
	EnableMemory           bool         `json:"enable_memory"`
	EnableDisk             bool         `json:"enable_disk"`
	EnableNetwork          bool         `json:"enable_network"`
	EnableHost             bool         `json:"enable_host"`
	EnableGPU              bool         `json:"enable_gpu"`
	EnableProc             bool         `json:"enable_proc"`
	EnableFS               bool         `json:"enable_fs"`

	// AuthPassword 来自 .env（AUTH_PASSWORD），机密字段，不参与 setting.json 解析。
	AuthPassword string `json:"-"`
	// EnvPath .env 文件路径（由 Load 记录），供运行期“修改密码”持久化回写使用。
	EnvPath string `json:"-"`
	// SettingPath setting.json 文件路径（由 Load 记录），供运行期“修改刷新时间”回写使用。
	SettingPath string `json:"-"`
}

// DefaultPassword 出厂默认登录密码（可在页面“修改密码”中更改，改动回写 .env 持久化）。
const DefaultPassword = "admin123"

// Lock/Unlock 供“修改密码/修改配置”写路径使用（F-003：与后台采集/登录比对隔离）。
func (c *Config) Lock()   { c.mu.Lock() }
func (c *Config) Unlock() { c.mu.Unlock() }

// RLock/RUnlock 供后台采集、登录比对、页面配置注入等读路径使用。
func (c *Config) RLock()   { c.mu.RLock() }
func (c *Config) RUnlock() { c.mu.RUnlock() }

// Default 返回全部默认值（与文档配置表一致）。
func Default() *Config {
	return &Config{
		RefreshIntervalSeconds: 2,
		HistoryPoints:          4,
		Port:                   8002,
		ListenHost:             "0.0.0.0",
		AuthEnabled:            true,
		SessionTTLMinutes:      120,
		EnableCPU:              true,
		EnableMemory:           true,
		EnableDisk:             true,
		EnableNetwork:          true,
		EnableHost:             true,
		EnableGPU:              true,
		EnableProc:             true,
		EnableFS:               true,
	}
}

// Load 读取并合并配置。setting.json 缺失时使用默认值（非机密、有合理缺省）；
// 其它读取/解析错误直接返回。.env 缺失视为未设置机密。
func Load(settingPath, envPath string) (*Config, error) {
	cfg := Default()

	if data, err := os.ReadFile(settingPath); err == nil {
		if err := json.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("解析 %s 失败: %w", settingPath, err)
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("读取 %s 失败: %w", settingPath, err)
	}

	values, err := parseEnvFile(envPath)
	if err != nil {
		return nil, err
	}
	if pw, ok := values["AUTH_PASSWORD"]; ok {
		cfg.AuthPassword = pw
	}
	if cfg.AuthEnabled && cfg.AuthPassword == "" {
		// 鉴权开启但未配置密码时使用出厂默认密码（登录后可在页面修改并持久化）。
		cfg.AuthPassword = DefaultPassword
	}
	cfg.EnvPath = envPath
	cfg.SettingPath = settingPath

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// UpdateSettings 将补丁键合并回写 setting.json（保留其它配置键；文件不存在则创建）。
// 调用方负责业务取值校验；补丁值需为 JSON 可序列化的 int/bool。
func UpdateSettings(settingPath string, patch map[string]any) error {
	if len(patch) == 0 {
		return fmt.Errorf("无有效配置项")
	}
	raw := map[string]any{}
	if data, err := os.ReadFile(settingPath); err == nil {
		if err := json.Unmarshal(data, &raw); err != nil {
			return fmt.Errorf("解析 %s 失败: %w", settingPath, err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("读取 %s 失败: %w", settingPath, err)
	}
	for k, v := range patch {
		raw[k] = v
	}
	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(settingPath, append(out, '\n'), 0o644)
}

// UpdateAuthPassword 将新密码回写 .env（保留其它行与注释；文件不存在则创建）。
// 供运行期“修改密码”接口持久化调用。
func UpdateAuthPassword(envPath, password string) error {
	if password == "" {
		return fmt.Errorf("密码不能为空")
	}
	var out []string
	replaced := false
	if data, err := os.ReadFile(envPath); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "AUTH_PASSWORD=") {
				out = append(out, "AUTH_PASSWORD="+password)
				replaced = true
				continue
			}
			out = append(out, line)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("读取 %s 失败: %w", envPath, err)
	}
	if !replaced {
		out = append(out, "AUTH_PASSWORD="+password)
	}
	return os.WriteFile(envPath, []byte(strings.Join(out, "\n")+"\n"), 0o600)
}

func (c *Config) validate() error {
	switch {
	case c.Port < 1 || c.Port > 65535:
		return fmt.Errorf("配置非法: port=%d 超出范围 1-65535", c.Port)
	case c.RefreshIntervalSeconds <= 0:
		return fmt.Errorf("配置非法: refresh_interval_seconds 必须大于 0")
	case c.HistoryPoints <= 0:
		return fmt.Errorf("配置非法: history_points 必须大于 0")
	case c.SessionTTLMinutes <= 0:
		return fmt.Errorf("配置非法: session_ttl_minutes 必须大于 0")
	case c.AuthEnabled && strings.TrimSpace(c.AuthPassword) == "":
		return fmt.Errorf("配置非法: 鉴权开启但访问密码为空")
	}
	return nil
}

// parseEnvFile 读取 .env 文件；文件不存在时返回空表（不视为错误）。
func parseEnvFile(path string) (map[string]string, error) {
	values := map[string]string{}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return values, nil
		}
		return nil, fmt.Errorf("读取 %s 失败: %w", path, err)
	}
	return parseEnv(data), nil
}

// parseEnv 解析 KEY=VALUE 行：忽略空行与 # 注释，容忍首行 BOM、
// 键值两侧空白与包裹值的单/双引号。可测试的纯函数。
func parseEnv(data []byte) map[string]string {
	values := map[string]string{}
	text := strings.TrimPrefix(string(data), "\ufeff")
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.Index(line, "=")
		if eq <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		if key == "" {
			continue
		}
		val := strings.TrimSpace(line[eq+1:])
		values[key] = unquote(val)
	}
	return values
}

// unquote 去除包裹值的成对单引号或双引号。
func unquote(v string) string {
	if len(v) >= 2 {
		if (v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'') {
			return v[1 : len(v)-1]
		}
	}
	return v
}
