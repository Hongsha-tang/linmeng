package collector

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestParseNvidiaCSV(t *testing.T) {
	in := `0, NVIDIA GeForce RTX 3060, 21
1, NVIDIA GeForce RTX 3090, 35
`
	cards, err := parseNvidiaCSV([]byte(in))
	if err != nil {
		t.Fatalf("parseNvidiaCSV: %v", err)
	}
	if len(cards) != 2 {
		t.Fatalf("应解析出 2 张卡, got %d: %+v", len(cards), cards)
	}
	if cards[0].name != "NVIDIA GeForce RTX 3060" || cards[0].percent != 21 {
		t.Errorf("卡0解析不符: %+v", cards[0])
	}
	if cards[1].name != "NVIDIA GeForce RTX 3090" || cards[1].percent != 35 {
		t.Errorf("卡1解析不符: %+v", cards[1])
	}
}

func TestParseNvidiaCSVSkipsBadRows(t *testing.T) {
	in := "0, Good Card, 42\n,broken line\n1, Bad Percent, abc\n3, Card Three, 88\n"
	cards, err := parseNvidiaCSV([]byte(in))
	if err != nil {
		t.Fatalf("parseNvidiaCSV: %v", err)
	}
	if len(cards) != 2 {
		t.Fatalf("应跳过坏行保留 2 张卡, got %d: %+v", len(cards), cards)
	}
	if cards[0].name != "Good Card" || cards[1].name != "Card Three" {
		t.Errorf("解析结果不符: %+v", cards)
	}
}

func TestParseNvidiaCSVEmpty(t *testing.T) {
	if _, err := parseNvidiaCSV([]byte("")); err == nil {
		t.Error("空输出应报错（无数据源）")
	}
}

func TestBuildGPU(t *testing.T) {
	samples := []gpuSample{
		{name: "NVIDIA GeForce RTX 3060", percent: 20},
		{name: "NVIDIA GeForce RTX 3090", percent: 40},
	}
	gi := buildGPU(samples)
	if gi.Count != 2 || len(gi.Name) != 2 || len(gi.PerGPU) != 2 {
		t.Fatalf("buildGPU 结构不符: %+v", gi)
	}
	if gi.Percent != 30 {
		t.Errorf("平均使用率应为 30, got %v", gi.Percent)
	}
	if gi.PerGPU[1].Index != 1 || gi.PerGPU[1].Name != "NVIDIA GeForce RTX 3090" {
		t.Errorf("per_gpu 不符: %+v", gi.PerGPU)
	}
}

func TestScanAMD(t *testing.T) {
	root := t.TempDir()
	mk := func(rel, content string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// card0：AMD，可读利用率 → 应被识别
	mk("card0/device/vendor", "0x1002\n")
	mk("card0/device/product_name", "AMD Radeon RX 6700 XT\n")
	mk("card0/device/gpu_busy_percent", "42\n")
	// card1：NVIDIA vendor → 排除
	mk("card1/device/vendor", "0x10de\n")
	mk("card1/device/product_name", "NVIDIA GeForce RTX 3060\n")
	mk("card1/device/gpu_busy_percent", "77\n")
	// card2：AMD 但无利用率文件 → 跳过（尽力而为）
	mk("card2/device/vendor", "0x1002\n")
	mk("card2/device/product_name", "AMD Old Card\n")
	// card3：AMD 且利用率文件损坏 → 跳过
	mk("card3/device/vendor", "0x1002\n")
	mk("card3/device/gpu_busy_percent", "not-a-number\n")

	cards, err := scanAMD(root)
	if err != nil {
		t.Fatalf("scanAMD: %v", err)
	}
	if len(cards) != 1 {
		t.Fatalf("应仅识别 1 张 AMD 卡, got %d: %+v", len(cards), cards)
	}
	if cards[0].name != "AMD Radeon RX 6700 XT" || cards[0].percent != 42 {
		t.Errorf("AMD 卡解析不符: %+v", cards[0])
	}
}

func TestScanAMDNone(t *testing.T) {
	root := t.TempDir()
	if _, err := scanAMD(root); err == nil {
		t.Error("无 AMD 卡时应报错")
	}
}

func TestClampPercent(t *testing.T) {
	if clampPercent(-5) != 0 || clampPercent(105) != 100 || clampPercent(42.5) != 42.5 {
		t.Error("clampPercent 行为不符")
	}
}

func TestGPUJSONShape(t *testing.T) {
	gi := buildGPU([]gpuSample{
		{name: "AMD Radeon RX 6700 XT", percent: 42},
		{name: "AMD Radeon RX 6600", percent: 8},
	})
	data, err := json.Marshal(gi)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	for _, k := range []string{"count", "name", "percent", "per_gpu"} {
		if _, ok := m[k]; !ok {
			t.Errorf("gpu 对象缺少键 %q: %s", k, data)
		}
	}
	if m["count"].(float64) != 2 || m["percent"].(float64) != 25 {
		t.Errorf("count/percent 不符: %v", m)
	}
	pg := m["per_gpu"].([]any)
	if len(pg) != 2 {
		t.Fatalf("per_gpu 应为 2 项: %v", pg)
	}
	card := pg[0].(map[string]any)
	if card["index"].(float64) != 0 || card["name"] != "AMD Radeon RX 6700 XT" {
		t.Errorf("per_gpu 卡片不符: %v", card)
	}
}

// 全模块可空快照 + GPU 有值时的整帧 JSON 形状（gpu 键应为对象而非 null）。
func TestSnapshotGPUObjectMarshal(t *testing.T) {
	gi := buildGPU([]gpuSample{{name: "NVIDIA GeForce RTX 3060", percent: 21}})
	snap := Snapshot{
		Timestamp: "2026-09-06T10:00:00.000+08:00",
		GPU:       gi,
	}
	data, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var m map[string]any
	_ = json.Unmarshal(data, &m)
	g, ok := m["gpu"].(map[string]any)
	if !ok {
		t.Fatalf("gpu 应为对象: %s", data)
	}
	if g["count"].(float64) != 1 || g["percent"].(float64) != 21 {
		t.Errorf("gpu 内容不符: %v", g)
	}
}
