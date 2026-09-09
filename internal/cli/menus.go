// 菜单树定义（设计稿 §4.4 目录层级）+ 根目录构建。
package cli

// buildRootMenu 依据设计稿目录层级构建根菜单。
// 命令闭包持有 *App，导航时调用。
func buildRootMenu(a *App) *Menu {
	return buildMenu("linmeng 运维工具", []*Entry{
		{
			Sub: buildMenu("服务生命周期", []*Entry{
				{Cmd: &Command{Label: "查看服务状态（status）", Run: func() error {
					return a.Exec.Status()
				}}},
				{Cmd: &Command{Label: "启动服务（start）", NeedRoot: true, Run: func() error {
					return a.Exec.Start()
				}}},
				{Cmd: &Command{Label: "停止服务（stop）", NeedRoot: true, Run: func() error {
					return a.Exec.Stop()
				}}},
				{Cmd: &Command{Label: "重启服务（restart）", NeedRoot: true, Run: func() error {
					return a.Exec.Restart()
				}}},
			}),
		},
		{
			Sub: buildMenu("日志查看", []*Entry{
				{Cmd: &Command{Label: "查看最近 200 行日志", Run: func() error {
					return a.Exec.LogRecent(200)
				}}},
				{Cmd: &Command{Label: "实时跟随日志（Ctrl+C 退出返回）", Kind: KindPersistent, Run: func() error {
					return a.Exec.LogFollow()
				}}},
				{Cmd: &Command{Label: "自定义日志行数", Run: func() error {
					line, err := a.readLine("请输入日志行数（1–5000）：")
					if err != nil {
						return err
					}
					n, ok := parseNum(line)
					if !ok || n < 1 || n > 5000 {
						a.outLine("行数无效（1–5000）")
						return nil
					}
					return a.Exec.LogRecent(n)
				}}},
			}),
		},
		{
			Sub: buildMenu("配置管理", []*Entry{
				{Cmd: &Command{Label: "查看当前配置（读 setting.json，脱敏）", Run: a.cmdConfigView}},
				{Cmd: &Command{Label: "查看单个配置项", Run: a.cmdConfigGetKey}},
				{Cmd: &Command{Label: "修改配置项", NeedRoot: true, Run: a.cmdConfigModify}},
				{Cmd: &Command{Label: "修改登录密码", NeedRoot: true, Run: a.cmdConfigPassword}},
			}),
		},
		{
			Sub: buildMenu("系统信息查看", []*Entry{
				{Cmd: &Command{Label: "当前快照摘要（读本地快照缓存）", Run: a.cmdSnapshotSummary}},
				{Cmd: &Command{Label: "运行自检（端口/登录页/401/登录后快照）", Run: a.cmdSelfCheck}},
			}),
		},
		{
			Sub: buildMenu("防火墙配置", []*Entry{
				{Cmd: &Command{Label: "放行默认端口（ufw allow <port>/tcp）", NeedRoot: true, Run: a.cmdFirewallDefault}},
				{Cmd: &Command{Label: "放行自定义端口", NeedRoot: true, Run: a.cmdFirewallCustom}},
			}),
		},
		{
			Sub: buildMenu("更新与回滚", []*Entry{
				{Cmd: &Command{Label: "更新服务（备份→覆盖→重启）", NeedRoot: true, Run: a.cmdUpdate}},
				{Cmd: &Command{Label: "回滚到上次备份", NeedRoot: true, Run: a.cmdRollback}},
			}),
		},
		{
			Sub: buildMenu("工具信息", []*Entry{
				{Cmd: &Command{Label: "版本信息", Run: a.cmdVersion}},
				{Cmd: &Command{Label: "帮助与关于", Run: a.cmdHelp}},
			}),
		},
	})
}
