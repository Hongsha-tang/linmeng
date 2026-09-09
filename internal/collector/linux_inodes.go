//go:build linux

package collector

import "golang.org/x/sys/unix"

// linuxRootInodeCounts 通过 statfs("/") 读取根分区 inode 总量与空闲数
// （已用 = Files − Ffree，df -i 同口径）。
func linuxRootInodeCounts() (total, free int64, ok bool) {
	var st unix.Statfs_t
	if err := unix.Statfs("/", &st); err != nil {
		return 0, 0, false
	}
	return int64(st.Files), int64(st.Ffree), true
}

func init() {
	rootInodeCounts = linuxRootInodeCounts
}
