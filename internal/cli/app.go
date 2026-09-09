// 应用层：组装 App（路径常量/执行器/输入输出）、构建菜单树、
// 交互式导航（数字 + 回车确认）与一次性数字路径直通。
package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
)

// 部署路径常量（设计稿 §5；-app-dir 可覆盖便于测试）。
const (
	DefaultAppDir = "/opt/linmeng"
	DefaultSvc    = "linmeng"
	snapshotRel   = "linmeng-cache/snapshot.json"
	settingFile   = "setting.json"
	envFile       = ".env"
	serviceBin    = "linmeng"
	backupBin     = "linmeng.bak"
)

// App 是运维工具的运行时上下文。
type App struct {
	Dir     string // 服务安装目录（/opt/linmeng，可用 -app-dir 覆盖）
	Svc     string // systemd 单元名（linmeng）
	Version string
	Exec    Executor
	In      io.Reader
	Out     io.Writer
	Err     io.Writer
	in      *bufio.Reader // 输入缓冲（跨多次 readLine 保持，避免预读丢失）
	root    bool          // 是否以 root 运行（Linux 下 os.Geteuid()==0）
	tty     bool          // stdout 是否终端（是才输出清屏等控制序列）
}

// NewApp 构造工具上下文。
func NewApp(dir, svc, version string, exec Executor) *App {
	if dir == "" {
		dir = DefaultAppDir
	}
	if svc == "" {
		svc = DefaultSvc
	}
	a := &App{
		Dir:     dir,
		Svc:     svc,
		Version: version,
		Exec:    exec,
		In:      os.Stdin,
		Out:     os.Stdout,
		Err:     os.Stderr,
	}
	a.in = bufio.NewReader(a.In)
	a.root = platformRoot()
	a.tty = platformTTY()
	return a
}

// path 拼接服务目录下的相对路径。
func (a *App) path(rel string) string {
	if a.Dir == "" {
		return rel
	}
	return a.Dir + "/" + rel
}

// snapshotPath 快照缓存文件绝对路径。
func (a *App) snapshotPath() string { return a.path(snapshotRel) }
func (a *App) settingsPath() string { return a.path(settingFile) }
func (a *App) envPath() string      { return a.path(envFile) }

// requireRoot 校验 root：不足时提示“请使用 sudo linmeng”并取消本次命令。
func (a *App) requireRoot() error {
	if a.root {
		return nil
	}
	fmt.Fprintln(a.Out, "本命令需要 root 权限，请使用 sudo linmeng 执行。")
	return errNeedRoot
}

// errNeedRoot 标记“因权限不足取消”，供交互层判断是否等待回车。
var errNeedRoot = fmt.Errorf("need root")

// readLine 读取一行输入（回车确认），去除首尾空白。EOF 返回 io.EOF。
func (a *App) readLine(prompt string) (string, error) {
	if a.in == nil {
		a.in = bufio.NewReader(a.In)
	}
	if prompt != "" {
		fmt.Fprint(a.Out, prompt)
	}
	line, err := a.in.ReadString('\n')
	if err != nil && len(line) == 0 {
		return "", err
	}
	return strings.TrimSpace(strings.TrimSuffix(line, "\n")), nil
}

// promptNum 读取一个目录编号（1..n 或 0），非法则提示重输（回车确认）。
func (a *App) promptNum(menu *Menu, isRoot bool) (int, error) {
	// 每次显示菜单前清屏，保证终端只保留当前层内容（Q-05）。
	a.clear()
	printMenu(a.Out, menu, isRoot)
	for {
		line, err := a.readLine("请输入数字编号（0 = 返回/关闭）：")
		if err != nil {
			if err == io.EOF {
				return 0, err
			}
			fmt.Fprintln(a.Out, "输入无效，请重新输入")
			continue
		}
		n, ok := parseNum(line)
		if !ok || n < 0 || n > len(menu.Entries) {
			fmt.Fprintln(a.Out, "输入无效，请重新输入")
			continue
		}
		return n, nil
	}
}

// waitReturn 非持续命令结束后提示“按回车返回主菜单”（设计稿 §4.2，回车确认口径）。
// 回车后立即清屏一次，确保命令输出/旧菜单不残留，再由下一轮菜单重绘。
func (a *App) waitReturn() {
	_, _ = a.readLine("按回车返回主菜单...")
	a.clear()
}

// outLine 输出一行到 stdout。
func (a *App) outLine(s string) { fmt.Fprintln(a.Out, s) }

// clear 清屏（ANSI：清可视区 2J + 清回滚缓冲 3J + 光标归位 H）。
// 仅在 stdout 是终端时输出控制序列（避免污染管道/重定向输出）。
// 每次渲染菜单/提示前调用，保证当前层独占屏幕（Q-05：界面刷新、不残留）。
func (a *App) clear() {
	if !a.tty {
		return
	}
	fmt.Fprint(a.Out, "\x1b[H\x1b[2J\x1b[3J")
}

// setupInterrupt 注册 SIGINT 捕获（关闭时自动恢复）。
func setupInterrupt() chan os.Signal {
	ch := make(chan os.Signal, 4)
	signal.Notify(ch, os.Interrupt)
	return ch
}
