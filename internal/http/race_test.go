package http

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"linmeng/internal/collector"
	"linmeng/internal/config"
)

// TestConfigConcurrentReadWrite 并发覆盖 F-003 修复后的读/写路径：
// 后台缓存采集(读 cfg) × “修改配置”(写 cfg + 持久化) × 页面配置注入(读 cfg)
// 在 -race 下运行可发现残留数据竞争。
func TestConfigConcurrentReadWrite(t *testing.T) {
	dir := t.TempDir()
	settingPath := filepath.Join(dir, "setting.json")
	if err := os.WriteFile(settingPath, []byte(`{"auth_enabled": false, "enable_disk": false}`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.AuthEnabled = false
	cfg.SettingPath = settingPath
	cfg.EnableDisk = false

	mon := collector.New(cfg)
	cache := collector.NewCachedSnapshotter(
		mon.Snapshot,
		filepath.Join(dir, "snap.json"),
		func() time.Duration { return 5 * time.Millisecond },
	)
	ctx, cancel := context.WithCancel(context.Background())
	go cache.Start(ctx)

	pages := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte(`<script>window.__LM_CONFIG_RAW__ = "__LM_CFG_JSON__";</script>`)},
	}
	srv := NewServer(cfg, cache, pages)
	srv.log = log.New(io.Discard, "", 0)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	var errs int32
	var wg sync.WaitGroup
	worker := func(fn func()) {
		defer wg.Done()
		fn()
	}

	// A：页面读取（配置注入）
	wg.Add(1)
	go worker(func() {
		for i := 0; i < 80; i++ {
			resp, err := http.Get(ts.URL + "/login")
			if err == nil {
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
			}
		}
	})

	// B：后台读取（快照）
	wg.Add(1)
	go worker(func() {
		for i := 0; i < 80; i++ {
			resp, err := http.Get(ts.URL + "/api/system/info")
			if err == nil {
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
			}
		}
	})

	// C：写配置（交替模块开关与刷新间隔）
	wg.Add(1)
	go worker(func() {
		client := &http.Client{Timeout: 5 * time.Second}
		for i := 0; i < 40; i++ {
			var body string
			if i%2 == 0 {
				body = `{"enable_host": false, "refresh_interval_seconds": 3}`
			} else {
				body = `{"enable_host": true, "history_points": 8}`
			}
			resp, err := client.Post(ts.URL+"/api/settings", "application/json",
				io.NopCloser(stringReadCloser{body}))
			if err == nil {
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
				if resp.StatusCode >= 500 {
					atomic.AddInt32(&errs, 1)
				}
			}
		}
	})

	wg.Wait()
	cancel()

	if n := atomic.LoadInt32(&errs); n > 0 {
		t.Errorf("并发写配置出现 %d 个 5xx", n)
	}
	// 文件仍应合法
	data, err := os.ReadFile(settingPath)
	if err != nil {
		t.Fatalf("读取 setting.json: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("setting.json 不应为空")
	}
}

type stringReadCloser struct{ s string }

func (s stringReadCloser) Read(p []byte) (int, error) {
	if len(s.s) == 0 {
		return 0, io.EOF
	}
	n := copy(p, s.s)
	s.s = s.s[n:]
	return n, nil
}

func (s stringReadCloser) Close() error { return nil }
