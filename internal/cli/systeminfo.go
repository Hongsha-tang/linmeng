// 系统信息查看：本地快照文件摘要（零网络）与运行自检（HTTP 本地探测）。
package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// 快照摘要所需的最小结构（只取展示字段，未知字段忽略，向前兼容）。
type snapFile struct {
	Timestamp string    `json:"timestamp"`
	Host      *snapHost `json:"host"`
	CPU       *snapCPU  `json:"cpu"`
	Memory    *snapMem  `json:"memory"`
	Disk      *snapDisk `json:"disk"`
	Network   *snapNet  `json:"network"`
}

type snapHost struct {
	Hostname string `json:"hostname"`
	OS       string `json:"os"`
	Kernel   string `json:"kernel"`
	Uptime   int64  `json:"uptime_seconds"`
	LanIP    string `json:"lan_ip"`
}

type snapCPU struct {
	Percent float64   `json:"percent"`
	LoadAvg []float64 `json:"load_avg"`
	Cores   int       `json:"-"` // per_core 数量由 perCore 长度推导
	PerCore []float64 `json:"per_core"`
	Freq    *float64  `json:"frequency"`
}

type snapMem struct {
	Total     int64   `json:"total"`
	Used      int64   `json:"used"`
	Percent   float64 `json:"percent"`
	Available int64   `json:"available"`
	Swap      *struct {
		Total   int64   `json:"total"`
		Used    int64   `json:"used"`
		Percent float64 `json:"percent"`
	} `json:"swap"`
}

type snapDisk struct {
	Disks []snapDiskItem `json:"disks"`
}

type snapDiskItem struct {
	Index   int      `json:"index"`
	Name    string   `json:"name"`
	Mount   string   `json:"mount"`
	Total   int64    `json:"total"`
	Used    int64    `json:"used"`
	Percent float64  `json:"percent"`
	IoPct   *float64 `json:"io_percent"`
}

type snapNet struct {
	TCPEstablished int64         `json:"tcp_established"`
	Nets           []snapNetItem `json:"nets"`
}

type snapNetItem struct {
	Index  int     `json:"index"`
	Name   string  `json:"name"`
	RXRate float64 `json:"rx_rate"`
	TXRate float64 `json:"tx_rate"`
}

// cmdSnapshotSummary 读取本地快照缓存并打印摘要（主机/CPU/内存/磁盘/网络）。
func (a *App) cmdSnapshotSummary() error {
	data, err := os.ReadFile(a.snapshotPath())
	if err != nil {
		if os.IsNotExist(err) {
			a.outLine("未找到快照文件 " + a.snapshotPath() + "（服务可能未运行或尚未完成首次采集）")
			return nil
		}
		return fmt.Errorf("读取快照失败: %w", err)
	}
	var s snapFile
	if err := json.Unmarshal(data, &s); err != nil {
		return fmt.Errorf("解析快照失败: %w", err)
	}
	ts := strings.TrimSpace(s.Timestamp)
	if ts == "" {
		ts = "（未知）"
	}
	a.outLine("快照时间：" + ts)
	if h := s.Host; h != nil {
		a.outLine(fmt.Sprintf("主机：%s  系统：%s  内核：%s", orDash(h.Hostname), orDash(h.OS), orDash(h.Kernel)))
		a.outLine(fmt.Sprintf("运行时长：%s  局域网IP：%s", fmtDur(h.Uptime), orDash(h.LanIP)))
	}
	if c := s.CPU; c != nil {
		a.outLine(fmt.Sprintf("CPU：总使用率 %.1f%%  负载(1/5/15) %v", c.Percent, fmtLoad(c.LoadAvg)))
	}
	if m := s.Memory; m != nil {
		sw := "—"
		if m.Swap != nil {
			sw = fmt.Sprintf("%s / %s（%.1f%%）", fmtBytes(m.Swap.Used), fmtBytes(m.Swap.Total), m.Swap.Percent)
		}
		a.outLine(fmt.Sprintf("内存：%s / %s（%.1f%%）  可用 %s  交换 %s",
			fmtBytes(m.Used), fmtBytes(m.Total), m.Percent, fmtBytes(m.Available), sw))
	}
	if d := s.Disk; d != nil {
		for _, it := range d.Disks {
			root := ""
			if it.Mount == "/" {
				root = "（根盘）"
			}
			ioTxt := "-"
			if it.IoPct != nil {
				ioTxt = fmt.Sprintf("%.1f%%", *it.IoPct)
			}
			if it.Mount == "/" {
				a.outLine(fmt.Sprintf("硬盘#%d：%s %s  空间 %s / %s（%.1f%%）  IO调用 %s",
					it.Index, it.Name, root, fmtBytes(it.Used), fmtBytes(it.Total), it.Percent, ioTxt))
			} else {
				a.outLine(fmt.Sprintf("硬盘#%d：%s %s  IO调用 %s（非根盘不采集空间）",
					it.Index, it.Name, root, ioTxt))
			}
		}
	}
	if n := s.Network; n != nil {
		for _, it := range n.Nets {
			a.outLine(fmt.Sprintf("网络#%d：%s  下行 %s/s  上行 %s/s",
				it.Index, it.Name, fmtRate(it.RXRate), fmtRate(it.TXRate)))
		}
		a.outLine(fmt.Sprintf("TCP 已建连接（聚合）：%d", n.TCPEstablished))
	}
	return nil
}

// ---------- 运行自检（本地 HTTP 闭环） ----------

func (a *App) cmdSelfCheck() error {
	port := 8002
	var authOn = true
	var password = "admin123"
	if c, err := readLocalConfigForSelfCheck(a.settingsPath(), a.envPath()); err == nil && c != nil {
		port = c.port
		authOn = c.authEnabled
		password = c.password
	}
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	client := &http.Client{Timeout: 3 * time.Second}
	fail := 0
	step := func(ok bool, msg string) {
		if ok {
			a.outLine("  [通过] " + msg)
		} else {
			a.outLine("  [失败] " + msg)
			fail++
		}
	}

	// 1. 登录页 200
	step(a.httpStatus(client, base+"/login") == 200, "登录页可访问（GET /login → 200）")

	// 2. 未登录受保护接口 → 401
	unauth := a.httpStatus(client, base+"/api/system/info")
	step(unauth == 401, fmt.Sprintf("未登录访问快照 → 401（实际 %d）", unauth))

	if authOn {
		// 3. 登录 → Cookie
		jar := newCookieJar()
		cl := &http.Client{Timeout: 3 * time.Second, Jar: jar}
		code, _ := postLogin(cl, base, password)
		step(code == 200, fmt.Sprintf("登录成功（POST /api/auth/login → 200，实际 %d）", code))
		if code == 200 {
			// 4. 携带 Cookie 取快照 → 200
			got := httpGetStatus(cl, base+"/api/system/info")
			step(got == 200, fmt.Sprintf("携带 Cookie 获取快照 → 200（实际 %d）", got))
		}
	} else {
		step(true, "鉴权已关闭（auth_enabled=false），跳过登录闭环")
		got := a.httpStatus(client, base+"/api/system/info")
		step(got == 200, fmt.Sprintf("关闭鉴权下快照免认证 → 200（实际 %d）", got))
	}

	if fail == 0 {
		a.outLine("运行自检全部通过。")
	} else {
		a.outLine(fmt.Sprintf("运行自检存在 %d 项未通过（见上）。", fail))
	}
	return nil
}

// selfCheckConfig 自检用最小配置读取（不依赖 config.Load 的严格校验）。
type selfCheckConfig struct {
	port        int
	authEnabled bool
	password    string
}

func readLocalConfigForSelfCheck(settingPath, envPath string) (*selfCheckConfig, error) {
	cfg := &selfCheckConfig{port: 8002, authEnabled: true, password: "admin123"}
	if data, err := os.ReadFile(settingPath); err == nil {
		var raw map[string]any
		if err := json.Unmarshal(data, &raw); err == nil {
			if p, ok := raw["port"].(float64); ok && int(p) >= 1 && int(p) <= 65535 {
				cfg.port = int(p)
			}
			if b, ok := raw["auth_enabled"].(bool); ok {
				cfg.authEnabled = b
			}
		}
	}
	if vals, err := readEnvFile(envPath); err == nil {
		if pw, ok := vals["AUTH_PASSWORD"]; ok && strings.TrimSpace(pw) != "" {
			cfg.password = strings.TrimSpace(pw)
		}
	}
	return cfg, nil
}

func (a *App) httpStatus(client *http.Client, url string) int {
	resp, err := client.Get(url)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

func httpGetStatus(client *http.Client, url string) int {
	resp, err := client.Get(url)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

// postLogin 提交密码登录并返回状态码（cookie 由 jar 持有）。
func postLogin(client *http.Client, base, password string) (int, error) {
	body := fmt.Sprintf(`{"password":%q}`, password)
	resp, err := client.Post(base+"/api/auth/login", "application/json", strings.NewReader(body))
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, nil
}
