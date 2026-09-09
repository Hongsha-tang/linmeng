//go:build !linux

package cli

// MaybeElevate 非 Linux 平台（单测/开发机）不做自动提权。
func MaybeElevate(args []string) (bool, int) { return false, 0 }
