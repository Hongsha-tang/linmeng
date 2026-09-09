// 命令执行层：Executor 把 systemctl / journalctl / ufw 封装为可替换接口，
// 便于在单测中以假执行器验证动作编排；真实执行在 Linux 下由 systemExecutor 完成。
package cli

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
)

// Executor 抽象对外部运维命令的调用（动作层只依赖本接口）。
type Executor interface {
	// Status 查看服务状态（等价 systemctl status，原样透传）。
	Status() error
	// Start/Stop/Restart 服务生命周期（需 root）。
	Start() error
	Stop() error
	Restart() error
	// LogRecent 查看最近 n 行日志（journalctl -n n --no-pager）。
	LogRecent(n int) error
	// LogFollow 实时跟随日志（journalctl -f，原样输出）；
	// Ctrl+C（SIGINT）结束跟随并返回，由调用方决定后续导航。
	LogFollow() error
	// UFWAvailable 探测 ufw 是否可用。
	UFWAvailable() bool
	// UFWAllow 放行端口（ufw allow <port>/tcp）。
	UFWAllow(port int) error
}

// systemExecutor 在目标机（Linux）上的真实实现。
type systemExecutor struct {
	svc string // systemd 单元名，如 linmeng
}

// NewSystemExecutor 构造真实执行器。
func NewSystemExecutor(svc string) Executor {
	return &systemExecutor{svc: svc}
}

func (x *systemExecutor) run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (x *systemExecutor) Status() error  { return x.run("systemctl", "status", x.svc, "--no-pager") }
func (x *systemExecutor) Start() error   { return x.run("systemctl", "start", x.svc) }
func (x *systemExecutor) Stop() error    { return x.run("systemctl", "stop", x.svc) }
func (x *systemExecutor) Restart() error { return x.run("systemctl", "restart", x.svc) }

func (x *systemExecutor) LogRecent(n int) error {
	return x.run("journalctl", "-u", x.svc, "-n", strconv.Itoa(n), "--no-pager")
}

// LogFollow 实时跟随：父进程与 journalctl 同处前台进程组，
// Ctrl+C 会同时送达两者；父进程捕获 SIGINT（不默认退出），
// journalctl 收到后自行退出，Run 返回即视为“跟随结束”。
func (x *systemExecutor) LogFollow() error {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	defer func() {
		signal.Stop(sig)
		close(sig)
	}()
	cmd := exec.Command("journalctl", "-u", x.svc, "-f")
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		if err, ok := err.(*exec.ExitError); ok {
			_ = err // journalctl 以非零退出（如被 Ctrl+C 打断）时不视为工具错误
			return nil
		}
		return err
	}
	return nil
}

func (x *systemExecutor) UFWAvailable() bool {
	if _, err := exec.LookPath("ufw"); err != nil {
		return false
	}
	return true
}

func (x *systemExecutor) UFWAllow(port int) error {
	return x.run("ufw", "allow", fmt.Sprintf("%d/tcp", port))
}
