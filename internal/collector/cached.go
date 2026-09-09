package collector

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// CachedSnapshotter 后台缓存采集器：
// 按固定间隔调用底层采集动作，将最新快照保存在内存并落地为 JSON 缓存文件
// （新快照覆盖旧文件）；读侧返回缓存副本，做到页面进入即可拿到数据、
// 首帧即有可用指标（含已完成的差分值），且读请求几乎零采集延迟。
// 进程重启后首次读取会先载入上次缓存文件（瞬时）再由后台循环覆盖。
type CachedSnapshotter struct {
	collect  func() (Snapshot, error)
	filePath string
	// interval 返回两次采集的间隔；每次刷新后重新调用，运行期修改配置可即时生效（F-002）。
	interval func() time.Duration

	mu  sync.RWMutex
	cur Snapshot
	has bool
}

// NewCachedSnapshotter 构造后台缓存采集器。
// filePath 为缓存文件路径（目录自动创建）；interval 每次刷新后重新取值（nil 时按 1 秒），
// 使“修改刷新间隔”后后台采样周期同步变化。
func NewCachedSnapshotter(collect func() (Snapshot, error), filePath string, interval func() time.Duration) *CachedSnapshotter {
	if interval == nil {
		interval = func() time.Duration { return time.Second }
	}
	return &CachedSnapshotter{collect: collect, filePath: filePath, interval: interval}
}

// loadFromFile 启动/首次读时载入上次缓存（尽力而为：失败不报错，等后台刷新）。
func (c *CachedSnapshotter) loadFromFile() {
	data, err := os.ReadFile(c.filePath)
	if err != nil {
		return
	}
	var s Snapshot
	if err := json.Unmarshal(data, &s); err != nil {
		return
	}
	c.mu.Lock()
	c.cur = s
	c.has = true
	c.mu.Unlock()
}

// refreshOnce 执行一次采集并更新内存 + 覆盖缓存文件。
func (c *CachedSnapshotter) refreshOnce() error {
	s, err := c.collect()
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.cur = s
	c.has = true
	c.mu.Unlock()
	c.saveFile(s)
	return nil
}

// saveFile 原子写缓存文件（尽力而为，失败静默——缓存仅是加速手段）。
func (c *CachedSnapshotter) saveFile(s Snapshot) {
	if c.filePath == "" {
		return
	}
	data, err := json.Marshal(s)
	if err != nil {
		return
	}
	dir := filepath.Dir(c.filePath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	tmp := c.filePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, c.filePath)
}

// Start 启动后台循环：立即刷新一次，此后每次刷新后按 interval() 最新值等待周期；
// ctx 取消即停止。
func (c *CachedSnapshotter) Start(ctx context.Context) {
	c.loadFromFile()
	for {
		_ = c.refreshOnce() // 首轮尽快产出；失败由下一轮重试
		wait := c.interval()
		if wait <= 0 {
			wait = time.Second
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// Snapshot 返回最新缓存副本（瞬时）；尚未有任何缓存时执行一次同步采集。
func (c *CachedSnapshotter) Snapshot() (Snapshot, error) {
	c.mu.RLock()
	if c.has {
		s := cloneSnapshot(c.cur)
		c.mu.RUnlock()
		return s, nil
	}
	c.mu.RUnlock()
	if err := c.refreshOnce(); err != nil {
		return Snapshot{}, err
	}
	c.mu.RLock()
	s := cloneSnapshot(c.cur)
	c.mu.RUnlock()
	return s, nil
}

// cloneSnapshot 深拷贝快照的可变字段，避免读端与后台刷新并发改写共享切片。
func cloneSnapshot(s Snapshot) Snapshot {
	out := s
	if s.Host != nil {
		h := *s.Host
		out.Host = &h
	}
	if s.CPU != nil {
		c := *s.CPU
		c.LoadAvg = append([]float64(nil), s.CPU.LoadAvg...)
		c.PerCore = append([]float64(nil), s.CPU.PerCore...)
		if s.CPU.Frequency != nil {
			f := *s.CPU.Frequency
			c.Frequency = &f
		}
		out.CPU = &c
	}
	if s.Memory != nil {
		m := *s.Memory
		out.Memory = &m
	}
	if s.Disk != nil {
		d := *s.Disk
		d.Disks = make([]DiskItem, len(s.Disk.Disks))
		copyF := func(p *float64) *float64 {
			if p == nil {
				return nil
			}
			v := *p
			return &v
		}
		for i, src := range s.Disk.Disks {
			item := src
			item.IoPercent = copyF(src.IoPercent)
			item.ReadRate = copyF(src.ReadRate)
			item.WriteRate = copyF(src.WriteRate)
			item.ReadIOPS = copyF(src.ReadIOPS)
			item.WriteIOPS = copyF(src.WriteIOPS)
			d.Disks[i] = item
		}
		out.Disk = &d
	}
	if s.Network != nil {
		n := *s.Network
		n.Nets = append([]NetItem(nil), s.Network.Nets...)
		out.Network = &n
	}
	if s.GPU != nil {
		g := *s.GPU
		g.Name = append([]string(nil), s.GPU.Name...)
		g.PerGPU = append([]GPUCard(nil), s.GPU.PerGPU...)
		out.GPU = &g
	}
	return out
}
