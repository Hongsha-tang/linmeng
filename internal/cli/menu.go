// Package cli 实现「琳萌运维工具」：Linux 终端的交互式数字菜单 CLI。
// 设计稿：_temp_file/运维CLI设计规划260908_v1.3.md（已确认口径：
//  1. 所有输入“输入后回车确认”；2) 配置读写复用 internal/config；
//  3. 本期交付 = CLI 二进制 + 基本自测）。
//
// 平台无关部分（菜单树/直通解析/导航/输入/动作编排）在本目录可直接单测；
// 真实命令（systemctl/journalctl/ufw）由 Executor 在 Linux 上实现。
package cli

import (
	"fmt"
	"io"
	"strings"
)

// CommandKind 区分命令类别：一次性 / 持续（实时跟随日志）。
type CommandKind int

const (
	KindOnce       CommandKind = iota // 执行完返回
	KindPersistent                    // Ctrl+C 结束，返回一级并停留工具
)

// Command 是菜单叶子：一个具体可执行命令。
type Command struct {
	Label    string // 中文名称（菜单展示）
	NeedRoot bool   // 是否要求 root（改配置/启停/重启/防火墙/更新回滚）
	Kind     CommandKind
	Run      func() error // 实际执行；错误仅用于直通退出码
}

// Menu 是一层目录：标题 + 编号条目（自上而下从 1 开始；0 = 返回/关闭）。
type Menu struct {
	Title string
	// Entries 索引 i 对应编号 i+1；内容为子目录或命令。
	Entries []*Entry
}

// Entry 是 Menu 中的一项。
type Entry struct {
	Num int      // 展示编号（1..len）
	Sub *Menu    // 非 nil 表示子目录
	Cmd *Command // 非 nil 表示叶子命令
}

// label 返回该条目名称（目录/命令共用）。
func (e *Entry) label() string {
	if e.Sub != nil {
		return e.Sub.Title
	}
	return e.Cmd.Label
}

// isLeaf 是否叶子命令。
func (e *Entry) isLeaf() bool { return e.Cmd != nil }

// buildMenu 由标题与有序条目构造 Menu 并自动编号（1..n）。
func buildMenu(title string, entries []*Entry) *Menu {
	for i, e := range entries {
		e.Num = i + 1
	}
	return &Menu{Title: title, Entries: entries}
}

// printMenu 打印一层目录到指定输出（单测注入 Out 亦可捕获）。
func printMenu(w io.Writer, m *Menu, isRoot bool) {
	fmt.Fprintln(w)
	fmt.Fprintln(w, "【"+m.Title+"】")
	for _, e := range m.Entries {
		fmt.Fprintf(w, "  %d. %s\n", e.Num, e.label())
	}
	if isRoot {
		fmt.Fprintln(w, "  0. 关闭运维工具")
	} else {
		fmt.Fprintln(w, "  0. 返回上一级")
	}
	fmt.Fprintln(w)
}

// findEntryByNum 按用户输入编号取条目；返回 nil 表示越界/非法。
func (m *Menu) findEntryByNum(num int) *Entry {
	if num < 1 || num > len(m.Entries) {
		return nil
	}
	return m.Entries[num-1]
}

// resolvePath 将数字路径（如 ["2","1"]）解析为具体命令。
// 规则（设计稿 §4.1）：
//   - 每个 token 必须为 1..n 的编号；
//   - 中间 token 必须指向子目录；
//   - 末尾 token 必须指向叶子命令（“指向分类而非命令”= 无效）；
//   - 已到叶子后不允许再有多余 token。
func resolvePath(root *Menu, tokens []string) (*Command, error) {
	if len(tokens) == 0 {
		return nil, fmt.Errorf("空路径")
	}
	cur := root
	for i, tok := range tokens {
		num, ok := parseNum(tok)
		if !ok {
			return nil, fmt.Errorf("非数字路径元素 %q", tok)
		}
		e := cur.findEntryByNum(num)
		if e == nil {
			return nil, fmt.Errorf("编号 %d 超出目录范围", num)
		}
		last := i == len(tokens)-1
		if last {
			if !e.isLeaf() {
				return nil, fmt.Errorf("路径指向分类而非命令")
			}
			return e.Cmd, nil
		}
		if e.Sub == nil {
			return nil, fmt.Errorf("路径在命令 %q 处提前结束", e.Cmd.Label)
		}
		cur = e.Sub
	}
	return nil, fmt.Errorf("路径未指向命令")
}

// parseNum 解析非负整数（允许 0；范围由调用方判定）。
func parseNum(s string) (int, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
		if n > 1<<20 {
			return 0, false
		}
	}
	return n, true
}
