// Package collector 汇聚各指标采集动作：按需（收到请求才采集一次）调用
// gopsutil 只读读取系统信息，维护请求间基线以差分计算 CPU% 与网络速率，
// 并尽力而为探测 CPU 当前运行频率（sysfs）。数据模型见
// docs/architecture.md#数据模型（单一信息源）。
package collector

import (
	"bufio"
	"fmt"
	"math"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/host"
	"github.com/shirou/gopsutil/v3/load"
	"github.com/shirou/gopsutil/v3/mem"
	gonet "github.com/shirou/gopsutil/v3/net"

	"linmeng/internal/config"
)

// rootInodeCounts 返回根分区 inode 总量/空闲（statfs）。
// 非 Linux 平台默认不可用，由 linux_inodes.go（build tag=linux）覆盖实现。
var rootInodeCounts = func() (total, free int64, ok bool) { return 0, 0, false }

// Snapshot 一次采集的全部指标集合（接口响应体）。
// 模块指针为 nil 时 JSON 序列化为 null：被 enable_* 关闭或尽力而为无数据源。
type Snapshot struct {
	Timestamp string       `json:"timestamp"`
	Host      *HostInfo    `json:"host"`
	CPU       *CPUInfo     `json:"cpu"`
	Memory    *MemoryInfo  `json:"memory"`
	Disk      *DiskInfo    `json:"disk"`
	Network   *NetworkInfo `json:"network"`
	GPU       *GPUInfo     `json:"gpu"`
	Proc      *ProcInfo    `json:"proc"`
	FS        *FSInfo      `json:"fs"`
}

// HostInfo 主机/系统信息。
type HostInfo struct {
	Hostname      string `json:"hostname"`
	OS            string `json:"os"`
	Kernel        string `json:"kernel"`
	UptimeSeconds int64  `json:"uptime_seconds"`
	LanIP         string `json:"lan_ip"`
}

// CPUInfo CPU 指标。Frequency 尽力而为，无 cpufreq 数据源时为 nil（序列化 null）。
type CPUInfo struct {
	Percent   float64   `json:"percent"`
	LoadAvg   []float64 `json:"load_avg"`
	PerCore   []float64 `json:"per_core"`
	Frequency *float64  `json:"frequency"`
}

// MemoryInfo 内存/交换指标。
// Available/Buff/Cache 为 gopsutil 明细口径（Available≈不含不可回收部分）。
type MemoryInfo struct {
	Total     int64    `json:"total"`
	Used      int64    `json:"used"`
	Percent   float64  `json:"percent"`
	Swap      SwapInfo `json:"swap"`
	Available int64    `json:"available"`
	Buff      int64    `json:"buff"`
	Cache     int64    `json:"cache"`
}

// SwapInfo 交换分区。
type SwapInfo struct {
	Total   int64   `json:"total"`
	Used    int64   `json:"used"`
	Percent float64 `json:"percent"`
}

// DiskInfo 磁盘模块（多物理盘数组）。
// IO 类（io_percent/read|write_rate/read|write_iops）每块物理盘一项（尽力而为）；
// 空间/inode（statfs 口径）仅“包含根分区”的盘项携带（Mount="/"），其余盘项为 0。
type DiskInfo struct {
	Disks []DiskItem `json:"disks"`
}

// DiskItem 单块物理盘。
type DiskItem struct {
	Index         int      `json:"index"`
	Name          string   `json:"name"`  // 块设备名，如 sda / nvme0n1
	Mount         string   `json:"mount"` // 包含根分区时为 "/"，否则为空串
	Total         int64    `json:"total"`
	Used          int64    `json:"used"`
	Percent       float64  `json:"percent"`
	Inodes        int64    `json:"inodes"`
	InodesPercent float64  `json:"inodes_percent"`
	IoPercent     *float64 `json:"io_percent"`
	ReadRate      *float64 `json:"read_rate"`
	WriteRate     *float64 `json:"write_rate"`
	ReadIOPS      *float64 `json:"read_iops"`
	WriteIOPS     *float64 `json:"write_iops"`
}

// NetworkInfo 网络模块：tcp_established 为全机聚合值；
// Nets 为逐网卡数组（每项含各自累计/速率/丢包/错误计数，尽力而为差分速率）。
type NetworkInfo struct {
	TCPEstablished int64     `json:"tcp_established"`
	Nets           []NetItem `json:"nets"`
}

// NetItem 单个网卡。
type NetItem struct {
	Index    int     `json:"index"`
	Name     string  `json:"name"` // 网卡名，如 eth0 / enp3s0
	RXBytes  int64   `json:"rx_bytes"`
	TXBytes  int64   `json:"tx_bytes"`
	RXRate   float64 `json:"rx_rate"`
	TXRate   float64 `json:"tx_rate"`
	RXDrops  int64   `json:"rx_drops"`
	TXDrops  int64   `json:"tx_drops"`
	RXErrors int64   `json:"rx_errors"`
	TXErrors int64   `json:"tx_errors"`
}

// ProcInfo 进程/可调度任务计数（/proc/loadavg running/total，task 口径含线程）。
type ProcInfo struct {
	Total   int64 `json:"total"`
	Running int64 `json:"running"`
}

// FSInfo 文件描述符占用（/proc/sys/fs/file-nr 第 1/3 列）。
type FSInfo struct {
	FileDescriptors int64 `json:"file_descriptors"`
	FileDescLimit   int64 `json:"file_desc_limit"`
}

// GPUInfo GPU 指标（尽力而为，见 gpu.go 与架构文档·决策 D9）：
// 探测源（nvidia-smi / amdgpu sysfs）不可得时为 nil（整模块 null，前端隐藏模块）。
type GPUInfo struct {
	Count   int       `json:"count"`
	Name    []string  `json:"name"`
	Percent float64   `json:"percent"`
	PerGPU  []GPUCard `json:"per_gpu,omitempty"`
}

// GPUCard 逐 GPU 使用率（可选字段，便于逐卡展示）。
type GPUCard struct {
	Index   int     `json:"index"`
	Name    string  `json:"name"`
	Percent float64 `json:"percent"`
}

// Monitor 汇聚各指标采集动作，并在内部保存上次请求的采样基线用于差分。
// 多个请求并发到达时以互斥锁串行化基线读写，避免基线互相污染（决策 D8）。
type Monitor struct {
	cfg *config.Config

	mu sync.Mutex
	// CPU 差分基线：上次请求的各逻辑核计数及其求和。
	cpuPrev      []cpu.TimesStat
	cpuPrevTotal cpu.TimesStat
	// 逐网卡速率差分基线：网卡名 → 上次累计字节；prevAt 为分组时刻。
	netHasBase bool
	netPrev    map[string]netCounterSample
	netPrevAt  time.Time
	// 逐盘 IO 差分基线：块设备名 → 上次 diskstats 采样。
	diskHasBase bool
	diskPrev    map[string]diskSample
	diskPrevAt  time.Time
}

// netCounterSample 单网卡计数快照（速率差分用）。
type netCounterSample struct {
	rx, tx uint64
}

// diskSample 单块盘 /proc/diskstats 单次采样。
type diskSample struct {
	busyMS         int64
	sectorsRead    uint64
	sectorsWritten uint64
	reads          uint64
	writes         uint64
}

// New 创建监视器。cfg 不可为 nil。
func New(cfg *config.Config) *Monitor {
	return &Monitor{cfg: cfg}
}

// Snapshot 按需采集一次完整系统快照。被 enable_* 关闭或尽力而为不可得的
// 模块返回 nil（序列化为 null）；任一启用模块硬失败则返回 error（HTTP 500）。
func (m *Monitor) Snapshot() (Snapshot, error) {
	// 读锁：与“修改配置”写路径互斥（F-003），确保 enable_* 等运行期字段读取一致。
	m.cfg.RLock()
	defer m.cfg.RUnlock()

	now := time.Now()
	snap := Snapshot{Timestamp: now.Format("2006-01-02T15:04:05.000Z07:00")}

	if m.cfg.EnableHost {
		h, err := collectHost()
		if err != nil {
			return snap, err
		}
		snap.Host = &h
	}
	if m.cfg.EnableCPU {
		ci, err := m.collectCPU()
		if err != nil {
			return snap, err
		}
		snap.CPU = &ci
	}
	if m.cfg.EnableMemory {
		mi, err := collectMemory()
		if err != nil {
			return snap, err
		}
		snap.Memory = &mi
	}
	if m.cfg.EnableDisk {
		di, err := m.collectDisk(now)
		if err != nil {
			return snap, err
		}
		snap.Disk = &di
	}
	if m.cfg.EnableNetwork {
		ni, err := m.collectNetwork(now)
		if err != nil {
			return snap, err
		}
		snap.Network = &ni
	}
	// GPU（尽力而为，决策 D9）：enable_gpu 开启时尝试 nvidia-smi / amdgpu sysfs；
	// 无可用数据源时返回 nil（gpu: null，模块整体隐藏），不使整帧 500。
	if m.cfg.EnableGPU {
		snap.GPU = probeGPU()
	}
	// proc/fs（依赖 Linux /proc；非 Linux 或读取失败整模块 null，软失败）。
	if m.cfg.EnableProc {
		snap.Proc = collectProc()
	}
	if m.cfg.EnableFS {
		snap.FS = collectFS()
	}
	return snap, nil
}

func collectHost() (HostInfo, error) {
	var h HostInfo
	st, err := host.Info()
	if err != nil {
		return h, fmt.Errorf("采集主机信息失败: %w", err)
	}
	h.Hostname = st.Hostname
	h.Kernel = st.KernelVersion
	h.UptimeSeconds = int64(st.Uptime)
	if name, ok := readOSPrettyName(); ok {
		h.OS = name
	} else {
		h.OS = strings.TrimSpace(strings.Join([]string{st.Platform, st.PlatformVersion}, " "))
	}
	h.LanIP = firstLANIPv4()
	return h, nil
}

// readOSPrettyName 读取 Linux /etc/os-release 的 PRETTY_NAME（如 "Debian GNU/Linux 12"）。
func readOSPrettyName() (string, bool) {
	f, err := os.Open("/etc/os-release")
	if err != nil {
		return "", false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "PRETTY_NAME=") {
			continue
		}
		v := strings.TrimSpace(strings.TrimPrefix(line, "PRETTY_NAME="))
		if len(v) >= 2 {
			if (v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'') {
				v = v[1 : len(v)-1]
			}
		}
		v = strings.TrimSpace(v)
		if v == "" {
			return "", false
		}
		return v, true
	}
	return "", false
}

// firstLANIPv4 取第一个启用的非回环网卡的 IPv4 地址（跳过链路本地）。
func firstLANIPv4() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			var ip net.IP
			switch v := a.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			ip4 := ip.To4()
			if ip4 == nil || ip4.IsLoopback() || ip4.IsLinkLocalUnicast() {
				continue
			}
			return ip4.String()
		}
	}
	return ""
}

// collectCPU 采集 CPU 总使用率（请求间差分）、load average、逐核使用率与
// 当前运行频率（尽力而为）。首个请求无基线，percent/per_core 置 0 后建基线。
func (m *Monitor) collectCPU() (CPUInfo, error) {
	var ci CPUInfo

	cores, err := cpu.Times(true)
	if err != nil {
		return ci, fmt.Errorf("采集 CPU 计数失败: %w", err)
	}
	if avg, err := load.Avg(); err != nil {
		return ci, fmt.Errorf("采集系统负载失败: %w", err)
	} else {
		ci.LoadAvg = []float64{avg.Load1, avg.Load5, avg.Load15}
	}
	ci.Frequency = currentCPUFreqMHz()

	total := sumTimes(cores)
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.cpuPrev) == len(cores) && len(cores) > 0 {
		ci.Percent = percentDelta(m.cpuPrevTotal, total)
		per := make([]float64, len(cores))
		for i := range cores {
			per[i] = percentDelta(m.cpuPrev[i], cores[i])
		}
		ci.PerCore = per
	} else {
		// 首个请求（或核心数变化）：置 0 并重建基线。
		ci.Percent = 0
		ci.PerCore = make([]float64, len(cores))
	}
	m.cpuPrev = cloneTimes(cores) // 深拷贝基线：防御底层数组被复用导致逐核差分失真
	m.cpuPrevTotal = total
	return ci, nil
}

// currentCPUFreqMHz 尽力而为：读 /sys/devices/system/cpu/cpu*/cpufreq/scaling_cur_freq
// （kHz÷1000→MHz）逐核取算术平均。仅 Linux；数据源不可得时返回 nil（软失败）。
func currentCPUFreqMHz() *float64 {
	if runtime.GOOS != "linux" {
		return nil
	}
	paths, err := filepath.Glob("/sys/devices/system/cpu/cpu[0-9]*/cpufreq/scaling_cur_freq")
	if err != nil || len(paths) == 0 {
		return nil
	}
	var sum float64
	n := 0
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		f, err := strconv.ParseFloat(strings.TrimSpace(string(data)), 64)
		if err != nil || f <= 0 {
			continue
		}
		sum += f / 1000.0
		n++
	}
	if n == 0 {
		return nil
	}
	avg := math.Round(sum/float64(n)*10) / 10
	return &avg
}

func collectMemory() (MemoryInfo, error) {
	var mi MemoryInfo
	vm, err := mem.VirtualMemory()
	if err != nil {
		return mi, fmt.Errorf("采集内存失败: %w", err)
	}
	mi.Total = int64(vm.Total)
	mi.Used = int64(vm.Used)
	mi.Percent = vm.UsedPercent
	mi.Available = int64(vm.Available)
	mi.Buff = int64(vm.Buffers)
	mi.Cache = int64(vm.Cached)

	sm, err := mem.SwapMemory()
	if err != nil {
		return mi, fmt.Errorf("采集交换分区失败: %w", err)
	}
	mi.Swap = SwapInfo{Total: int64(sm.Total), Used: int64(sm.Used), Percent: sm.UsedPercent}
	return mi, nil
}

func (m *Monitor) collectDisk(now time.Time) (DiskInfo, error) {
	us, err := disk.Usage("/")
	if err != nil {
		return DiskInfo{}, fmt.Errorf("采集根分区失败: %w", err)
	}
	rootBase, _ := rootDiskBaseName()
	blocks := orderDisksRootFirst(listBlockDisks(), rootBase)
	if len(blocks) == 0 {
		if rootBase != "" {
			blocks = []string{rootBase} // 枚举兜底（如 dm/md 或受限容器）
		} else {
			// 非 Linux / 根设备不可知：返回空数组 disks:[]，
			// 遵循模块存在性语义（无设备→前端隐藏），不整帧报错。
			return DiskInfo{Disks: []DiskItem{}}, nil
		}
	}

	items := make([]DiskItem, 0, len(blocks))
	samples := readDiskStatsAll()
	for i, b := range blocks {
		it := DiskItem{Index: i, Name: b}
		if b == rootBase {
			it.Mount = "/"
			it.Total = int64(us.Total)
			it.Used = int64(us.Used)
			it.Percent = us.UsedPercent
			// inode（statfs，df -i 同口径：已用 = files − ffree）仅根盘项携带。
			if total, free, ok := rootInodeCounts(); ok && total > 0 {
				used := total - free
				if used < 0 {
					used = 0
				}
				it.Inodes = used
				it.InodesPercent = float64(used) / float64(total) * 100
			}
		}
		items = append(items, it)
	}
	m.applyDiskRates(items, samples, now)
	return DiskInfo{Disks: items}, nil
}

// orderDisksRootFirst 去重并将根盘置为首项（硬盘#0），其余按原序排列。
func orderDisksRootFirst(blocks []string, root string) []string {
	out := make([]string, 0, len(blocks)+1)
	if root != "" {
		out = append(out, root)
	}
	for _, b := range blocks {
		if b != root {
			out = append(out, b)
		}
	}
	return out
}

// applyDiskRates 以逐盘 diskstats 采样做差分，填充 io_percent/read|write_rate/read|write_iops。
// 无该盘数据源→指针 nil（尽力而为）；有采样但无基线（首轮）→差分项为 0 指针。
func (m *Monitor) applyDiskRates(items []DiskItem, samples map[string]diskSample, now time.Time) {
	if len(items) == 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	z := func(v float64) *float64 { return &v }
	if m.diskPrev == nil {
		m.diskPrev = map[string]diskSample{}
	}
	elapsedMS := now.Sub(m.diskPrevAt).Milliseconds()
	elapsedS := now.Sub(m.diskPrevAt).Seconds()
	for i := range items {
		it := &items[i]
		s, ok := samples[it.Name]
		if !ok {
			continue // 尽力而为：无数据源不填（保持 nil）
		}
		ioPct, rdRate, wrRate, rdIOPS, wrIOPS := 0.0, 0.0, 0.0, 0.0, 0.0
		if m.diskHasBase && elapsedMS > 0 && elapsedS > 0 {
			if prev, ok2 := m.diskPrev[it.Name]; ok2 {
				if db := s.busyMS - prev.busyMS; db >= 0 {
					p := float64(db) / float64(elapsedMS) * 100
					if p > 100 {
						p = 100
					}
					ioPct = p
				}
				rdRate = float64(subU64(s.sectorsRead, prev.sectorsRead)) * 512 / elapsedS
				wrRate = float64(subU64(s.sectorsWritten, prev.sectorsWritten)) * 512 / elapsedS
				rdIOPS = float64(subU64(s.reads, prev.reads)) / elapsedS
				wrIOPS = float64(subU64(s.writes, prev.writes)) / elapsedS
			}
		}
		it.IoPercent = z(ioPct)
		it.ReadRate = z(rdRate)
		it.WriteRate = z(wrRate)
		it.ReadIOPS = z(rdIOPS)
		it.WriteIOPS = z(wrIOPS)
		m.diskPrev[it.Name] = s
	}
	m.diskPrevAt = now
	m.diskHasBase = true
}

// subU64 无符号差值；回绕/复位按 0 处理。
func subU64(a, b uint64) uint64 {
	if a < b {
		return 0
	}
	return a - b
}

// rootDiskBaseName 从 /proc/mounts 取根分区设备并归一化为物理磁盘名
// （sda1→sda；nvme0n1p2→nvme0n1；mmcblk0p1→mmcblk0；dm/md 原样保留）。
func rootDiskBaseName() (string, bool) {
	f, err := os.Open("/proc/mounts")
	if err != nil {
		return "", false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 || fields[1] != "/" {
			continue
		}
		dev := fields[0]
		if !strings.HasPrefix(dev, "/dev/") {
			return "", false // overlay / tmpfs 等无对应 diskstats 设备
		}
		return normalizeDiskDevice(dev), true
	}
	return "", false
}

// normalizeDiskDevice 归一化设备名为 diskstats 中的磁盘名。
func normalizeDiskDevice(dev string) string {
	n := strings.TrimPrefix(dev, "/dev/")
	// “p+数字”分区后缀：nvme0n1p2 / mmcblk0p1 / md126p1 → nvme0n1 / mmcblk0 / md126
	if i := strings.LastIndex(n, "p"); i > 0 {
		if allDigits(n[i+1:]) {
			return n[:i]
		}
	}
	// 经典 sd/vd/xvd/hd 系列分区：sda1 → sda
	for _, fam := range []string{"xvd", "vd", "sd", "hd", "fvd"} {
		if strings.HasPrefix(n, fam) {
			return strings.TrimRight(n, "0123456789")
		}
	}
	// dm-0 / md126（无 p 分区）/ mapper/... 等原样保留
	return n
}

// allDigits 判断字符串是否全部为十进制数字（空串为否）。
func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// collectNetwork 采集逐网卡累计计数并差分速率（过滤回环/虚拟网卡）；
// TCP 已建立连接数为全机聚合（tcp_established）。
func (m *Monitor) collectNetwork(now time.Time) (NetworkInfo, error) {
	counters, err := gonet.IOCounters(true)
	if err != nil {
		return NetworkInfo{}, fmt.Errorf("采集网络计数失败: %w", err)
	}
	byName := map[string]gonet.IOCountersStat{}
	var order []string
	for i := range counters {
		c := counters[i]
		if skipVirtualNetName(c.Name) {
			continue
		}
		if _, seen := byName[c.Name]; seen {
			continue
		}
		byName[c.Name] = c
		order = append(order, c.Name)
	}
	sort.Strings(order)

	ni := NetworkInfo{
		TCPEstablished: establishedTCPCount(),
		Nets:           make([]NetItem, 0, len(order)),
	}
	if len(order) == 0 {
		return ni, nil // 仅回环/虚拟卡时 nets 为空，模块仍存在（含聚合 TCP）
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.netPrev == nil {
		m.netPrev = map[string]netCounterSample{}
	}
	elapsed := now.Sub(m.netPrevAt).Seconds()
	for idx, nm := range order {
		c := byName[nm]
		it := NetItem{
			Index:    idx,
			Name:     nm,
			RXBytes:  int64(c.BytesRecv),
			TXBytes:  int64(c.BytesSent),
			RXDrops:  int64(c.Dropin),
			TXDrops:  int64(c.Dropout),
			RXErrors: int64(c.Errin),
			TXErrors: int64(c.Errout),
		}
		if m.netHasBase && elapsed > 0 {
			if p, ok := m.netPrev[nm]; ok {
				if d := int64(c.BytesRecv) - int64(p.rx); d >= 0 {
					it.RXRate = float64(d) / elapsed
				}
				if d := int64(c.BytesSent) - int64(p.tx); d >= 0 {
					it.TXRate = float64(d) / elapsed
				}
			}
		}
		m.netPrev[nm] = netCounterSample{rx: c.BytesRecv, tx: c.BytesSent}
		ni.Nets = append(ni.Nets, it)
	}
	m.netPrevAt = now
	m.netHasBase = true
	return ni, nil
}

// cloneTimes 深拷贝采样切片，避免基线切片与 gopsutil 返回缓冲共享底层数组。
func cloneTimes(cores []cpu.TimesStat) []cpu.TimesStat {
	out := make([]cpu.TimesStat, len(cores))
	copy(out, cores)
	return out
}

// sumTimes 将各逻辑核的计数按字段求和，等价于 /proc/stat 的整体 CPU 计数。
func sumTimes(cores []cpu.TimesStat) cpu.TimesStat {
	var t cpu.TimesStat
	t.CPU = "cpu-total"
	for _, c := range cores {
		t.User += c.User
		t.System += c.System
		t.Idle += c.Idle
		t.Nice += c.Nice
		t.Iowait += c.Iowait
		t.Irq += c.Irq
		t.Softirq += c.Softirq
		t.Steal += c.Steal
	}
	return t
}

// timesTotal 计数总耗时（含 idle 的全部字段）。
func timesTotal(t cpu.TimesStat) float64 {
	return t.User + t.System + t.Nice + t.Iowait + t.Irq + t.Softirq + t.Steal + t.Idle
}

// percentDelta 依据两次采样差分计算使用率百分比：delta_busy / delta_total，
// 结果截断到 0-100。两次采样间隔为 0 时返回 0。
func percentDelta(prev, cur cpu.TimesStat) float64 {
	dTotal := timesTotal(cur) - timesTotal(prev)
	if dTotal <= 0 {
		return 0
	}
	dIdle := cur.Idle - prev.Idle
	p := (1 - dIdle/dTotal) * 100
	if p < 0 {
		return 0
	}
	if p > 100 {
		return 100
	}
	return p
}
