//go:build !linux

package cli

// platformRoot 非 Linux 平台（如单测/开发机）不判定 root，恒为 false；
// 需 root 命令在测试中经假执行器验证编排而非真实权限。
func platformRoot() bool { return false }

// platformTTY 非 Linux 平台判定终端恒为 false（测试输出为内存缓冲，不清屏）。
func platformTTY() bool { return false }
