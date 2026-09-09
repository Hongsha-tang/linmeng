// 运行编排：入口参数解析（-app-dir / -h / 数字直通路径）、
// 交互式菜单循环（数字 + 回车确认）与直通执行。
package cli

import (
	"fmt"
	"io"
	"os/signal"
	"strings"
)

// Run 是 CLI 主入口（由 cmd/linmeng-cli 调用）。
// 返回值：0=正常退出；1=路径无效/用法错误；2=执行出错。
func (a *App) Run(argv []string) int {
	// 全程捕获 SIGINT：Ctrl+C 不关闭工具，仅结束当前命令/跟随
	// （设计稿 §4.3；菜单输入为“回车确认”，Ctrl+C 后按回车继续）。
	sig := setupInterrupt()
	defer func() {
		signal.Stop(sig)
		close(sig)
	}()

	direct := make([]string, 0, len(argv))
	i := 0
	for i < len(argv) {
		arg := argv[i]
		switch {
		case arg == "-h" || arg == "--help" || arg == "help":
			printUsage(a)
			return 0
		case arg == "-app-dir" || arg == "--app-dir":
			if i+1 >= len(argv) {
				a.outLine("-app-dir 缺少目录参数")
				return 1
			}
			a.Dir = strings.TrimSuffix(argv[i+1], "/")
			i += 2
			continue
		case strings.HasPrefix(arg, "-app-dir="):
			a.Dir = strings.TrimSuffix(strings.TrimPrefix(arg, "-app-dir="), "/")
			i++
			continue
		case strings.HasPrefix(arg, "-"):
			a.outLine("未知参数：" + arg)
			printUsage(a)
			return 1
		default:
			direct = append(direct, arg)
			i++
		}
	}

	root := buildRootMenu(a)
	if len(direct) == 0 {
		return a.interactive(root)
	}

	// 数字直通：解析到具体命令后执行。
	cmd, err := resolvePath(root, direct)
	if err != nil {
		a.outLine("参数路径无效，请用 linmeng 进入菜单：" + err.Error())
		printMenu(a.Out, root, true)
		return 1
	}
	// 直通命令若要求 root 校验失败：直接退出。
	if cmd.NeedRoot {
		if err := a.requireRoot(); err != nil {
			return 2
		}
	}
	if cmd.Run != nil {
		if err := cmd.Run(); err != nil && err != io.EOF {
			a.outLine("执行出错：" + err.Error())
			return 2
		}
	}
	// 持续命令（日志跟随）：Ctrl+C 结束后回到一级并停留工具（设计稿 §4.1）。
	if cmd.Kind == KindPersistent {
		fmt.Fprintln(a.Out)
		a.outLine("已结束实时跟随，进入交互菜单。")
		return a.interactive(root)
	}
	return 0
}

// interactive 交互式数字菜单循环。root 为一级目录。
func (a *App) interactive(root *Menu) int {
	cur := root
	for {
		if cur == nil {
			cur = root
		}
		isRoot := cur == root
		n, err := a.promptNum(cur, isRoot)
		if err == io.EOF {
			a.outLine("")
			return 0 // Ctrl+D 退出
		}
		if err != nil {
			a.outLine("输入读取失败：" + err.Error())
			return 1
		}
		if n == 0 {
			if isRoot {
				return 0 // 关闭运维工具
			}
			cur = root // 返回上一级（仅两级，即回到一级）
			continue
		}
		e := cur.findEntryByNum(n)
		if e == nil {
			a.outLine("输入无效，请重新输入")
			continue
		}
		if e.Sub != nil {
			cur = e.Sub
			continue
		}
		cmd := e.Cmd
		if cmd.NeedRoot {
			if err := a.requireRoot(); err != nil {
				cur = root
				a.waitReturn()
				continue
			}
		}
		if cmd.Run != nil {
			if err := cmd.Run(); err != nil && err != io.EOF {
				a.outLine("执行出错：" + err.Error())
			}
		}
		if cmd.Kind == KindPersistent {
			cur = root // Ctrl+C 结束后回到一级
			continue
		}
		a.waitReturn()
		cur = root // 非持续命令后回到一级
	}
}

// printUsage 打印命令行用法。
func printUsage(a *App) {
	fmt.Fprintln(a.Out, "用法：")
	fmt.Fprintln(a.Out, "  linmeng                 进入交互式数字菜单")
	fmt.Fprintln(a.Out, "  linmeng 2 1             按数字路径直达命令（执行完退出）")
	fmt.Fprintln(a.Out, "  linmeng 2 2            直达实时跟随日志（Ctrl+C 后进入菜单）")
	fmt.Fprintln(a.Out, "  linmeng -h / --help     打印本帮助")
	fmt.Fprintln(a.Out, "  linmeng -app-dir <dir>  覆盖服务安装目录（测试用，默认 "+DefaultAppDir+"）")
	fmt.Fprintln(a.Out, "")
	fmt.Fprintln(a.Out, "运行身份：非 root 且为交互终端时自动以 sudo 重跑（需 root 命令无需单独 sudo）；")
	fmt.Fprintln(a.Out, "         设置环境变量 LINMENG_NO_SUDO=1 可禁用自动提升（脚本/管道场景）。")
	fmt.Fprintln(a.Out, "")
	fmt.Fprintln(a.Out, "一级目录（数字+回车确认；每层显示前自动清屏）：")
	printMenu(a.Out, buildRootMenu(a), true)
}
