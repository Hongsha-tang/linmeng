// Package http 提供接口层：路由注册、鉴权中间件、登录/登出、
// 内嵌仪表盘页面渲染与系统快照 JSON 接口。
// 接口契约见 docs/api-reference.md（单一信息源）。
package http

import (
	"bytes"
	"encoding/json"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"linmeng/internal/collector"
	"linmeng/internal/config"
)

// Snapshotter 抽象快照采集动作，便于测试注入桩实现（依赖倒置）。
type Snapshotter interface {
	Snapshot() (collector.Snapshot, error)
}

// Server 持有接口层依赖。
type Server struct {
	cfg      *config.Config
	snap     Snapshotter
	pages    fs.FS
	sessions *sessionStore
	log      *log.Logger
}

// NewServer 构造接口层服务。pages 为 go:embed 的静态资源文件系统。
func NewServer(cfg *config.Config, snap Snapshotter, pages fs.FS) *Server {
	return &Server{
		cfg:      cfg,
		snap:     snap,
		pages:    pages,
		sessions: newSessionStore(),
		log:      log.New(os.Stdout, "linmeng ", log.LstdFlags),
	}
}

// Handler 注册全部路由（Go 1.22+ 方法路由）。
// 返回的处理器已套 /api/* JSON 错误包装层（F-001）：ServeMux 对未登记方法自动
// 生成的 405/404 纯文本响应统一改写为 {"error":...}，保证接口错误均为 JSON。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /login", s.handleIndex)
	mux.HandleFunc("POST /api/auth/login", s.handleLogin)
	mux.HandleFunc("POST /api/auth/logout", s.requireAuth(s.handleLogout))
	mux.HandleFunc("POST /api/auth/password", s.requireAuth(s.handleChangePassword))
	mux.HandleFunc("POST /api/settings", s.requireAuth(s.handleUpdateSettings))
	mux.HandleFunc("GET /api/system/info", s.requireAuth(s.handleSystemInfo))
	// 已知接口 + 非支持方法：显式返回 JSON 405（避免被 GET / 通配路由吞成 404）。
	mux.HandleFunc("GET /api/auth/logout", s.methodNotAllowedJSON)
	mux.HandleFunc("GET /api/auth/password", s.methodNotAllowedJSON)
	mux.HandleFunc("GET /api/settings", s.methodNotAllowedJSON)
	// 公开静态资源：登录页自身加载 style.css/app.js 时尚未建立会话，
	// 不能走鉴权重定向（否则浏览器拿到 HTML 文本导致页面无样式、JS 失效）。
	mux.HandleFunc("GET /style.css", s.handleIndex)
	mux.HandleFunc("GET /app.js", s.handleIndex)
	// 页面路由：未登录 302 → /login。
	mux.HandleFunc("GET /", s.requirePage(s.handleIndex))
	return apiJSONErrors(mux)
}

// apiJSONErrors 将 /api/* 下由 ServeMux 兜底产生的非 JSON 错误（404/405 文本）
// 改写为标准 JSON {"error":...}；已是 JSON 的响应原样透传。
func apiJSONErrors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		code := rec.status
		if code == 0 {
			code = http.StatusOK
		}
		if code >= 400 && !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
			msg := map[int]string{
				http.StatusBadRequest:          "参数错误",
				http.StatusUnauthorized:        "未授权",
				http.StatusNotFound:            "接口不存在",
				http.StatusMethodNotAllowed:    "方法不允许",
				http.StatusInternalServerError: "服务器内部错误",
			}[code]
			if msg == "" {
				msg = "请求失败"
			}
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(code)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
			return
		}
		w.WriteHeader(code)
		if len(rec.body) > 0 {
			_, _ = w.Write(rec.body)
		}
	})
}

// statusRecorder 缓冲响应，便于统一改写非 JSON 错误。
type statusRecorder struct {
	http.ResponseWriter
	status int
	body   []byte
}

func (r *statusRecorder) WriteHeader(code int) {
	if r.status != 0 {
		return
	}
	r.status = code
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	r.body = append(r.body, b...)
	return len(b), nil
}

// handleIndex 渲染仪表盘页（/ 与 /login 共用同一内嵌页面，前端按路径区分视图），
// 并支持直接访问内嵌静态资源（/style.css、/app.js 等）。
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/")
	switch name {
	case "", "login":
		name = "index.html"
	}
	if strings.HasPrefix(name, "api/") {
		s.writeJSONError(w, http.StatusNotFound, "接口不存在")
		return
	}
	if s.pages == nil {
		http.NotFound(w, r)
		return
	}
	// index.html：注入前端运行配置（轮询间隔/曲线点数/鉴权开关），保持 API 面不变。
	if name == "index.html" {
		data, err := fs.ReadFile(s.pages, name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		data = s.injectFrontConfig(data)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(data)
		return
	}
	f, err := s.pages.Open(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()

	if ct := contentTypeByExt(name); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if seeker, ok := f.(io.ReadSeeker); ok {
		http.ServeContent(w, r, name, time.Time{}, seeker)
		return
	}
	data, err := io.ReadAll(f)
	if err != nil {
		http.Error(w, "读取资源失败", http.StatusInternalServerError)
		return
	}
	_, _ = w.Write(data)
}

// cfgMarker 在 index.html 中以字符串形式出现；服务端替换为转义后的配置 JSON，
// 供前端 JSON.parse 使用（本地直开文件时解析失败则回退默认值）。
const cfgMarker = "__LM_CFG_JSON__"

// injectFrontConfig 将服务端配置写入页面（非机密子集：轮询间隔/曲线点数/鉴权开关）。
func (s *Server) injectFrontConfig(data []byte) []byte {
	s.cfg.RLock()
	payload, err := json.Marshal(map[string]any{
		"refresh_interval_seconds": s.cfg.RefreshIntervalSeconds,
		"history_points":           s.cfg.HistoryPoints,
		"auth_enabled":             s.cfg.AuthEnabled,
		"enable_cpu":               s.cfg.EnableCPU,
		"enable_memory":            s.cfg.EnableMemory,
		"enable_disk":              s.cfg.EnableDisk,
		"enable_network":           s.cfg.EnableNetwork,
		"enable_host":              s.cfg.EnableHost,
		"enable_gpu":               s.cfg.EnableGPU,
		"enable_proc":              s.cfg.EnableProc,
		"enable_fs":                s.cfg.EnableFS,
	})
	s.cfg.RUnlock()
	if err != nil {
		return data
	}
	// strconv.Quote 转义内嵌双引号；去掉其首尾包裹引号后嵌回模板字符串内。
	inner := strconv.Quote(string(payload))
	inner = inner[1 : len(inner)-1]
	return bytes.ReplaceAll(data, []byte(cfgMarker), []byte(inner))
}

// handleSystemInfo 采集并返回一次完整系统快照（数据体，无信封）。
func (s *Server) handleSystemInfo(w http.ResponseWriter, r *http.Request) {
	snap, err := s.snap.Snapshot()
	if err != nil {
		s.log.Printf("WARN 采集快照失败: %v", err)
		s.writeJSONError(w, http.StatusInternalServerError, "系统信息采集失败")
		return
	}
	s.writeJSON(w, http.StatusOK, snap)
}

func (s *Server) methodNotAllowedJSON(w http.ResponseWriter, r *http.Request) {
	s.writeJSONError(w, http.StatusMethodNotAllowed, "方法不允许")
}

// requireAuth 保护 JSON 接口：未认证返回 401 {"error":"未授权"}。
func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.authorized(r) {
			s.writeJSONError(w, http.StatusUnauthorized, "未授权")
			return
		}
		next(w, r)
	}
}

// requirePage 保护页面：未登录 302 跳转 /login；/api/* 前缀按接口语义返回 401 JSON。
func (s *Server) requirePage(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.authorized(r) {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				s.writeJSONError(w, http.StatusUnauthorized, "未授权")
				return
			}
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		next(w, r)
	}
}

// authorized 判定当前请求是否已认证。auth_enabled=false 时免认证放行。
// 会话无过期概念：存在即有效，登出删除；Cookie 为浏览器会话级（关浏览器失效）。
func (s *Server) authorized(r *http.Request) bool {
	if !s.cfg.AuthEnabled {
		return true
	}
	c, err := r.Cookie(sessionCookieName)
	if err != nil {
		return false
	}
	return s.sessions.valid(c.Value)
}

func (s *Server) writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

func (s *Server) writeJSONError(w http.ResponseWriter, code int, msg string) {
	s.writeJSON(w, code, map[string]string{"error": msg})
}

// contentTypeByExt 为内嵌静态资源设置 Content-Type，避免嗅探误判。
func contentTypeByExt(name string) string {
	switch {
	case strings.HasSuffix(name, ".html"):
		return "text/html; charset=utf-8"
	case strings.HasSuffix(name, ".css"):
		return "text/css; charset=utf-8"
	case strings.HasSuffix(name, ".js"):
		return "text/javascript; charset=utf-8"
	case strings.HasSuffix(name, ".svg"):
		return "image/svg+xml"
	case strings.HasSuffix(name, ".json"):
		return "application/json; charset=utf-8"
	}
	return ""
}
