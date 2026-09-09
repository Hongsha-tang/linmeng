package collector

import (
	"encoding/json"
	"testing"
)

func TestParseLoadavgCounts(t *testing.T) {
	gotR, gotT, ok := parseLoadavgCounts([]byte("0.03 0.05 0.08 1/266 12345\n"))
	if !ok || gotR != 1 || gotT != 266 {
		t.Fatalf("parseLoadavg = %d/%d ok=%v", gotR, gotT, ok)
	}
	if _, _, ok := parseLoadavgCounts([]byte("bad")); ok {
		t.Error("畸形内容应失败")
	}
	if _, _, ok := parseLoadavgCounts([]byte("0.0 0.0 0.0 1/0\n")); ok {
		t.Error("total 为 0 应失败")
	}
}

func TestParseFileNr(t *testing.T) {
	a, l, ok := parseFileNr([]byte("5120  0  524288\n"))
	if !ok || a != 5120 || l != 524288 {
		t.Fatalf("parseFileNr = %d/%d ok=%v", a, l, ok)
	}
	if _, _, ok := parseFileNr([]byte("1 2")); ok {
		t.Error("字段不足应失败")
	}
}

func TestCountEstablishedTCP(t *testing.T) {
	sample := "  sl  local_address rem_address   st\n" +
		"   0: 0100007F:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 12345 1 0000000000000000 100 0 0 10 0\n" +
		"   1: 0100007F:1F91 00000000:0000 01 00000000:00000000 00:00000000 00000000     0        0 12346 1 0000000000000000 100 0 0 10 0\n"
	if got := countEstablishedTCP([]byte(sample)); got != 1 {
		t.Fatalf("应统计 1 条 ESTABLISHED(0A), got %d", got)
	}
	if got := countEstablishedTCP([]byte("  sl  local_address rem_address   st\n")); got != 0 {
		t.Fatalf("仅表头应为 0, got %d", got)
	}
}

// 扩展字段 JSON 契约：新增模块/字段键存在、尽力而为指针为 null。
func TestExtendedFieldsJSONContract(t *testing.T) {
	snap := Snapshot{
		Timestamp: "2026-09-07T10:00:00.000+08:00",
		Disk: &DiskInfo{Disks: []DiskItem{
			{Index: 0, Name: "sda", Mount: "/", Inodes: 1000, InodesPercent: 12.5},
			{Index: 1, Name: "sdb"}, // 非根盘：仅 IO 类，空间字段为 0
		}},
		Memory:  &MemoryInfo{Available: 1, Buff: 2, Cache: 3},
		Network: &NetworkInfo{TCPEstablished: 10, Nets: []NetItem{{Index: 0, Name: "eth0", RXDrops: 1, TXErrors: 2}}},
		Proc:    &ProcInfo{Total: 100, Running: 2},
		FS:      &FSInfo{FileDescriptors: 100, FileDescLimit: 1024},
	}
	data, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(data, &m)
	for _, k := range []string{"proc", "fs"} {
		if v, ok := m[k]; !ok || v == nil {
			t.Errorf("顶层模块 %q 应存在且为对象", k)
		}
	}
	disk := m["disk"].(map[string]any)
	disks, ok := disk["disks"].([]any)
	if !ok || len(disks) != 2 {
		t.Fatalf("disk.disks 应为 2 项数组, got %v", disk["disks"])
	}
	d0 := disks[0].(map[string]any)
	for _, k := range []string{"inodes", "inodes_percent", "read_rate", "write_rate", "read_iops", "write_iops"} {
		if _, ok := d0[k]; !ok {
			t.Errorf("disk.disks[0].%s 键应存在", k)
		}
	}
	for _, k := range []string{"read_rate", "write_rate", "read_iops", "write_iops"} {
		if d0[k] != nil {
			t.Errorf("未采集时 disk.disks[0].%s 应为 null, got %v", k, d0[k])
		}
	}
	if d0["mount"] != "/" || d0["inodes"].(float64) != 1000 {
		t.Errorf("根盘项空间字段不符: %v", d0)
	}
	d1 := disks[1].(map[string]any)
	if d1["mount"] != "" || d1["total"].(float64) != 0 {
		t.Errorf("非根盘项不应携带空间字段: %v", d1)
	}
	mem := m["memory"].(map[string]any)
	for _, k := range []string{"available", "buff", "cache"} {
		if mem[k].(float64) == 0 {
			t.Errorf("memory.%s 应有值", k)
		}
	}
	net := m["network"].(map[string]any)
	if _, ok := net["tcp_established"]; !ok {
		t.Error("network.tcp_established 应存在（聚合）")
	}
	nets, ok := net["nets"].([]any)
	if !ok || len(nets) != 1 {
		t.Fatalf("network.nets 应为 1 项数组, got %v", net["nets"])
	}
	n0 := nets[0].(map[string]any)
	for _, k := range []string{"rx_drops", "tx_drops", "rx_errors", "tx_errors"} {
		if _, ok := n0[k]; !ok {
			t.Errorf("network.nets[0].%s 键应存在", k)
		}
	}
	proc := m["proc"].(map[string]any)
	fs := m["fs"].(map[string]any)
	if proc["total"].(float64) != 100 || proc["running"].(float64) != 2 {
		t.Errorf("proc 内容不符: %v", proc)
	}
	if fs["file_descriptors"].(float64) != 100 || fs["file_desc_limit"].(float64) != 1024 {
		t.Errorf("fs 内容不符: %v", fs)
	}
}

func TestNewModulesNullWhenDisabled(t *testing.T) {
	// enable_* 关闭路径：proc/fs 顶层键保留且为 null（schema 稳定）。
	snap := Snapshot{Timestamp: "t"}
	data, _ := json.Marshal(snap)
	var m map[string]any
	_ = json.Unmarshal(data, &m)
	for _, k := range []string{"proc", "fs"} {
		if v, ok := m[k]; !ok || v != nil {
			t.Errorf("%s 应保留键且为 null: %v", k, v)
		}
	}
}
