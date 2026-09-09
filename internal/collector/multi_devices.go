package collector

import (
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

// 多硬盘/多网卡枚举与 diskstats 全量解析。
// 仅在 Linux 有效；其它平台返回空，便于保持跨平台编译。

// isPhysicalDiskName 判断是否为一整块物理块设备名（/sys/block 顶层项），
// 排除 loop/ram/zram 等虚拟设备。
func isPhysicalDiskName(name string) bool {
	for _, p := range []string{"sd", "vd", "xvd", "hd", "fvd", "nvme", "mmcblk", "dm-", "md"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// listBlockDisks 枚举 /sys/block 下的物理块设备名（去重）。
func listBlockDisks() []string {
	if runtime.GOOS != "linux" {
		return nil
	}
	entries, err := os.ReadDir("/sys/block")
	if err != nil {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() && isPhysicalDiskName(n) && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// skipVirtualNetName 判断应排除的虚拟/回环网卡。
func skipVirtualNetName(name string) bool {
	if name == "lo" {
		return true
	}
	for _, p := range []string{
		"veth", "docker", "br-", "virbr", "vboxnet", "vmnet",
		"tun", "tap", "wg", "kube-", "tailscale", "vcan", "zta",
	} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// readDiskStatsAll 一次性读取 /proc/diskstats，返回按设备名索引的采样。
func readDiskStatsAll() map[string]diskSample {
	out := map[string]diskSample{}
	if runtime.GOOS != "linux" {
		return out
	}
	data, err := os.ReadFile("/proc/diskstats")
	if err != nil {
		return out
	}
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		fields := strings.Fields(line)
		// 0,1 major/minor；2 name；3 reads；5 sectors_read；7 writes；
		// 9 sectors_written；12 io_ticks(ms)
		if len(fields) < 14 {
			continue
		}
		var s diskSample
		s.busyMS, _ = strconv.ParseInt(fields[12], 10, 64)
		s.sectorsRead, _ = strconv.ParseUint(fields[5], 10, 64)
		s.sectorsWritten, _ = strconv.ParseUint(fields[9], 10, 64)
		s.reads, _ = strconv.ParseUint(fields[3], 10, 64)
		s.writes, _ = strconv.ParseUint(fields[7], 10, 64)
		out[fields[2]] = s
	}
	return out
}

// visibleNetNames 将 gopsutil 网卡计数排序并过滤虚拟/回环网卡。
func visibleNetNames(counts map[string]struct{ rx, tx uint64 }) []string {
	var names []string
	for n := range counts {
		if !skipVirtualNetName(n) {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return names
}
