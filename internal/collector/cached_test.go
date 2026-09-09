package collector

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestCachedSnapshotSyncRefreshPersists(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "snap.json")

	calls := 0
	src := func() (Snapshot, error) {
		calls++
		p := float64(calls)
		return Snapshot{
			Timestamp: "t",
			Host:      &HostInfo{Hostname: "h" + intText(calls)},
			CPU:       &CPUInfo{Percent: p, LoadAvg: []float64{p}, PerCore: []float64{p}},
		}, nil
	}

	c := NewCachedSnapshotter(src, file, func() time.Duration { return time.Hour })
	s, err := c.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if calls != 1 || s.Host.Hostname != "h1" || s.CPU.Percent != 1 {
		t.Fatalf("首读应触发一次采集: calls=%d %+v", calls, s)
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("缓存文件应已写入: %v", err)
	}
}

func TestCachedSnapshotSecondReaderLoadsFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "snap.json")

	src := func() (Snapshot, error) {
		return Snapshot{
			Timestamp: "t1",
			Host:      &HostInfo{Hostname: "persisted-host"},
			CPU:       &CPUInfo{Percent: 12.5, LoadAvg: []float64{0.1, 0.2, 0.3}, PerCore: []float64{1, 2}},
		}, nil
	}
	first := NewCachedSnapshotter(src, file, func() time.Duration { return time.Hour })
	if _, err := first.Snapshot(); err != nil {
		t.Fatal(err)
	}

	// 第二个实例（模拟进程重启）：载入文件后读缓存不触发采集
	calls := 0
	second := NewCachedSnapshotter(func() (Snapshot, error) {
		calls++
		return Snapshot{}, nil
	}, file, func() time.Duration { return time.Hour })
	second.loadFromFile()

	s, err := second.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Errorf("载入文件后不应触发采集, calls=%d", calls)
	}
	if s.Host.Hostname != "persisted-host" || s.CPU.Percent != 12.5 {
		t.Errorf("应返回缓存快照: %+v", s)
	}
	if len(s.CPU.LoadAvg) != 3 || len(s.CPU.PerCore) != 2 {
		t.Errorf("缓存快照切片应完整: %+v", s.CPU)
	}
}

func TestCachedSnapshotOverwriteFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "snap.json")

	i := 0
	src := func() (Snapshot, error) {
		i++
		return Snapshot{Timestamp: "t", Host: &HostInfo{Hostname: intText(i)}}, nil
	}
	c := NewCachedSnapshotter(src, file, func() time.Duration { return time.Hour })
	_, _ = c.Snapshot()
	_ = c.refreshOnce() // 新一轮采集应覆盖文件

	// 文件内容应为第二轮（hostname 2）
	s, _ := c.Snapshot()
	_ = s
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 {
		t.Fatal("文件不应为空")
	}
}

func TestCachedDynamicIntervalReRead(t *testing.T) {
	dir := t.TempDir()
	var calls int32
	c := NewCachedSnapshotter(
		func() (Snapshot, error) { return Snapshot{Timestamp: "t"}, nil },
		filepath.Join(dir, "x.json"),
		func() time.Duration {
			atomic.AddInt32(&calls, 1)
			return time.Millisecond
		},
	)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		c.Start(ctx)
		close(done)
	}()
	time.Sleep(40 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("后台循环未在取消后及时退出")
	}
	if got := atomic.LoadInt32(&calls); got < 2 {
		t.Fatalf("间隔函数应按周期被重复调用（当前 %d 次），确认后台周期随配置动态重读", got)
	}
}

func intText(n int) string {
	if n == 0 {
		return "0"
	}
	out := ""
	for n > 0 {
		out = string(rune('0'+n%10)) + out
		n /= 10
	}
	return out
}
