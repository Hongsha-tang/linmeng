// 琳萌（Linmeng）运维工具 CLI 入口。
// 独立运维二进制：安装为 /usr/local/bin/linmeng（命令名即 linmeng），
// 与服务二进制（/opt/linmeng/linmeng）解耦，职责见
// _temp_file/运维CLI设计规划260908_v1.3.md 与 docs/deployment.md。
//
// 版本号经 -ldflags "-X main.version=..." 注入：
//
//	go build -ldflags "-X main.version=v0.4.0" -o linmeng-cli ./cmd/linmeng-cli
package main

import (
	"os"

	"linmeng/internal/cli"
)

// version 构建期注入的版本号；未注入时为 dev。
var version = "dev"

func main() {
	// 非 root + 交互终端时自动以 sudo 重跑一次（LINMENG_NO_SUDO=1 可禁用）。
	if rerun, code := cli.MaybeElevate(os.Args[1:]); rerun {
		os.Exit(code)
	}
	a := cli.NewApp("", cli.DefaultSvc, version, cli.NewSystemExecutor(cli.DefaultSvc))
	os.Exit(a.Run(os.Args[1:]))
}
