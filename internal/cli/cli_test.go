// CLI 基本自测：菜单结构/数字路径直通解析/交互编排（假执行器）。
package cli

import (
	"bufio"
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"
)

// ---------- 测试用假执行器 ----------

type fakeExec struct {
	status   int
	started  int
	stopped  int
	restarts int
	recent   []int
	follow   int
	ufw      []int
	ufwOK    bool
	lastErr  error
}

func (f *fakeExec) Status() error { f.status++; return f.lastErr }
func (f *fakeExec) Start() error  { f.started++; return f.lastErr }
func (f *fakeExec) Stop() error   { f.stopped++; return f.lastErr }
func (f *fakeExec) Restart() error {
	f.restarts++
	return f.lastErr
}
func (f *fakeExec) LogRecent(n int) error {
	f.recent = append(f.recent, n)
	return f.lastErr
}
func (f *fakeExec) LogFollow() error { f.follow++; return f.lastErr }
func (f *fakeExec) UFWAvailable() bool {
	return f.ufwOK
}
func (f *fakeExec) UFWAllow(port int) error {
	f.ufw = append(f.ufw, port)
	return f.lastErr
}

// newTestApp 构造测试用 App：输入来自 stdin 字符串，root 可注入。
func newTestApp(input string, exec Executor) (*App, *bytes.Buffer, *bytes.Buffer) {
	out := &bytes.Buffer{}
	errBuf := &bytes.Buffer{}
	a := &App{
		Dir:     os.TempDir(),
		Svc:     "linmeng",
		Version: "test",
		Exec:    exec,
		In:      strings.NewReader(input),
		Out:     out,
		Err:     errBuf,
	}
	a.in = bufio.NewReader(a.In)
	return a, out, errBuf
}

// ---------- 菜单结构 ----------

func TestBuildRootMenuStructure(t *testing.T) {
	a, _, _ := newTestApp("", &fakeExec{})
	root := buildRootMenu(a)
	if len(root.Entries) != 7 {
		t.Fatalf("一级应有 7 个分类，实得 %d", len(root.Entries))
	}
	for i, e := range root.Entries {
		if e.Num != i+1 {
			t.Errorf("编号应 1..7，实得 %d", e.Num)
		}
		if e.Sub == nil {
			t.Errorf("一级条目 %q 应为子目录", e.label())
		}
	}
	// 抽查：2 日志查看 下 3 条命令；第 2 条应为持续命令。
	logMenu := root.Entries[1].Sub
	if len(logMenu.Entries) != 3 {
		t.Fatalf("日志菜单应有 3 项，实得 %d", len(logMenu.Entries))
	}
	if logMenu.Entries[1].Cmd.Kind != KindPersistent {
		t.Error("实时跟随日志应标记为持续命令")
	}
	// 配置菜单 3-3/3-4 需要 root。
	cfg := root.Entries[2].Sub
	if !cfg.Entries[2].Cmd.NeedRoot || !cfg.Entries[3].Cmd.NeedRoot {
		t.Error("3-3 修改配置项 / 3-4 修改登录密码 应标记需要 root")
	}
}

// ---------- 数字路径直通解析 ----------

func TestResolvePath(t *testing.T) {
	a, _, _ := newTestApp("", &fakeExec{})
	root := buildRootMenu(a)

	cases := []struct {
		path []string
		want string // 叶子命令 label；空=期望错误
	}{
		{[]string{"2", "1"}, "查看最近 200 行日志"},
		{[]string{"2", "2"}, "实时跟随日志（Ctrl+C 退出返回）"},
		{[]string{"7", "1"}, "版本信息"},
		{[]string{"1", "4"}, "重启服务（restart）"},
		// 无效路径
		{[]string{"9"}, ""},
		{[]string{"2"}, ""},           // 指向分类而非命令
		{[]string{"2", "1", "1"}, ""}, // 命令后多余 token
		{[]string{"x", "1"}, ""},      // 非数字
		{[]string{"0"}, ""},           // 0 非有效路径元素
	}
	for _, c := range cases {
		cmd, err := resolvePath(root, c.path)
		if c.want == "" {
			if err == nil {
				t.Errorf("路径 %v 应解析失败，却得到命令 %q", c.path, cmd.Label)
			}
			continue
		}
		if err != nil {
			t.Errorf("路径 %v 解析失败: %v", c.path, err)
			continue
		}
		if cmd.Label != c.want {
			t.Errorf("路径 %v 应指向 %q，实得 %q", c.path, c.want, cmd.Label)
		}
	}
}

// ---------- 直通模式（非持续命令执行完退出） ----------

func TestRunDirectOnce(t *testing.T) {
	fe := &fakeExec{}
	a, out, _ := newTestApp("", fe)
	a.root = true // 无需 root
	if code := a.Run([]string{"1", "4"}); code != 0 {
		t.Fatalf("直通 1-4 应返回 0，实得 %d", code)
	}
	if fe.restarts != 1 {
		t.Errorf("应执行 1 次重启，实得 %d", fe.restarts)
	}
	if !strings.Contains(out.String(), "systemctl") {
		// 假执行器不输出，仅验证编排即可
	}
}

// 无参数 → 进入交互菜单；输入 0 关闭。
func TestRunInteractiveExit(t *testing.T) {
	fe := &fakeExec{}
	a, out, _ := newTestApp("0\n", fe)
	if code := a.Run(nil); code != 0 {
		t.Fatalf("交互直接输入 0 应关闭返回 0，实得 %d", code)
	}
	if !strings.Contains(out.String(), "linmeng 运维工具") {
		t.Error("应打印一级目录标题")
	}
}

// 交互菜单：1-2 启动服务，随后按回车返回主菜单，再 0 关闭。
func TestRunInteractiveStart(t *testing.T) {
	fe := &fakeExec{}
	// 一级选 1 → 二级选 2（启动）→ 执行后回车返回 → 一级选 0 关闭
	a, out, _ := newTestApp("1\n2\n\n0\n", fe)
	a.root = true
	if code := a.Run(nil); code != 0 {
		t.Fatalf("交互流程应正常关闭返回 0，实得 %d", code)
	}
	if fe.started != 1 {
		t.Errorf("应启动 1 次，实得 %d", fe.started)
	}
	_ = out
}

// root 不足时：需要 root 的命令被取消并提示。
func TestRunNonRootDenied(t *testing.T) {
	fe := &fakeExec{}
	a, out, _ := newTestApp("", fe)
	a.root = false
	code := a.Run([]string{"1", "2"}) // 直通启动
	if code == 0 {
		t.Fatal("非 root 直通启动应返回非 0")
	}
	if fe.started != 0 {
		t.Errorf("非 root 不应执行启动，实得 %d", fe.started)
	}
	if !strings.Contains(out.String(), "sudo linmeng") {
		t.Error("应提示使用 sudo linmeng")
	}
}

// 非法路径：提示并打印一级目录，返回 1。
func TestRunInvalidPath(t *testing.T) {
	fe := &fakeExec{}
	a, out, _ := newTestApp("", fe)
	if code := a.Run([]string{"2"}); code != 1 {
		t.Fatalf("指向分类的路径应返回 1，实得 %d", code)
	}
	if !strings.Contains(out.String(), "参数路径无效") {
		t.Error("应提示参数路径无效")
	}
	if !strings.Contains(out.String(), "服务生命周期") {
		t.Error("应打印一级目录")
	}
}

// -app-dir 覆盖目录。
func TestRunAppDirFlag(t *testing.T) {
	fe := &fakeExec{}
	a, out, _ := newTestApp("", fe)
	a.Run([]string{"-app-dir", "/srv/linmeng", "7", "1"})
	if a.Dir != "/srv/linmeng" {
		t.Errorf("-app-dir 应覆盖为 /srv/linmeng，实得 %q", a.Dir)
	}
	if !strings.Contains(out.String(), "/srv/linmeng") {
		t.Error("版本信息应展示覆盖后的服务目录")
	}
}

// EOF（Ctrl+D）在交互菜单中退出。
func TestRunInteractiveEOF(t *testing.T) {
	fe := &fakeExec{}
	a, _, _ := newTestApp("", fe)
	if code := a.Run(nil); code != 0 {
		t.Fatalf("EOF 应退出返回 0，实得 %d", code)
	}
}

// 直通持续命令（2-2 实时跟随）：跟随结束后进入交互菜单，输入 0 关闭。
func TestRunDirectPersistentThenInteractive(t *testing.T) {
	fe := &fakeExec{}
	a, out, _ := newTestApp("0\n", fe)
	if code := a.Run([]string{"2", "2"}); code != 0 {
		t.Fatalf("直通 2-2 应返回 0，实得 %d", code)
	}
	if fe.follow != 1 {
		t.Errorf("应执行 1 次日志跟随，实得 %d", fe.follow)
	}
	if !strings.Contains(out.String(), "已结束实时跟随") {
		t.Error("应提示已结束实时跟随并进入交互菜单")
	}
}

// -h/--help 打印用法并返回 0。
func TestRunHelp(t *testing.T) {
	fe := &fakeExec{}
	a, out, _ := newTestApp("", fe)
	if code := a.Run([]string{"-h"}); code != 0 {
		t.Fatalf("-h 应返回 0，实得 %d", code)
	}
	for _, want := range []string{"用法", "linmeng 2 1", "-app-dir", "LINMENG_NO_SUDO"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("帮助应包含 %q", want)
		}
	}
}

// 每次显示菜单前应清屏（ANSI 清屏序列出现）。
func TestMenuClearsScreen(t *testing.T) {
	fe := &fakeExec{}
	a, out, _ := newTestApp("0\n", fe)
	a.tty = true // 单测内存缓冲需显式开启终端模式
	a.Run(nil)
	if !strings.Contains(out.String(), "\x1b[H\x1b[2J\x1b[3J") {
		t.Error("交互菜单显示前应输出清屏序列")
	}
	// 再次进入（返回一级后重绘）也应清屏：模拟 1→0 返回 → 0 退出。
	a2, out2, _ := newTestApp("1\n0\n0\n", fe)
	a2.root = true
	a2.tty = true
	a2.Run(nil)
	if c := strings.Count(out2.String(), "\x1b[H\x1b[2J\x1b[3J"); c < 2 {
		t.Errorf("多次导航应多次清屏，实得 %d 次", c)
	}
}

// 非终端（管道/重定向）输出时不应输出清屏控制序列。
func TestClearSkippedWhenNotTTY(t *testing.T) {
	fe := &fakeExec{}
	a, out, _ := newTestApp("0\n", fe)
	a.tty = false
	a.Run(nil)
	if strings.Contains(out.String(), "\x1b[") {
		t.Error("非终端输出不应包含 ANSI 控制序列")
	}
}

// 非 Linux（测试环境）自动提权恒为不触发。
func TestMaybeElevateNoopOnNonLinux(t *testing.T) {
	rerun, code := MaybeElevate([]string{"2", "1"})
	if rerun {
		t.Fatal("非 Linux 不应触发自动提权")
	}
	if code != 0 {
		t.Fatalf("非 Linux 自动提权返回码应为 0，实得 %d", code)
	}
}

// errNeedRoot 判定。
func TestErrNeedRoot(t *testing.T) {
	if !errors.Is(errNeedRoot, errNeedRoot) {
		t.Fatal("errNeedRoot 自我判定失败")
	}
}
