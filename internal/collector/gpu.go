package collector

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// GPU 尽力而为探测（架构文档·关键设计决策 D9）。
// 探测顺序：① NVIDIA——nvidia-smi；② AMD——/sys/class/drm 下 vendor=0x1002
// 且含 gpu_busy_percent 的显卡；③ 其它/Intel——无统一利用率 API，不采集。
// 任何探测失败/无数据源都返回 nil（gpu: null，软失败，不使整帧 500）。
//
// 采集口径：
//   - percent：整体平均使用率（%），多卡取算术平均；
//   - per_gpu：逐卡 {index, name, percent}，便于逐卡展示；
//   - name：NVIDIA 取 nvidia-smi 名称；AMD 取 sysfs product_name（缺省 "AMD GPU"）。

// gpuSample 探测得到的单卡样本（未分配 index，组装时按序编号）。
type gpuSample struct {
	name    string
	percent float64
}

// probeGPU 依序尝试各数据源，全部不可得返回 nil。
func probeGPU() *GPUInfo {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if cards, err := probeNVIDIA(ctx); err == nil && len(cards) > 0 {
		return buildGPU(cards)
	}
	if cards, err := scanAMD("/sys/class/drm"); err == nil && len(cards) > 0 {
		return buildGPU(cards)
	}
	return nil
}

// probeNVIDIA 通过 nvidia-smi 查询逐卡 index/名称/利用率（csv 输出）。
func probeNVIDIA(ctx context.Context) ([]gpuSample, error) {
	bin, err := exec.LookPath("nvidia-smi")
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, bin,
		"--query-gpu=index,name,utilization.gpu",
		"--format=csv,noheader,nounits")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return parseNvidiaCSV(out)
}

// parseNvidiaCSV 解析 nvidia-smi 的 csv 行：index,name,utilization.gpu。
// 利用率无法解析的行跳过（尽力而为）。纯函数，便于测试。
func parseNvidiaCSV(data []byte) ([]gpuSample, error) {
	r := csv.NewReader(strings.NewReader(string(data)))
	r.FieldsPerRecord = -1 // 容忍变长/损坏行，逐行容错
	records, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("解析 nvidia-smi 输出失败: %w", err)
	}
	var out []gpuSample
	for _, rec := range records {
		if len(rec) < 3 {
			continue
		}
		name := strings.TrimSpace(rec[1])
		pct, err := strconv.ParseFloat(strings.TrimSpace(rec[2]), 64)
		if err != nil {
			continue
		}
		out = append(out, gpuSample{name: name, percent: clampPercent(pct)})
	}
	if len(out) == 0 {
		return nil, errors.New("nvidia-smi 未返回可用 GPU 利用率")
	}
	return out, nil
}

// scanAMD 扫描 sysfs DRM 设备：vendor=0x1002（AMD）且可读 gpu_busy_percent 的显卡。
// drmRoot 可注入便于测试（生产为 /sys/class/drm）。
func scanAMD(drmRoot string) ([]gpuSample, error) {
	cards, err := filepath.Glob(filepath.Join(drmRoot, "card[0-9]*"))
	if err != nil {
		return nil, err
	}
	var out []gpuSample
	for _, card := range cards {
		dev := filepath.Join(card, "device")
		vendor, err := os.ReadFile(filepath.Join(dev, "vendor"))
		if err != nil {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(string(vendor)), "0x1002") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dev, "gpu_busy_percent"))
		if err != nil {
			// 无利用率读数的 AMD 卡不做硬失败，跳过（尽力而为）。
			continue
		}
		pct, err := strconv.ParseFloat(strings.TrimSpace(string(raw)), 64)
		if err != nil {
			continue
		}
		name := strings.TrimSpace(readSysfsText(filepath.Join(dev, "product_name")))
		if name == "" {
			name = "AMD GPU"
		}
		out = append(out, gpuSample{name: name, percent: clampPercent(pct)})
	}
	if len(out) == 0 {
		return nil, errors.New("未发现可用 AMD GPU 数据源")
	}
	return out, nil
}

// readSysfsText 尽力读取文本文件；出错返回空串。
func readSysfsText(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// buildGPU 由样本组装 GPUInfo：count/name 列表/整体平均 percent/逐卡 per_gpu。
func buildGPU(samples []gpuSample) *GPUInfo {
	gi := &GPUInfo{
		Count:  len(samples),
		Name:   make([]string, 0, len(samples)),
		PerGPU: make([]GPUCard, 0, len(samples)),
	}
	var sum float64
	for i, s := range samples {
		gi.Name = append(gi.Name, s.name)
		gi.PerGPU = append(gi.PerGPU, GPUCard{Index: i, Name: s.name, Percent: s.percent})
		sum += s.percent
	}
	gi.Percent = sum / float64(len(samples))
	return gi
}

// clampPercent 将利用率截断到 0-100。
func clampPercent(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}
