//go:build linux

package cli

import "os"

// platformRoot Linux 下返回是否以 root 运行。
func platformRoot() bool { return os.Geteuid() == 0 }

// platformTTY Linux 下判定 stdout 是否字符终端（决定是否输出清屏等控制序列）。
func platformTTY() bool {
	st, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return st.Mode()&os.ModeCharDevice != 0
}
