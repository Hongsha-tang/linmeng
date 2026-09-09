package http

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"linmeng/internal/collector"
	"linmeng/internal/config"
)

// stubSnapshotter 固定返回快照或错误，隔离对真实采集的依赖。
type stubSnapshotter struct {
	snap collector.Snapshot
	err  error
}

func (st *stubSnapshotter) Snapshot() (collector.Snapshot, error) { return st.snap, st.err }

func testSnapshot() collector.Snapshot {
	return collector.Snapshot{
		Timestamp: "2026-09-06T10:00:00.000+08:00",
		Host:      &collector.HostInfo{Hostname: "test-host", OS: "Test OS", Kernel: "6.1", UptimeSeconds: 100, LanIP: "127.0.0.1"},
		CPU:       &collector.CPUInfo{Percent: 5, LoadAvg: []float64{0.1, 0.2, 0.3}, PerCore: []float64{5}},
	}
}

func newTestHandler(t *testing.T, cfg *config.Config, ss Snapshotter) http.Handler {
	t.Helper()
	if cfg == nil {
		cfg = config.Default()
		cfg.AuthEnabled = false
	}
	if ss == nil {
		ss = &stubSnapshotter{snap: testSnapshot()}
	}
	pages := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<!doctype html><title>linmeng</title>")},
	}
	srv := NewServer(cfg, ss, pages)
	srv.log = log.New(io.Discard, "", 0)
	return srv.Handler()
}

func doReq(h http.Handler, method, path, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
	var rd io.Reader
	if body != "" {
		rd = bytes.NewReader([]byte(body))
	}
	req := httptest.NewRequest(method, path, rd)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func authConfig(password string) *config.Config {
	c := config.Default()
	c.AuthEnabled = true
	c.AuthPassword = password
	c.SessionTTLMinutes = 60
	return c
}

func sessionCookie(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookieName {
			return c
		}
	}
	t.Fatal("响应未包含会话 Cookie")
	return nil
}

func TestLoginLogoutFlow(t *testing.T) {
	h := newTestHandler(t, authConfig("s3cret"), nil)

	// 未登录访问受保护接口 → 401
	if rec := doReq(h, "GET", "/api/system/info", "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("未登录快照应 401, got %d", rec.Code)
	}
	// 密码错误 → 401 且无 Cookie
	rec := doReq(h, "POST", "/api/auth/login", `{"password":"wrong"}`, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("密码错误应 401, got %d", rec.Code)
	}
	if len(rec.Result().Cookies()) != 0 {
		t.Error("密码错误不应下发 Cookie")
	}
	// 请求体非法 → 400
	if rec := doReq(h, "POST", "/api/auth/login", `not-json`, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("非法请求体应 400, got %d", rec.Code)
	}
	// 登录成功 → 200 + Set-Cookie
	rec = doReq(h, "POST", "/api/auth/login", `{"password":"s3cret"}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("登录成功应 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if strings.TrimSpace(rec.Body.String()) != "{}" {
		t.Errorf("登录成功应返回空对象 {}, got %s", rec.Body.String())
	}
	cookie := sessionCookie(t, rec)

	// 携带 Cookie 取快照 → 200 且为文档数据模型
	rec = doReq(h, "GET", "/api/system/info", "", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("携带会话应 200, got %d", rec.Code)
	}
	var snap map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &snap); err != nil {
		t.Fatalf("响应非 JSON: %v", err)
	}
	if _, ok := snap["timestamp"]; !ok {
		t.Error("快照缺少 timestamp")
	}
	hostObj, ok := snap["host"].(map[string]any)
	if !ok {
		t.Fatalf("快照 host 应为对象: %v", snap["host"])
	}
	if hostObj["hostname"] != "test-host" {
		t.Errorf("hostname 不符: %v", hostObj["hostname"])
	}
	// gpu 占位 → null 且键保留
	if v, ok := snap["gpu"]; !ok || v != nil {
		t.Errorf("gpu 应为 null: %v", v)
	}

	// 登出 → 200；此后原 Cookie 失效 → 401
	if rec := doReq(h, "POST", "/api/auth/logout", "", cookie); rec.Code != http.StatusOK {
		t.Fatalf("登出应 200, got %d", rec.Code)
	}
	if rec := doReq(h, "GET", "/api/system/info", "", cookie); rec.Code != http.StatusUnauthorized {
		t.Fatalf("登出后会话应失效(401), got %d", rec.Code)
	}
}

func TestPageRedirect(t *testing.T) {
	h := newTestHandler(t, authConfig("pw"), nil)

	// 未登录访问 / → 302 /login
	rec := doReq(h, "GET", "/", "", nil)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/login" {
		t.Fatalf("未登录访问 / 应 302→/login, got %d %q", rec.Code, rec.Header().Get("Location"))
	}
	// /login 公开可访问 → 200
	if rec := doReq(h, "GET", "/login", "", nil); rec.Code != http.StatusOK {
		t.Fatalf("登录页应 200, got %d", rec.Code)
	}
	// 登录后访问 / → 200 页面
	rec = doReq(h, "POST", "/api/auth/login", `{"password":"pw"}`, nil)
	cookie := sessionCookie(t, rec)
	if rec := doReq(h, "GET", "/", "", cookie); rec.Code != http.StatusOK {
		t.Fatalf("登录后 / 应 200, got %d", rec.Code)
	}
}

func TestAuthDisabledBypass(t *testing.T) {
	c := config.Default()
	c.AuthEnabled = false
	h := newTestHandler(t, c, nil)

	if rec := doReq(h, "GET", "/api/system/info", "", nil); rec.Code != http.StatusOK {
		t.Fatalf("关闭鉴权应免认证取快照, got %d", rec.Code)
	}
	if rec := doReq(h, "GET", "/", "", nil); rec.Code != http.StatusOK {
		t.Fatalf("关闭鉴权应直接返回页面, got %d", rec.Code)
	}
	if rec := doReq(h, "POST", "/api/auth/login", `{"password":""}`, nil); rec.Code != http.StatusOK {
		t.Fatalf("关闭鉴权登录应幂等 200, got %d", rec.Code)
	}
}

func TestSnapshotFailureReturns500(t *testing.T) {
	h := newTestHandler(t, authConfig("pw"), &stubSnapshotter{err: io.ErrUnexpectedEOF})
	rec := doReq(h, "POST", "/api/auth/login", `{"password":"pw"}`, nil)
	cookie := sessionCookie(t, rec)

	rec = doReq(h, "GET", "/api/system/info", "", cookie)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("采集失败应 500, got %d", rec.Code)
	}
	var e map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil || e["error"] == "" {
		t.Fatalf("应返回 JSON 错误体: %v %s", err, rec.Body.String())
	}
}

func TestMethodNotAllowed(t *testing.T) {
	h := newTestHandler(t, authConfig("pw"), nil)
	rec := doReq(h, "POST", "/api/system/info", "", nil)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST 快照应 405, got %d", rec.Code)
	}
	var e map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &e)
	if e["error"] == "" {
		t.Errorf("405 应为 JSON 错误体: %s", rec.Body.String())
	}
}

func TestUnknownAPIPath404(t *testing.T) {
	h := newTestHandler(t, authConfig("pw"), nil)
	// 未认证访问未知接口 → 401（接口语义优先于 404）
	if rec := doReq(h, "GET", "/api/nope", "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("未认证未知接口应 401, got %d", rec.Code)
	}
	// 认证后访问未知接口 → 404 JSON
	rec := doReq(h, "POST", "/api/auth/login", `{"password":"pw"}`, nil)
	cookie := sessionCookie(t, rec)
	if rec := doReq(h, "GET", "/api/nope", "", cookie); rec.Code != http.StatusNotFound {
		t.Fatalf("认证后未知接口应 404, got %d", rec.Code)
	}
}

// 会话无过期概念（产品要求）：存在即有效，删除即失效；超容量清理最旧。
func TestSessionStoreLifecycle(t *testing.T) {
	st := newSessionStore()
	st.add("s1")
	if !st.valid("s1") {
		t.Error("会话应有效（无过期概念）")
	}
	st.remove("s1")
	if st.valid("s1") {
		t.Error("登出删除后应无效")
	}
	if st.valid("ghost") {
		t.Error("未创建会话应无效")
	}
}

func TestSessionStoreCapPrunesOldest(t *testing.T) {
	st := newSessionStore()
	for i := 0; i < sessionStoreCap; i++ {
		st.add("s" + string(rune('a'+i%26)) + string(rune('a'+(i/26)%26)) + string(rune('0'+i%10)))
	}
	// 超出容量再添加一个 → 至少保留新会话且总数被约束
	st.add("newest")
	if !st.valid("newest") {
		t.Error("新会话应存在")
	}
	st.mu.Lock()
	n := len(st.entries)
	st.mu.Unlock()
	if n > sessionStoreCap {
		t.Errorf("会话总数应受上限约束, got %d", n)
	}
}

func TestSessionIDUnique(t *testing.T) {
	a, err := newSessionID()
	if err != nil {
		t.Fatalf("newSessionID: %v", err)
	}
	b, _ := newSessionID()
	if a == b || len(a) != 32 {
		t.Errorf("会话标识应唯一且为 32 位十六进制: %q %q", a, b)
	}
}

func TestSecureEqual(t *testing.T) {
	if !secureEqual("abc", "abc") {
		t.Error("相同值应相等")
	}
	if secureEqual("abc", "abd") {
		t.Error("不同值不应相等")
	}
	if secureEqual("abc", "abcd") {
		t.Error("长度不同不应相等")
	}
}

// 登录页自身加载的静态资源必须公开：未登录访问不应被 302 到 /login（否则返回 HTML 文本）。
func TestPublicStaticAssets(t *testing.T) {
	c := authConfig("pw")
	pages := fstest.MapFS{
		"style.css": &fstest.MapFile{Data: []byte("body{color:#333}")},
		"app.js":    &fstest.MapFile{Data: []byte("/* app */")},
	}
	srv := NewServer(c, &stubSnapshotter{snap: testSnapshot()}, pages)
	srv.log = log.New(io.Discard, "", 0)
	h := srv.Handler()

	rec := doReq(h, "GET", "/style.css", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("未登录取 style.css 应 200, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/css") {
		t.Errorf("style.css Content-Type 应为 text/css, got %q", ct)
	}

	rec = doReq(h, "GET", "/app.js", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("未登录取 app.js 应 200, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/javascript") {
		t.Errorf("app.js Content-Type 应为 text/javascript, got %q", ct)
	}
}

func TestIndexConfigInjection(t *testing.T) {
	pages := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte(`<script>window.__LM_CONFIG_RAW__ = "__LM_CFG_JSON__";</script>`)},
	}
	c := config.Default()
	c.AuthEnabled = false
	c.RefreshIntervalSeconds = 7
	c.HistoryPoints = 5
	srv := NewServer(c, &stubSnapshotter{snap: testSnapshot()}, pages)
	srv.log = log.New(io.Discard, "", 0)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/login", nil)
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("/login 应 200, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("Content-Type 应为 text/html, got %q", ct)
	}
	body := rec.Body.String()
	if strings.Contains(body, cfgMarker) {
		t.Error("配置占位符应被替换")
	}
	// 注入值以 JS 字符串内转义形态出现；按浏览器语义先还原再校验 JSON 内容。
	const marker = "__LM_CONFIG_RAW__ = "
	start := strings.Index(body, marker)
	if start < 0 {
		t.Fatalf("页面应包含配置变量声明，body=%s", body)
	}
	chunk := body[start+len(marker):]
	end := strings.Index(chunk, `";`)
	if end < 0 {
		t.Fatalf("配置字符串应闭合，chunk=%s", chunk)
	}
	raw, err := strconv.Unquote(chunk[:end+1])
	if err != nil {
		t.Fatalf("配置字符串还原失败: %v（chunk=%q）", err, chunk)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("配置 JSON 非法: %v（%q）", err, raw)
	}
	if got["refresh_interval_seconds"].(float64) != 7 {
		t.Errorf("refresh_interval_seconds 注入不符: %v", got)
	}
	if got["history_points"].(float64) != 5 {
		t.Errorf("history_points 注入不符: %v", got)
	}
	if got["auth_enabled"].(bool) != false {
		t.Errorf("auth_enabled 注入不符: %v", got)
	}
}

func TestChangePasswordFlow(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	if err := os.WriteFile(envPath, []byte("AUTH_PASSWORD=pw\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := authConfig("pw")
	c.EnvPath = envPath
	h := newTestHandler(t, c, nil)

	rec := doReq(h, "POST", "/api/auth/login", `{"password":"pw"}`, nil)
	cookie := sessionCookie(t, rec)

	// 未带会话 → 401
	if rec := doReq(h, "POST", "/api/auth/password", `{"old_password":"pw","new_password":"x1"}`, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("未认证改密应 401, got %d", rec.Code)
	}
	// 原密码错误 → 400
	rec = doReq(h, "POST", "/api/auth/password", `{"old_password":"bad","new_password":"new123"}`, cookie)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("原密码错误应 400, got %d", rec.Code)
	}
	// 新密码为空 / 与原密码相同 → 400
	if rec := doReq(h, "POST", "/api/auth/password", `{"old_password":"pw","new_password":""}`, cookie); rec.Code != http.StatusBadRequest {
		t.Fatalf("空新密码应 400, got %d", rec.Code)
	}
	if rec := doReq(h, "POST", "/api/auth/password", `{"old_password":"pw","new_password":"pw"}`, cookie); rec.Code != http.StatusBadRequest {
		t.Fatalf("新旧相同应 400, got %d", rec.Code)
	}
	// 成功 → 200，且 .env 已持久化
	rec = doReq(h, "POST", "/api/auth/password", `{"old_password":"pw","new_password":"new123"}`, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("改密成功应 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	data, _ := os.ReadFile(envPath)
	if !strings.Contains(string(data), "AUTH_PASSWORD=new123") {
		t.Errorf(".env 应持久化新密码: %s", data)
	}
	// 旧密码失效、新密码可用；改密不注销当前会话
	if rec := doReq(h, "POST", "/api/auth/login", `{"password":"pw"}`, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("旧密码应失效 401, got %d", rec.Code)
	}
	if rec := doReq(h, "POST", "/api/auth/login", `{"password":"new123"}`, nil); rec.Code != http.StatusOK {
		t.Fatalf("新密码应可登录 200, got %d", rec.Code)
	}
	if rec := doReq(h, "GET", "/api/system/info", "", cookie); rec.Code != http.StatusOK {
		t.Fatalf("改密后原会话应保持有效, got %d", rec.Code)
	}
}

func TestUpdateSettingsFlow(t *testing.T) {
	dir := t.TempDir()
	settingPath := filepath.Join(dir, "setting.json")
	if err := os.WriteFile(settingPath, []byte(`{"port": 8002, "enable_disk": true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	c := authConfig("pw")
	c.SettingPath = settingPath
	c.RefreshIntervalSeconds = 4
	c.HistoryPoints = 4
	h := newTestHandler(t, c, nil)

	rec := doReq(h, "POST", "/api/auth/login", `{"password":"pw"}`, nil)
	cookie := sessionCookie(t, rec)

	// 未带会话 → 401
	if rec := doReq(h, "POST", "/api/settings", `{"refresh_interval_seconds":3}`, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("未认证应 401, got %d", rec.Code)
	}
	// 越界 / 空 / 非法
	for _, bad := range []string{
		`{"refresh_interval_seconds":0}`, `{"refresh_interval_seconds":6}`,
		`{"history_points":1}`, `{"history_points":61}`,
		`{}`, `{"enable_disk":"yes"}`, `not-json`,
	} {
		if rec := doReq(h, "POST", "/api/settings", bad, cookie); rec.Code != http.StatusBadRequest {
			t.Fatalf("非法入参 %s 应 400, got %d", bad, rec.Code)
		}
	}
	// 部分字段成功 → 200；仅改提交项，其余保留
	rec = doReq(h, "POST", "/api/settings",
		`{"refresh_interval_seconds":3,"history_points":10,"enable_disk":false}`, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("成功应 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	data, _ := os.ReadFile(settingPath)
	if !strings.Contains(string(data), `"refresh_interval_seconds": 3`) ||
		!strings.Contains(string(data), `"history_points": 10`) ||
		!strings.Contains(string(data), `"enable_disk": false`) {
		t.Errorf("setting.json 应持久化提交项: %s", data)
	}
	if !strings.Contains(string(data), `"port": 8002`) {
		t.Error("未提交的 port 应保留")
	}
	if c.RefreshIntervalSeconds != 3 || c.HistoryPoints != 10 || c.EnableDisk {
		t.Errorf("内存配置应更新: %+v", c)
	}
	// 布尔开关提交后应可再次打开
	rec = doReq(h, "POST", "/api/settings", `{"enable_disk":true}`, cookie)
	if rec.Code != http.StatusOK || !c.EnableDisk {
		t.Fatalf("重新开启 enable_disk 应成功, code=%d", rec.Code)
	}
	// proc/fs 两个新开关可独立关闭/开启
	rec = doReq(h, "POST", "/api/settings", `{"enable_proc":false,"enable_fs":false}`, cookie)
	if rec.Code != http.StatusOK || c.EnableProc || c.EnableFS {
		t.Fatalf("关闭 enable_proc/enable_fs 应成功, code=%d", rec.Code)
	}
	rec = doReq(h, "POST", "/api/settings", `{"enable_proc":true,"enable_fs":true}`, cookie)
	if rec.Code != http.StatusOK || !c.EnableProc || !c.EnableFS {
		t.Fatalf("重新开启 enable_proc/enable_fs 应成功, code=%d", rec.Code)
	}
}

// F-001：/api/* 的错误（含 ServeMux 自动生成的 405/404 纯文本）必须统一为 JSON。
func TestAPIErrorsAreJSON(t *testing.T) {
	c := authConfig("pw")
	h := newTestHandler(t, c, nil)

	rec := doReq(h, "POST", "/api/auth/login", `{"password":"pw"}`, nil)
	cookie := sessionCookie(t, rec)

	assertJSONErr := func(t *testing.T, method, path string, wantCode int) {
		t.Helper()
		r := doReq(h, method, path, "", cookie)
		if r.Code != wantCode {
			t.Fatalf("%s %s 应 %d, got %d", method, path, wantCode, r.Code)
		}
		if ct := r.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Fatalf("%s %s 错误响应应为 JSON, got Content-Type %q", method, path, ct)
		}
		var e map[string]string
		if err := json.Unmarshal(r.Body.Bytes(), &e); err != nil || e["error"] == "" {
			t.Fatalf("%s %s 应为 {\"error\":...}, body=%s", method, path, r.Body.String())
		}
	}

	assertJSONErr(t, "PUT", "/api/system/info", http.StatusMethodNotAllowed)
	assertJSONErr(t, "GET", "/api/auth/logout", http.StatusMethodNotAllowed)
	assertJSONErr(t, "DELETE", "/api/settings", http.StatusMethodNotAllowed)
	assertJSONErr(t, "PUT", "/api/nope", http.StatusMethodNotAllowed)
	assertJSONErr(t, "GET", "/api/nope", http.StatusNotFound)
}
