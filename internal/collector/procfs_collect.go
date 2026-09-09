package collector

import (
	"os"
	"runtime"
	"strconv"
	"strings"
)

// collectProc 读取 /proc/loadavg 的 running/total（task 口径，含线程）。
// 非 Linux 或解析失败返回 nil（整模块 null，软失败）。
func collectProc() *ProcInfo {
	running, total, ok := readLoadavgCounts()
	if !ok || total <= 0 {
		return nil
	}
	return &ProcInfo{Total: total, Running: running}
}

// collectFS 读取 /proc/sys/fs/file-nr（已分配/上限）。失败返回 nil。
func collectFS() *FSInfo {
	alloc, limit, ok := readFileNr()
	if !ok {
		return nil
	}
	return &FSInfo{FileDescriptors: alloc, FileDescLimit: limit}
}

// establishedTCPCount 统计 /proc/net/tcp 与 tcp6 中 state=0A（ESTABLISHED）的连接数。
func establishedTCPCount() int64 {
	if runtime.GOOS != "linux" {
		return 0
	}
	var n int64
	for _, p := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		if data, err := os.ReadFile(p); err == nil {
			n += countEstablishedTCP(data)
		}
	}
	return n
}

// countEstablishedTCP 纯函数：按行解析 tcp 表，统计 state 列等于 0A 的行数。
func countEstablishedTCP(data []byte) int64 {
	var n int64
	lines := strings.Split(string(data), "\n")
	for i, line := range lines {
		if i == 0 { // 表头：sl local_address rem_address st ...
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 4 && fields[3] == "0A" {
			n++
		}
	}
	return n
}

// readLoadavgCounts 读取 /proc/loadavg 的 running/total 对。
func readLoadavgCounts() (running, total int64, ok bool) {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, 0, false
	}
	return parseLoadavgCounts(data)
}

// parseLoadavgCounts 纯函数：/proc/loadavg 第 4 段形如 "1/266"（running/total）。
func parseLoadavgCounts(data []byte) (running, total int64, ok bool) {
	fields := strings.Fields(string(data))
	if len(fields) < 4 {
		return 0, 0, false
	}
	parts := strings.SplitN(fields[3], "/", 2)
	if len(parts) != 2 {
		return 0, 0, false
	}
	r, err1 := strconv.ParseInt(parts[0], 10, 64)
	t, err2 := strconv.ParseInt(parts[1], 10, 64)
	if err1 != nil || err2 != nil || t <= 0 || r < 0 {
		return 0, 0, false
	}
	return r, t, true
}

// readFileNr 读取 /proc/sys/fs/file-nr（第 1 列已分配、第 3 列上限）。
func readFileNr() (alloc, limit int64, ok bool) {
	data, err := os.ReadFile("/proc/sys/fs/file-nr")
	if err != nil {
		return 0, 0, false
	}
	return parseFileNr(data)
}

// parseFileNr 纯函数解析 file-nr 内容。
func parseFileNr(data []byte) (alloc, limit int64, ok bool) {
	fields := strings.Fields(string(data))
	if len(fields) < 3 {
		return 0, 0, false
	}
	a, err1 := strconv.ParseInt(fields[0], 10, 64)
	l, err2 := strconv.ParseInt(fields[2], 10, 64)
	if err1 != nil || err2 != nil || a < 0 || l <= 0 {
		return 0, 0, false
	}
	return a, l, true
}
