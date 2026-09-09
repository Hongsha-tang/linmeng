//go:build linux

package cli

import (
	"fmt"
	"os"
	"os/exec"
)

// MaybeElevate 非 root 且交互终端调用时，自动以 `sudo <自身> <args>` 重跑一次
// （设计需求：输入 linmeng 默认即拥有 root，免对每条命令单独 sudo）。
// 返回 (true, 退出码) 表示已转交 sudo 执行完毕、调用方应立即退出；
// 返回 (false, 0) 表示无需/无法提升，按普通用户继续运行。
// 逃生口：环境变量 LINMENG_NO_SUDO=1 可禁用自动提升（管道/脚本场景）。
func MaybeElevate(args []string) (bool, int) {
	if platformRoot() {
		return false, 0
	}
	if os.Getenv("LINMENG_NO_SUDO") != "" {
		return false, 0
	}
	// 仅交互终端自动提升，避免脚本/管道中意外触发 sudo 密码提示。
	st, err := os.Stdin.Stat()
	if err != nil || st.Mode()&os.ModeCharDevice == 0 {
		return false, 0
	}
	sudo, err := exec.LookPath("sudo")
	if err != nil {
		fmt.Fprintln(os.Stderr, "提示：非 root 且未找到 sudo；需 root 的命令将提示“请使用 sudo linmeng”。")
		return false, 0
	}
	exe, err := os.Executable()
	if err != nil {
		return false, 0
	}
	argv := append([]string{exe}, args...)
	cmd := exec.Command(sudo, argv...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return true, ee.ExitCode()
		}
		return true, 1
	}
	return true, 0
}
