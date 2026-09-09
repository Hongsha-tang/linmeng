// 琳萌（Linmeng）Linux 系统信息监视器 —— 程序入口。
// 职责：加载配置（setting.json + .env）、构造采集器与接口层、
// 内嵌前端静态资源（web/static）、启动 HTTP 服务并优雅退出。
// 模块职责与文件布局见 docs/architecture.md。
package main

import (
	"context"
	"embed"
	"errors"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"linmeng/internal/collector"
	"linmeng/internal/config"
	lhttp "linmeng/internal/http"
)

//go:embed web/static
var assets embed.FS

func main() {
	logger := log.New(os.Stdout, "linmeng ", log.LstdFlags)

	// 配置以二进制所在工作目录的 setting.json + .env 为准（部署见 docs/deployment.md）。
	cfg, err := config.Load("setting.json", ".env")
	if err != nil {
		logger.Fatalf("配置加载失败: %v", err)
	}

	pages, err := fs.Sub(assets, "web/static")
	if err != nil {
		logger.Fatalf("加载内嵌前端资源失败: %v", err)
	}

	monitor := collector.New(cfg)

	// 快照缓存采集：后台按刷新间隔持续采集并落地临时文件，
	// HTTP 读缓存即时返回（页面进入即有数据、首帧即有差分值）。
	// 周期每次刷新后重读配置，使页面“修改配置”后即时生效（F-002）。
	snapCache := collector.NewCachedSnapshotter(
		monitor.Snapshot,
		"linmeng-cache/snapshot.json",
		func() time.Duration {
			cfg.RLock()
			d := time.Duration(cfg.RefreshIntervalSeconds) * time.Second
			cfg.RUnlock()
			return d
		},
	)
	srv := lhttp.NewServer(cfg, snapCache, pages)
	addr := net.JoinHostPort(cfg.ListenHost, strconv.Itoa(cfg.Port))

	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       90 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go snapCache.Start(ctx) // 后台缓存循环随进程退出信号停止

	errCh := make(chan error, 1)
	go func() {
		errCh <- httpSrv.ListenAndServe()
	}()

	logger.Printf("琳萌 Linmeng 启动成功，监听 %s，鉴权开启=%v", addr, cfg.AuthEnabled)

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Fatalf("HTTP 服务异常退出: %v", err)
		}
	case <-ctx.Done():
		logger.Printf("收到退出信号，正在关闭...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			logger.Printf("关闭异常: %v", err)
		}
	}
	logger.Printf("已退出")
}
