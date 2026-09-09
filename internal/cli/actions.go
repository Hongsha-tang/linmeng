// 具体命令动作：服务生命周期 / 日志 / 配置(复用 internal/config) /
// 系统信息 / 防火墙 / 更新回滚 / 工具信息。
package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"linmeng/internal/config"
)

// 配置键类型表：CLI 识别并可读/改的 setting.json 键。
type keyKind int

const (
	kindInt keyKind = iota
	kindBool
	kindStr
	kindMasked // .env AUTH_PASSWORD（仅读掩码/经改密命令写）
)

type keySpec struct {
	name string
	kind keyKind
	// deploy 部署级键：修改后需重启服务生效。
	deploy bool
	// lo/hi int 范围（可选）。
	lo, hi int
	label  string // 中文说明
}

var keyTable = []keySpec{
	{name: "refresh_interval_seconds", kind: kindInt, lo: 1, hi: 5, label: "轮询刷新间隔（秒，1–5）"},
	{name: "history_points", kind: kindInt, lo: 2, hi: 60, label: "短期曲线点数（2–60）"},
	{name: "port", kind: kindInt, lo: 1, hi: 65535, deploy: true, label: "监听端口（部署级，重启生效）"},
	{name: "listen_host", kind: kindStr, deploy: true, label: "监听地址（部署级，重启生效）"},
	{name: "auth_enabled", kind: kindBool, deploy: true, label: "鉴权总开关（部署级，重启生效）"},
	{name: "session_ttl_minutes", kind: kindInt, lo: 1, hi: 10080, deploy: true, label: "会话超时分钟（部署级，重启生效）"},
	{name: "enable_cpu", kind: kindBool, label: "CPU 模块开关"},
	{name: "enable_memory", kind: kindBool, label: "内存模块开关"},
	{name: "enable_disk", kind: kindBool, label: "硬盘模块开关"},
	{name: "enable_network", kind: kindBool, label: "网络模块开关"},
	{name: "enable_host", kind: kindBool, label: "系统信息模块开关"},
	{name: "enable_gpu", kind: kindBool, label: "GPU 模块开关"},
	{name: "enable_proc", kind: kindBool, label: "进程模块开关"},
	{name: "enable_fs", kind: kindBool, label: "文件描述符模块开关"},
}

func findKey(name string) (keySpec, bool) {
	for _, k := range keyTable {
		if k.name == name {
			return k, true
		}
	}
	return keySpec{}, false
}

// parseKeyValue 按键类型解析用户输入的新值。
func parseKeyValue(spec keySpec, raw string) (any, error) {
	switch spec.kind {
	case kindInt:
		n, ok := parseNum(raw)
		if !ok {
			return nil, fmt.Errorf("请输入整数")
		}
		if n < spec.lo || n > spec.hi {
			return nil, fmt.Errorf("取值范围 %d–%d", spec.lo, spec.hi)
		}
		return n, nil
	case kindBool:
		switch strings.ToLower(strings.TrimSpace(raw)) {
		case "true", "1", "yes", "开", "on":
			return true, nil
		case "false", "0", "no", "关", "off":
			return false, nil
		}
		return nil, fmt.Errorf("请输入 true/false（或 1/0）")
	case kindStr:
		if strings.TrimSpace(raw) == "" {
			return nil, fmt.Errorf("值不能为空")
		}
		return strings.TrimSpace(raw), nil
	}
	return nil, fmt.Errorf("不支持的键类型")
}

// ---------- 配置查看（脱敏） ----------

// cmdConfigView 查看当前配置：输出全部 setting.json 键值 + 密码掩码。
func (a *App) cmdConfigView() error {
	raw := map[string]any{}
	if data, err := os.ReadFile(a.settingsPath()); err == nil {
		if err := json.Unmarshal(data, &raw); err != nil {
			return fmt.Errorf("解析配置失败: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("读取配置失败: %w", err)
	}
	fmt.Fprintln(a.Out, "当前配置（setting.json）：")
	for _, k := range keyTable {
		v, present := raw[k.name]
		if !present {
			v = "（未设置，采用默认）"
		}
		if k.name == "listen_host" {
			fmt.Fprintf(a.Out, "  %-24s = %v   （%s）\n", k.name, strconv.Quote(fmt.Sprint(v)), k.label)
			continue
		}
		fmt.Fprintf(a.Out, "  %-24s = %v   （%s）\n", k.name, fmt.Sprint(v), k.label)
	}
	fmt.Fprintln(a.Out, "  AUTH_PASSWORD             = "+a.passwordMask()+"   （访问密码，脱敏）")
	return nil
}

// passwordMask 返回 .env 密码掩码：未设置/默认显示提示，不泄露明文。
func (a *App) passwordMask() string {
	vals, err := readEnvFile(a.envPath())
	if err != nil {
		return "（读取失败）"
	}
	pw, set := vals["AUTH_PASSWORD"]
	if !set || strings.TrimSpace(pw) == "" {
		return "（未设置，将使用出厂默认）"
	}
	return "********（已设置）"
}

// cmdConfigGetKey 查看单个配置项（AUTH_PASSWORD 脱敏）。
func (a *App) cmdConfigGetKey() error {
	name, err := a.readLine("请输入配置键名（如 refresh_interval_seconds）：")
	if err != nil {
		return err
	}
	spec, ok := findKey(name)
	if name == "AUTH_PASSWORD" {
		fmt.Fprintln(a.Out, "AUTH_PASSWORD = "+a.passwordMask()+"   （访问密码，脱敏）")
		return nil
	}
	if !ok {
		fmt.Fprintln(a.Out, "未知配置键。可用键：")
		for _, k := range keyTable {
			fmt.Fprintf(a.Out, "  %s —— %s\n", k.name, k.label)
		}
		fmt.Fprintln(a.Out, "  AUTH_PASSWORD —— 访问密码（脱敏显示）")
		return nil
	}
	raw := map[string]any{}
	if data, err := os.ReadFile(a.settingsPath()); err == nil {
		_ = json.Unmarshal(data, &raw)
	}
	val := raw[spec.name]
	if val == nil {
		fmt.Fprintf(a.Out, "%s = （未设置，使用默认值）  %s\n", spec.name, spec.label)
		return nil
	}
	fmt.Fprintf(a.Out, "%s = %v  %s\n", spec.name, val, spec.label)
	if spec.deploy {
		fmt.Fprintln(a.Out, "提示：该键为部署级，重启服务后生效。")
	}
	return nil
}

// cmdConfigModify 修改配置项（复用 internal/config.UpdateSettings）。
func (a *App) cmdConfigModify() error {
	if err := a.requireRoot(); err != nil {
		return err
	}
	name, err := a.readLine("请输入要修改的配置键名：")
	if err != nil {
		return err
	}
	spec, ok := findKey(name)
	if !ok {
		fmt.Fprintln(a.Out, "未知配置键；输入 linmeng 进入菜单“3-2 查看单个配置项”可见可用键。")
		return nil
	}
	rawVal, err := a.readLine("请输入新值（" + keyHint(spec) + "）：")
	if err != nil {
		return err
	}
	val, err := parseKeyValue(spec, rawVal)
	if err != nil {
		fmt.Fprintln(a.Out, "值无效："+err.Error())
		return nil
	}
	if err := config.UpdateSettings(a.settingsPath(), map[string]any{spec.name: val}); err != nil {
		return fmt.Errorf("写入配置失败: %w", err)
	}
	// 回读校验（复用 internal/config.Load 的范围校验）。
	if _, err := config.Load(a.settingsPath(), a.envPath()); err != nil {
		fmt.Fprintf(a.Out, "已写入但配置校验告警：%v（请尽快修正或回滚）\n", err)
		return nil
	}
	fmt.Fprintf(a.Out, "配置已写入 setting.json：%s = %v\n", spec.name, val)
	if spec.deploy {
		fmt.Fprintln(a.Out, "注意：该键为部署级，需重启服务后生效（菜单 1-4 重启）。")
	} else {
		fmt.Fprintln(a.Out, "注意：运行中的服务读取内存配置；如需立即生效，请重启服务或在页面“修改配置”调整。")
	}
	return nil
}

func keyHint(spec keySpec) string {
	switch spec.kind {
	case kindInt:
		if spec.hi > 0 {
			return fmt.Sprintf("整数 %d–%d", spec.lo, spec.hi)
		}
		return "整数"
	case kindBool:
		return "true/false 或 1/0"
	case kindStr:
		return "字符串"
	}
	return ""
}

// cmdConfigPassword 修改登录密码（复用 internal/config.UpdateAuthPassword）。
func (a *App) cmdConfigPassword() error {
	if err := a.requireRoot(); err != nil {
		return err
	}
	pw1, err := a.readLine("请输入新密码：")
	if err != nil {
		return err
	}
	pw2, err := a.readLine("请再次输入新密码：")
	if err != nil {
		return err
	}
	if pw1 == "" {
		fmt.Fprintln(a.Out, "密码不能为空")
		return nil
	}
	if pw1 != pw2 {
		fmt.Fprintln(a.Out, "两次输入不一致")
		return nil
	}
	if err := config.UpdateAuthPassword(a.envPath(), pw1); err != nil {
		return fmt.Errorf("写入密码失败: %w", err)
	}
	fmt.Fprintln(a.Out, "访问密码已写入 .env。")
	fmt.Fprintln(a.Out, "注意：运行中的服务使用内存密码，需重启服务后生效（菜单 1-4 重启）；页面“修改密码”亦可即时生效。")
	return nil
}

// readEnvFile 读取 .env 为键值表（本地实现，避免改动 config 包导出面）。
func readEnvFile(path string) (map[string]string, error) {
	out := map[string]string{}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.Index(line, "=")
		if eq <= 0 {
			continue
		}
		k := strings.TrimSpace(line[:eq])
		v := strings.TrimSpace(line[eq+1:])
		if len(v) >= 2 && ((v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'')) {
			v = v[1 : len(v)-1]
		}
		out[k] = v
	}
	return out, nil
}

// ---------- 服务文件更新 / 回滚 ----------

// installFile 以“临时文件 + rename”覆盖安装（避免直接覆盖运行中二进制）。
func installFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	tmp := dst + ".tmp-cli"
	if err := os.WriteFile(tmp, data, 0o755); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// serviceBinary 服务二进制路径（<app-dir>/linmeng）。
func (a *App) serviceBinary() string { return a.path(serviceBin) }

func (a *App) backupBinary() string { return a.path(backupBin) }

// cmdUpdate 更新服务：备份当前 → 覆盖新二进制 → 重启。
func (a *App) cmdUpdate() error {
	if err := a.requireRoot(); err != nil {
		return err
	}
	src, err := a.readLine("请输入新二进制绝对路径：")
	if err != nil {
		return err
	}
	if strings.TrimSpace(src) == "" {
		fmt.Fprintln(a.Out, "路径不能为空")
		return nil
	}
	abs, err := filepath.Abs(src)
	if err != nil {
		return fmt.Errorf("路径非法: %w", err)
	}
	if st, err := os.Stat(abs); err != nil || st.IsDir() {
		fmt.Fprintln(a.Out, "新二进制不存在或不可读："+abs)
		return nil
	}
	bin := a.serviceBinary()
	if _, err := os.Stat(bin); err == nil {
		if err := copyFile(bin, a.backupBinary()); err != nil {
			return fmt.Errorf("备份当前二进制失败: %w", err)
		}
		fmt.Fprintln(a.Out, "已备份当前二进制 → "+a.backupBinary())
	}
	if err := installFile(abs, bin); err != nil {
		return fmt.Errorf("安装新二进制失败: %w", err)
	}
	fmt.Fprintln(a.Out, "已覆盖安装 → "+bin)
	fmt.Fprintln(a.Out, "正在重启服务...")
	if err := a.Exec.Restart(); err != nil {
		fmt.Fprintln(a.Out, "重启失败（服务可能未在运行，可尝试菜单 1-2 启动）："+err.Error())
		return nil
	}
	fmt.Fprintln(a.Out, "服务已重启。")
	return nil
}

// cmdRollback 回滚：用 linmeng.bak 覆盖 → 重启。
func (a *App) cmdRollback() error {
	if err := a.requireRoot(); err != nil {
		return err
	}
	bak := a.backupBinary()
	if _, err := os.Stat(bak); err != nil {
		fmt.Fprintln(a.Out, "未找到备份 "+bak+"，无法回滚。")
		return nil
	}
	if err := copyFile(bak, a.serviceBinary()); err != nil {
		return fmt.Errorf("回滚覆盖失败: %w", err)
	}
	fmt.Fprintln(a.Out, "已从备份恢复 → "+a.serviceBinary())
	fmt.Fprintln(a.Out, "正在重启服务...")
	if err := a.Exec.Restart(); err != nil {
		fmt.Fprintln(a.Out, "重启失败（服务可能未在运行，可尝试菜单 1-2 启动）："+err.Error())
		return nil
	}
	fmt.Fprintln(a.Out, "服务已重启。")
	return nil
}

// ---------- 防火墙 ----------

// cmdFirewallDefault 放行默认端口（读取配置 port，缺省 8002）。
func (a *App) cmdFirewallDefault() error {
	return a.firewallAllow("默认端口")
}

// cmdFirewallCustom 放行自定义端口。
func (a *App) cmdFirewallCustom() error {
	return a.firewallAllow("自定义端口")
}

func (a *App) firewallAllow(label string) error {
	if err := a.requireRoot(); err != nil {
		return err
	}
	if !a.Exec.UFWAvailable() {
		fmt.Fprintln(a.Out, "未检测到 ufw，跳过（本工具防火墙功能仅支持 ufw）。")
		return nil
	}
	port := 8002
	if label == "自定义端口" {
		line, err := a.readLine("请输入端口号（1–65535）：")
		if err != nil {
			return err
		}
		n, ok := parseNum(line)
		if !ok || n < 1 || n > 65535 {
			fmt.Fprintln(a.Out, "端口号无效")
			return nil
		}
		port = n
	} else if c, err := config.Load(a.settingsPath(), a.envPath()); err == nil {
		port = c.Port
	}
	fmt.Fprintf(a.Out, "执行：ufw allow %d/tcp\n", port)
	if err := a.Exec.UFWAllow(port); err != nil {
		fmt.Fprintln(a.Out, "放行失败："+err.Error())
		return nil
	}
	fmt.Fprintf(a.Out, "已放行 %d/tcp。\n", port)
	return nil
}

// ---------- 工具信息 ----------

func (a *App) cmdVersion() error {
	fmt.Fprintf(a.Out, "琳萌运维工具（linmeng-cli）版本：%s\n", a.Version)
	fmt.Fprintf(a.Out, "服务目录：%s    服务名：%s\n", a.Dir, a.Svc)
	return nil
}

func (a *App) cmdHelp() error {
	fmt.Fprintln(a.Out, "琳萌运维工具（linmeng-cli）：")
	fmt.Fprintln(a.Out, "  · 输入 linmeng 进入数字菜单；以数字+回车逐层选择，每层显示前自动清屏。")
	fmt.Fprintln(a.Out, "  · 支持一行直达：linmeng 2 1 等价于“日志→最近 200 行”；非持续命令执行完即退出。")
	fmt.Fprintln(a.Out, "  · 实时跟随日志（2-2）以 Ctrl+C 结束并返回一级目录；其余输入以回车确认。")
	fmt.Fprintln(a.Out, "  · 非 root 交互终端下自动以 sudo 运行，启停/重启/改配置等无需单独 sudo；")
	fmt.Fprintln(a.Out, "    脚本/管道场景可设 LINMENG_NO_SUDO=1 禁用自动提升。")
	fmt.Fprintln(a.Out, "  · 服务安装目录默认 /opt/linmeng；可用 -app-dir 覆盖（测试用）。")
	fmt.Fprintln(a.Out, "  · 更多见 docs/deployment.md。")
	return nil
}
