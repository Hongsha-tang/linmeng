package collector

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/shirou/gopsutil/v3/cpu"
)

// 测试差分纯函数，避免依赖平台/真机采集（Windows 开发机亦可运行）。

func TestPercentDelta(t *testing.T) {
	// 采样1：总 1000，其中 idle 900（busy 100）
	prev := cpu.TimesStat{User: 50, System: 50, Idle: 900}
	// 采样2：总 1090，其中 idle 970（busy 120）
	cur := cpu.TimesStat{User: 60, System: 60, Idle: 970}
	// 增量：total=90，busy=20 → 22.22%
	got := percentDelta(prev, cur)
	if math.Abs(got-22.222) > 0.01 {
		t.Errorf("percentDelta = %.3f, want ≈22.22", got)
	}
}

func TestPercentDeltaZeroIdleDelta(t *testing.T) {
	// busy 全部增长，idle 不变 → 100%
	prev := cpu.TimesStat{User: 100, Idle: 900}
	cur := cpu.TimesStat{User: 200, Idle: 900}
	if got := percentDelta(prev, cur); got != 100 {
		t.Errorf("percentDelta = %v, want 100", got)
	}
}

func TestPercentDeltaNoDelta(t *testing.T) {
	prev := cpu.TimesStat{User: 100, Idle: 900}
	cur := cpu.TimesStat{User: 100, Idle: 900}
	if got := percentDelta(prev, cur); got != 0 {
		t.Errorf("相同采样应返回 0, got %v", got)
	}
}

func TestPercentDeltaClamped(t *testing.T) {
	// idle 倒退会造成瞬时 >100%，必须截断到 100。
	// prev: idle 980 / total 1000；cur: idle 960 / total 1020
	prev := cpu.TimesStat{User: 10, System: 10, Idle: 980}
	cur := cpu.TimesStat{User: 30, System: 30, Idle: 960}
	if got := percentDelta(prev, cur); got != 100 {
		t.Errorf("超出上限应截断到 100, got %v", got)
	}
}

func TestSumTimes(t *testing.T) {
	cores := []cpu.TimesStat{
		{CPU: "cpu0", User: 1, System: 2, Idle: 3, Nice: 4},
		{CPU: "cpu1", User: 5, System: 6, Idle: 7, Iowait: 8},
	}
	total := sumTimes(cores)
	if total.CPU != "cpu-total" || total.User != 6 || total.System != 8 || total.Idle != 10 {
		t.Errorf("sumTimes 结果不符: %+v", total)
	}
	if got := timesTotal(total); got != 36 {
		t.Errorf("timesTotal = %v, want 36", got)
	}
}

func TestCloneTimesIsolation(t *testing.T) {
	// 基线必须与 gopsutil 返回缓冲解耦：源切片随后被复用/改写不影响已存基线。
	src := []cpu.TimesStat{
		{CPU: "cpu0", User: 1, Idle: 9},
		{CPU: "cpu1", User: 2, Idle: 18},
	}
	clone := cloneTimes(src)
	src[0].User = 99
	src[0].Idle = 0
	src[1].Idle = 99
	src = append(src, cpu.TimesStat{CPU: "cpu2", User: 3, Idle: 27})
	if len(clone) != 2 {
		t.Fatalf("clone 长度应保持 2, got %d", len(clone))
	}
	if clone[0].User != 1 || clone[0].Idle != 9 || clone[1].Idle != 18 {
		t.Errorf("clone 应独立于源切片: %+v", clone)
	}
}

// —— JSON 契约测试：验证序列化形状与文档数据模型一致 ——

func TestSnapshotJSONContract(t *testing.T) {
	freq := 3400.5
	rr := 3.5
	snap := Snapshot{
		Timestamp: "2026-09-06T10:00:00.000+08:00",
		Host:      &HostInfo{Hostname: "prod-01", OS: "Debian GNU/Linux 12", Kernel: "6.1.0-xx", UptimeSeconds: 360000, LanIP: "192.168.1.10"},
		CPU:       &CPUInfo{Percent: 12.3, LoadAvg: []float64{0.15, 0.22, 0.30}, PerCore: []float64{10, 15.5}, Frequency: &freq},
		Memory:    &MemoryInfo{Total: 17179869184, Used: 8589934592, Percent: 45.6, Swap: SwapInfo{Total: 4294967296, Used: 0, Percent: 0}},
		Disk: &DiskInfo{Disks: []DiskItem{
			{Index: 0, Name: "sda", Mount: "/", Total: 107374182400, Used: 64424509440, Percent: 60.1, ReadRate: &rr},
		}},
		Network: &NetworkInfo{
			TCPEstablished: 42,
			Nets: []NetItem{
				{Index: 0, Name: "eth0", RXBytes: 100, TXBytes: 200, RXRate: 3.5, TXRate: 1.25},
			},
		},
		GPU: nil,
	}

	data, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	// 六个模块键必须齐全（GPU 尽力而为 → null，但键保留）。
	for _, k := range []string{"timestamp", "host", "cpu", "memory", "disk", "network", "gpu"} {
		if _, ok := m[k]; !ok {
			t.Errorf("快照缺少键 %q", k)
		}
	}
	if m["gpu"] != nil {
		t.Errorf("gpu 应为 null, got %v", m["gpu"])
	}

	cpuObj := m["cpu"].(map[string]any)
	if cpuObj["percent"].(float64) != 12.3 {
		t.Errorf("cpu.percent 不符: %v", cpuObj["percent"])
	}
	if cpuObj["frequency"].(float64) != 3400.5 {
		t.Errorf("cpu.frequency 不符: %v", cpuObj["frequency"])
	}
	la := cpuObj["load_avg"].([]any)
	if len(la) != 3 || la[1].(float64) != 0.22 {
		t.Errorf("cpu.load_avg 不符: %v", la)
	}

	memObj := m["memory"].(map[string]any)
	swap := memObj["swap"].(map[string]any)
	if _, ok := swap["total"]; !ok {
		t.Error("memory.swap 应存在")
	}

	netObj := m["network"].(map[string]any)
	if _, ok := netObj["tcp_established"]; !ok {
		t.Error("network.tcp_established 应存在")
	}
	nets, ok := netObj["nets"].([]any)
	if !ok || len(nets) != 1 {
		t.Fatalf("network.nets 应为数组, got %v", netObj["nets"])
	}
	first := nets[0].(map[string]any)
	if first["rx_bytes"].(float64) != 100 || first["rx_rate"].(float64) != 3.5 {
		t.Errorf("network.nets[0] 内容不符: %v", first)
	}

	diskObj := m["disk"].(map[string]any)
	disks, ok := diskObj["disks"].([]any)
	if !ok || len(disks) != 1 {
		t.Fatalf("disk.disks 应为数组, got %v", diskObj["disks"])
	}
	d0 := disks[0].(map[string]any)
	if d0["mount"] != "/" || d0["name"] != "sda" || d0["read_rate"].(float64) != 3.5 {
		t.Errorf("disk.disks[0] 内容不符: %v", d0)
	}

	if m["timestamp"] != "2026-09-06T10:00:00.000+08:00" {
		t.Errorf("timestamp 不符: %v", m["timestamp"])
	}
}

func TestSnapshotDisabledModulesMarshalNull(t *testing.T) {
	// enable_* 关闭路径：对象为 nil 且不带 omitempty → 键保留、值为 null（schema 稳定）。
	snap := Snapshot{Timestamp: "2026-09-06T10:00:00.000+08:00"} // 全部模块 nil
	data, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	for _, k := range []string{"host", "cpu", "memory", "disk", "network", "gpu"} {
		if v, ok := m[k]; !ok || v != nil {
			t.Errorf("模块 %q 应保留键且为 null, got %v (present=%v)", k, v, ok)
		}
	}
}

func TestFrequencyNilMarshalsNull(t *testing.T) {
	// 尽力而为字段：无数据源时模块仍在、该字段为 null。
	snap := Snapshot{
		Timestamp: "2026-09-06T10:00:00.000+08:00",
		CPU:       &CPUInfo{Percent: 0, LoadAvg: []float64{0, 0, 0}, PerCore: []float64{0}, Frequency: nil},
	}
	data, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var m map[string]any
	_ = json.Unmarshal(data, &m)
	cpuObj := m["cpu"].(map[string]any)
	if v, ok := cpuObj["frequency"]; !ok || v != nil {
		t.Errorf("cpu.frequency 应保留键且为 null, got %v", v)
	}
}
