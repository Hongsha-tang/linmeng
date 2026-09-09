package http

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"linmeng/internal/config"
)

// sessionCookieName 会话 Cookie 键名（HttpOnly，局域网 HTTP）。
const sessionCookieName = "session_id"

// 会话生命周期（产品要求）：服务端不设过期时间；Cookie 为浏览器会话级
// （不写 Max-Age/Expires），关闭浏览器/标签页即失效，仅刷新不退出；
// 主动登出删除会话。sessionStore 仅防内存无限增长：超上限清理最旧会话。
const sessionStoreCap = 256

type sessionEntry struct {
	createdAt time.Time
}

// sessionStore 会话存储（内存 map + 互斥锁）。
type sessionStore struct {
	mu      sync.Mutex
	entries map[string]sessionEntry
}

func newSessionStore() *sessionStore {
	return &sessionStore{entries: map[string]sessionEntry{}}
}

func (st *sessionStore) add(id string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.entries) >= sessionStoreCap {
		st.pruneOldestLocked()
	}
	st.entries[id] = sessionEntry{createdAt: time.Now()}
}

// valid 判定会话是否存在（无过期概念；登出即删除）。
func (st *sessionStore) valid(id string) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	_, ok := st.entries[id]
	return ok
}

func (st *sessionStore) remove(id string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	delete(st.entries, id)
}

// pruneOldestLocked 需在持锁时调用：超出容量时删除创建最早的会话。
func (st *sessionStore) pruneOldestLocked() {
	if len(st.entries) < sessionStoreCap {
		return
	}
	var oldestID string
	var oldestAt time.Time
	first := true
	for id, e := range st.entries {
		if first || e.createdAt.Before(oldestAt) {
			oldestID = id
			oldestAt = e.createdAt
			first = false
		}
	}
	if oldestID != "" {
		delete(st.entries, oldestID)
	}
}

// handleLogin 校验密码并发放会话 Cookie。成功返回 200 与空对象 {}。
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Body != nil {
		defer r.Body.Close()
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)

	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeJSONError(w, http.StatusBadRequest, "请求体格式错误")
		return
	}

	if !s.cfg.AuthEnabled {
		// 鉴权关闭：登录接口幂等成功，前端统一处理。
		s.writeJSON(w, http.StatusOK, struct{}{})
		return
	}
	s.cfg.RLock()
	authed := secureEqual(req.Password, s.cfg.AuthPassword)
	s.cfg.RUnlock()
	if !authed {
		s.log.Printf("WARN 登录失败（密码错误），remote=%s", r.RemoteAddr)
		s.writeJSONError(w, http.StatusUnauthorized, "未授权")
		return
	}

	id, err := newSessionID()
	if err != nil {
		s.writeJSONError(w, http.StatusInternalServerError, "创建会话失败")
		return
	}
	s.sessions.add(id)
	s.setSessionCookie(w, id)
	s.log.Printf("INFO 登录成功，remote=%s", r.RemoteAddr)
	s.writeJSON(w, http.StatusOK, struct{}{})
}

// handleLogout 销毁当前会话并清除 Cookie。
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookieName); err == nil {
		s.sessions.remove(c.Value)
	}
	s.clearSessionCookie(w)
	s.writeJSON(w, http.StatusOK, struct{}{})
}

func (s *Server) setSessionCookie(w http.ResponseWriter, id string) {
	// 浏览器会话级 Cookie：不写 Max-Age/Expires，关闭浏览器/标签页即失效；
	// 仅刷新页面不会退出（产品要求，取消服务端 TTL 过期）。
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    id,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
		Expires:  time.Unix(1, 0),
	})
}

// newSessionID 生成 32 位十六进制随机会话标识。
func newSessionID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// handleChangePassword 修改登录密码：需有效会话 + 原密码正确；
// 成功即回写 .env 持久化并更新内存，当前会话保持有效。
func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.AuthEnabled {
		s.writeJSONError(w, http.StatusBadRequest, "鉴权未开启，无需修改密码")
		return
	}
	if r.Body != nil {
		defer r.Body.Close()
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)

	var req struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeJSONError(w, http.StatusBadRequest, "请求体格式错误")
		return
	}
	// 写锁：校验原密码、持久化与内存更新在锁内完成（F-003）。
	s.cfg.Lock()
	defer s.cfg.Unlock()
	if !secureEqual(req.OldPassword, s.cfg.AuthPassword) {
		s.writeJSONError(w, http.StatusBadRequest, "原密码错误")
		return
	}
	if req.NewPassword == "" {
		s.writeJSONError(w, http.StatusBadRequest, "新密码不能为空")
		return
	}
	if req.NewPassword == req.OldPassword {
		s.writeJSONError(w, http.StatusBadRequest, "新密码不能与原密码相同")
		return
	}
	if err := config.UpdateAuthPassword(s.cfg.EnvPath, req.NewPassword); err != nil {
		s.log.Printf("ERROR 修改密码持久化失败: %v", err)
		s.writeJSONError(w, http.StatusInternalServerError, "密码保存失败")
		return
	}
	s.cfg.AuthPassword = req.NewPassword
	s.log.Printf("INFO 登录密码已修改，remote=%s", r.RemoteAddr)
	s.writeJSON(w, http.StatusOK, struct{}{})
}

// handleUpdateSettings 修改运行时允许的配置（部分字段可提交）：
// refresh_interval_seconds(1–5)、history_points(2–60)、enable_* 八个模块开关。
// 成功后合并回写 setting.json 并更新内存（采集器/前端读同一配置指针即时生效）。
func (s *Server) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	if r.Body != nil {
		defer r.Body.Close()
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)

	var p struct {
		RefreshIntervalSeconds *int  `json:"refresh_interval_seconds"`
		HistoryPoints          *int  `json:"history_points"`
		EnableCPU              *bool `json:"enable_cpu"`
		EnableMemory           *bool `json:"enable_memory"`
		EnableDisk             *bool `json:"enable_disk"`
		EnableNetwork          *bool `json:"enable_network"`
		EnableHost             *bool `json:"enable_host"`
		EnableGPU              *bool `json:"enable_gpu"`
		EnableProc             *bool `json:"enable_proc"`
		EnableFS               *bool `json:"enable_fs"`
	}
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		s.writeJSONError(w, http.StatusBadRequest, "请求体格式错误")
		return
	}

	// 写锁：内存字段更新与持久化在锁内完成，与后台采集读路径互斥（F-003）。
	s.cfg.Lock()
	defer s.cfg.Unlock()

	patch := map[string]any{}
	if p.RefreshIntervalSeconds != nil {
		if *p.RefreshIntervalSeconds < 1 || *p.RefreshIntervalSeconds > 5 {
			s.writeJSONError(w, http.StatusBadRequest, "刷新间隔需为 1–5 的整数（秒）")
			return
		}
		patch["refresh_interval_seconds"] = *p.RefreshIntervalSeconds
		s.cfg.RefreshIntervalSeconds = *p.RefreshIntervalSeconds
	}
	if p.HistoryPoints != nil {
		if *p.HistoryPoints < 2 || *p.HistoryPoints > 60 {
			s.writeJSONError(w, http.StatusBadRequest, "曲线点数需为 2–60 的整数")
			return
		}
		patch["history_points"] = *p.HistoryPoints
		s.cfg.HistoryPoints = *p.HistoryPoints
	}
	applyBool := func(cur *bool, key string, v *bool) {
		if v != nil {
			*cur = *v
			patch[key] = *v
		}
	}
	applyBool(&s.cfg.EnableCPU, "enable_cpu", p.EnableCPU)
	applyBool(&s.cfg.EnableMemory, "enable_memory", p.EnableMemory)
	applyBool(&s.cfg.EnableDisk, "enable_disk", p.EnableDisk)
	applyBool(&s.cfg.EnableNetwork, "enable_network", p.EnableNetwork)
	applyBool(&s.cfg.EnableHost, "enable_host", p.EnableHost)
	applyBool(&s.cfg.EnableGPU, "enable_gpu", p.EnableGPU)
	applyBool(&s.cfg.EnableProc, "enable_proc", p.EnableProc)
	applyBool(&s.cfg.EnableFS, "enable_fs", p.EnableFS)

	if len(patch) == 0 {
		s.writeJSONError(w, http.StatusBadRequest, "未包含可修改的配置项")
		return
	}
	if err := config.UpdateSettings(s.cfg.SettingPath, patch); err != nil {
		s.log.Printf("ERROR 配置持久化失败: %v", err)
		s.writeJSONError(w, http.StatusInternalServerError, "配置保存失败")
		return
	}
	s.log.Printf("INFO 运行配置已修改：%v，remote=%s", patch, r.RemoteAddr)
	s.writeJSON(w, http.StatusOK, struct{}{})
}

// secureEqual 恒定时间比较（先 SHA-256 归一化长度）。
func secureEqual(a, b string) bool {
	ha := sha256.Sum256([]byte(a))
	hb := sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(ha[:], hb[:]) == 1
}
