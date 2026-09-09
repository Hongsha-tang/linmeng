// 显示格式化小工具（快照摘要输出用）。
package cli

import (
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"strings"
)

// newCookieJar 返回自检登录用的内存 Cookie 容器。
func newCookieJar() http.CookieJar {
	jar, _ := cookiejar.New(nil)
	return jar
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

func fmtBytes(n int64) string {
	if n < 0 {
		return "-"
	}
	units := []string{"B", "KB", "MB", "GB", "TB", "PB"}
	v := float64(n)
	i := 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%d B", n)
	}
	return fmt.Sprintf("%.1f %s", v, units[i])
}

func fmtRate(bps float64) string {
	return fmtBytes(int64(bps))
}

func fmtLoad(l []float64) string {
	if len(l) == 0 {
		return "-"
	}
	parts := make([]string, len(l))
	for i, v := range l {
		parts[i] = fmt.Sprintf("%.2f", v)
	}
	return strings.Join(parts, " / ")
}

func fmtDur(sec int64) string {
	if sec < 0 {
		return "-"
	}
	d := sec / 86400
	h := (sec % 86400) / 3600
	m := (sec % 3600) / 60
	s := sec % 60
	var out []string
	if d > 0 {
		out = append(out, fmt.Sprintf("%d天", d))
	}
	if h > 0 {
		out = append(out, fmt.Sprintf("%d时", h))
	}
	if m > 0 {
		out = append(out, fmt.Sprintf("%d分", m))
	}
	if s > 0 || len(out) == 0 {
		out = append(out, fmt.Sprintf("%d秒", s))
	}
	return strings.Join(out, " ")
}
