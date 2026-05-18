package main

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const defaultAddr = ":29173"
const journalName = "__journal__"

var indexTemplate = template.Must(template.New("index").Parse(indexHTML))
var serviceNamePattern = regexp.MustCompile(`^[A-Za-z0-9_.@-]+$`)

//go:embed assets/admin-avatar.png
var adminAvatar []byte

type authStore struct {
	Salt string `json:"salt"`
	Hash string `json:"hash"`
}

type serverState struct {
	rootDir    string
	authPath   string
	listenAddr string
	service    string
	sessions   map[string]time.Time
	loginFails map[string]loginFail
	mu         sync.Mutex
}

type loginFail struct {
	Count     int
	LockedTil time.Time
}

type logFile struct {
	Name    string `json:"name"`
	Label   string `json:"label"`
	Size    int64  `json:"size"`
	ModTime string `json:"modTime"`
}

func main() {
	addr := flag.String("addr", defaultAddr, "public listen address for VPS web ui")
	root := flag.String("root", "/opt/Openxhh", "Openxhh working directory")
	service := flag.String("service", "Openxhh", "systemd service name")
	flag.Parse()

	if err := validateServiceName(*service); err != nil {
		log.Fatal(err)
	}
	rootDir, err := filepath.Abs(*root)
	if err != nil {
		log.Fatal(err)
	}
	state := &serverState{
		rootDir:    rootDir,
		authPath:   filepath.Join(rootDir, "webui_auth.json"),
		service:    *service,
		sessions:   map[string]time.Time{},
		loginFails: map[string]loginFail{},
	}

	password, created, err := ensureAuth(state.authPath)
	if err != nil {
		log.Fatal(err)
	}
	if created {
		fmt.Println("Openxhh VPS Web UI 已生成随机强密码")
		fmt.Println("登录密码:", password)
		fmt.Println("请立即保存；如需重置，停止 Web UI 后删除 webui_auth.json 再启动。")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/assets/admin-avatar.png", handleAdminAvatar)
	mux.HandleFunc("/", state.handleIndex)
	mux.HandleFunc("/login", state.handleLogin)
	mux.HandleFunc("/logout", state.requireAuth(state.handleLogout))
	mux.HandleFunc("/api/status", state.requireAuth(state.handleStatus))
	mux.HandleFunc("/api/start", state.requireAuth(state.handleStart))
	mux.HandleFunc("/api/stop", state.requireAuth(state.handleStop))
	mux.HandleFunc("/api/restart", state.requireAuth(state.handleRestart))
	mux.HandleFunc("/api/logs", state.requireAuth(state.handleLogs))
	mux.HandleFunc("/api/logs/read", state.requireAuth(state.handleReadLog))

	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("监听 %s 失败: %v", *addr, err)
	}
	state.listenAddr = listener.Addr().String()
	fmt.Printf("Openxhh VPS Web UI: http://%s\n", publicAddr(state.listenAddr))
	fmt.Printf("服务名: %s\n", state.service)
	fmt.Printf("工作目录: %s\n", state.rootDir)
	log.Fatal(http.Serve(listener, withSecurityHeaders(mux)))
}

func validateServiceName(service string) error {
	if service == "" || strings.HasPrefix(service, "-") || !serviceNamePattern.MatchString(service) {
		return fmt.Errorf("systemd 服务名无效: %q", service)
	}
	return nil
}

func ensureAuth(path string) (string, bool, error) {
	if _, err := os.Stat(path); err == nil {
		return "", false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return "", false, err
	}
	password, err := randomPassword(32)
	if err != nil {
		return "", false, err
	}
	saltBytes := make([]byte, 32)
	if _, err := rand.Read(saltBytes); err != nil {
		return "", false, err
	}
	salt := hex.EncodeToString(saltBytes)
	store := authStore{Salt: salt, Hash: hashPassword(password, salt)}
	data, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return "", false, err
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		return "", false, err
	}
	return password, true, nil
}

func randomPassword(length int) (string, error) {
	buf := make([]byte, length)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func hashPassword(password, salt string) string {
	digest := sha256.Sum256([]byte(salt + ":" + password))
	for i := 0; i < 120000; i++ {
		h := sha256.New()
		_, _ = h.Write([]byte(salt))
		_, _ = h.Write([]byte(password))
		_, _ = h.Write(digest[:])
		copy(digest[:], h.Sum(nil))
	}
	return hex.EncodeToString(digest[:])
}

func (s *serverState) validPassword(password string) bool {
	data, err := os.ReadFile(s.authPath)
	if err != nil {
		return false
	}
	var store authStore
	if err := json.Unmarshal(data, &store); err != nil {
		return false
	}
	actual := hashPassword(password, store.Salt)
	return subtle.ConstantTimeCompare([]byte(actual), []byte(store.Hash)) == 1
}

func (s *serverState) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = indexTemplate.Execute(w, map[string]any{"Authed": s.validSession(r), "Service": s.service})
}

func handleAdminAvatar(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	http.ServeContent(w, r, "admin-avatar.png", time.Time{}, bytes.NewReader(adminAvatar))
}

func (s *serverState) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ip := clientIP(r)
	if lockedUntil := s.lockedUntil(ip); !lockedUntil.IsZero() {
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"ok": false, "error": "登录失败过多，请稍后再试"})
		return
	}
	var payload struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "请求格式错误"})
		return
	}
	if !s.validPassword(payload.Password) {
		s.recordLoginFailure(ip)
		writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "密码错误"})
		return
	}
	s.clearLoginFailure(ip)
	token, err := randomPassword(48)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "无法创建会话"})
		return
	}
	s.mu.Lock()
	s.sessions[token] = time.Now().Add(24 * time.Hour)
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{
		Name:     "xhh_vps_webui_session",
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Expires:  time.Now().Add(24 * time.Hour),
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *serverState) lockedUntil(ip string) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	fail := s.loginFails[ip]
	if fail.LockedTil.After(time.Now()) {
		return fail.LockedTil
	}
	return time.Time{}
}

func (s *serverState) recordLoginFailure(ip string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fail := s.loginFails[ip]
	fail.Count++
	if fail.Count >= 5 {
		fail.Count = 0
		fail.LockedTil = time.Now().Add(5 * time.Minute)
	}
	s.loginFails[ip] = fail
}

func (s *serverState) clearLoginFailure(ip string) {
	s.mu.Lock()
	delete(s.loginFails, ip)
	s.mu.Unlock()
}

func (s *serverState) validSession(r *http.Request) bool {
	cookie, err := r.Cookie("xhh_vps_webui_session")
	if err != nil || cookie.Value == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	expiresAt, ok := s.sessions[cookie.Value]
	if !ok || time.Now().After(expiresAt) {
		delete(s.sessions, cookie.Value)
		return false
	}
	return true
}

func (s *serverState) handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie("xhh_vps_webui_session"); err == nil {
		s.mu.Lock()
		delete(s.sessions, cookie.Value)
		s.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: "xhh_vps_webui_session", Path: "/", MaxAge: -1})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *serverState) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.validSession(r) {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "未登录"})
			return
		}
		next(w, r)
	}
}

func (s *serverState) handleStatus(w http.ResponseWriter, r *http.Request) {
	active, activeErr := s.systemctl("is-active")
	status, statusErr := s.systemctl("status", "--no-pager")
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"service":    s.service,
		"running":    strings.TrimSpace(active) == "active",
		"active":     strings.TrimSpace(active),
		"detail":     errorText(activeErr),
		"rootDir":    s.rootDir,
		"listenAddr": s.listenAddr,
		"statusText": trimStatus(firstNonEmpty(status, errorText(statusErr))),
	})
}

func (s *serverState) handleStart(w http.ResponseWriter, r *http.Request) {
	s.handleSystemctlAction(w, r, "start")
}

func (s *serverState) handleStop(w http.ResponseWriter, r *http.Request) {
	s.handleSystemctlAction(w, r, "stop")
}

func (s *serverState) handleRestart(w http.ResponseWriter, r *http.Request) {
	s.handleSystemctlAction(w, r, "restart")
}

func (s *serverState) handleSystemctlAction(w http.ResponseWriter, r *http.Request, action string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	out, err := s.systemctl(action)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": out})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *serverState) systemctl(args ...string) (string, error) {
	cmdArgs := append(args, s.service)
	cmd := exec.Command("systemctl", cmdArgs...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return strings.TrimSpace(buf.String()), err
}

func (s *serverState) handleLogs(w http.ResponseWriter, r *http.Request) {
	files := []logFile{{Name: journalName, Label: "systemd journal · " + s.service}}
	logFiles, _ := listLogFiles(filepath.Join(s.rootDir, "log"))
	files = append(files, logFiles...)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "files": files})
}

func listLogFiles(logDir string) ([]logFile, error) {
	entries, err := os.ReadDir(logDir)
	if err != nil {
		return nil, err
	}
	files := make([]logFile, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".log") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		files = append(files, logFile{
			Name:    entry.Name(),
			Label:   entry.Name(),
			Size:    info.Size(),
			ModTime: info.ModTime().Format("2006-01-02 15:04:05"),
		})
	}
	sort.Slice(files, func(i, j int) bool {
		return files[i].ModTime > files[j].ModTime
	})
	return files, nil
}

func (s *serverState) handleReadLog(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("file")
	if name == journalName || name == "" {
		content, err := s.readJournal()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": content})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "content": content})
		return
	}
	if strings.Contains(name, "/") || strings.Contains(name, "\\") || strings.Contains(name, "..") {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "日志文件名无效"})
		return
	}
	content, err := tailFile(filepath.Join(s.rootDir, "log", name), 800)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "content": content})
}

func (s *serverState) readJournal() (string, error) {
	cmd := exec.Command("journalctl", "-u", s.service, "-n", "800", "--no-pager", "-o", "short-iso")
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return strings.TrimRight(buf.String(), "\n"), err
}

func tailFile(path string, maxLines int) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	var lines []string
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
		if len(lines) > maxLines {
			copy(lines, lines[1:])
			lines = lines[:maxLines]
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return strings.Join(lines, "\n"), nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func withSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "geolocation=(), microphone=(), camera=()")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; connect-src 'self'; img-src 'self' data:")
		next.ServeHTTP(w, r)
	})
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func trimStatus(status string) string {
	lines := strings.Split(status, "\n")
	if len(lines) > 24 {
		lines = lines[:24]
	}
	return strings.Join(lines, "\n")
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func publicAddr(addr string) string {
	if strings.HasPrefix(addr, "[::]:") {
		return "服务器IP" + strings.TrimPrefix(addr, "[::]")
	}
	if strings.HasPrefix(addr, "0.0.0.0:") {
		return "服务器IP" + strings.TrimPrefix(addr, "0.0.0.0")
	}
	return addr
}

const indexHTML = `<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>小黑盒猫娘控制台</title>
  <style>
    :root{color-scheme:light;--bg:#f2f5f9;--paper:#fff;--ink:#111827;--muted:#667085;--line:#e5eaf2;--dark:#111820;--green:#08b99e;--blue:#1684e2;--amber:#ffc45c;--red:#de3038;--violet:#6657ff;--shadow:0 16px 42px rgba(36,50,74,.12);--soft:0 8px 22px rgba(36,50,74,.08)}
    *{box-sizing:border-box}body{margin:0;min-height:100vh;font-family:"Microsoft YaHei UI","Microsoft YaHei",ui-sans-serif,system-ui,sans-serif;color:var(--ink);background:radial-gradient(circle at 12% -8%,rgba(255,197,213,.32),transparent 26rem),radial-gradient(circle at 88% -4%,rgba(22,132,226,.12),transparent 30rem),linear-gradient(180deg,#f8fafc 0%,var(--bg) 100%)}
    body:before{content:"";position:fixed;inset:0;pointer-events:none;background-image:linear-gradient(90deg,rgba(255,255,255,.55) 1px,transparent 1px),linear-gradient(rgba(255,255,255,.55) 1px,transparent 1px);background-size:30px 30px;mask-image:linear-gradient(to bottom,rgba(0,0,0,.28),transparent 58%)}
    button,input,select{font:inherit}.hidden{display:none!important}.shell{position:relative;width:min(1420px,calc(100vw - 56px));margin:0 auto;padding:18px 0 48px}.topnav{height:64px;display:flex;align-items:center;justify-content:space-between;gap:18px;padding:0 16px 0 20px;border:1px solid #dfe6ef;border-radius:28px;background:rgba(255,255,255,.84);box-shadow:var(--soft);backdrop-filter:blur(16px);position:sticky;top:14px;z-index:5}.brand{display:flex;align-items:center;gap:12px;min-width:220px}.logo{width:38px;height:38px;border-radius:14px;background:linear-gradient(145deg,#fff1f6,#ffd7e5);box-shadow:inset 0 -8px 18px rgba(255,135,174,.2);display:grid;place-items:center;color:#d45d88;font-weight:900}.brand strong{font-size:18px}.brand small{color:var(--muted);font-size:12px}.navlinks{display:flex;align-items:center;gap:8px;flex:1;justify-content:center}.navlinks button{border:0;background:transparent;color:#4b5563;border-radius:999px;padding:10px 16px;cursor:pointer;font-weight:800}.navlinks button.active{background:#eef3f8;color:#111827;box-shadow:inset 0 0 0 1px #e3eaf3}.right-tools{display:flex;align-items:center;gap:10px}.tool-pill{display:inline-flex;align-items:center;gap:8px;border:1px solid #dfe6ef;border-radius:999px;background:#fff;padding:9px 13px;color:#475467;font-weight:800}.avatar-button{width:42px;height:42px;border:3px solid #fff;border-radius:50%;padding:0;background:#fff;box-shadow:0 8px 20px rgba(36,50,74,.16);overflow:hidden;cursor:pointer}.avatar-button.active{outline:4px solid rgba(22,132,226,.14)}.avatar-button img{width:100%;height:100%;object-fit:cover;display:block}.dot{width:9px;height:9px;border-radius:50%;background:var(--red);box-shadow:0 0 0 5px rgba(222,48,56,.12)}.dot.on{background:var(--green);box-shadow:0 0 0 5px rgba(8,185,158,.13)}
    .login{min-height:72vh;display:grid;place-items:center}.login-card{width:min(470px,100%);padding:36px;border-radius:28px;background:var(--paper);box-shadow:var(--shadow);text-align:center}.catgirl{position:relative;width:126px;height:126px;margin:0 auto 18px;border-radius:40px;background:linear-gradient(145deg,#fff8fb,#ffe4ef 48%,#fff);box-shadow:inset 0 -12px 30px rgba(255,156,183,.22),var(--soft);display:grid;place-items:center;color:#d35d88;font-size:28px;font-weight:900}.catgirl:before,.catgirl:after{content:"";position:absolute;top:-12px;width:46px;height:46px;background:#ffe3ee;border:7px solid #fff;border-radius:14px;transform:rotate(45deg);box-shadow:var(--soft)}.catgirl:before{left:14px}.catgirl:after{right:14px}.catgirl b{position:relative;z-index:1}.login-card h1{margin:0 0 8px;font-size:30px}.login-card p{margin:0 0 22px;color:var(--muted);line-height:1.7}.input,select{width:100%;border:1px solid var(--line);background:#fbfcfe;color:var(--ink);border-radius:16px;padding:14px 15px;outline:none}.input:focus,select:focus{border-color:rgba(22,132,226,.55);box-shadow:0 0 0 4px rgba(22,132,226,.09)}.toast{min-height:22px;margin-top:14px;color:var(--red);font-size:13px}
    .layout{display:grid;grid-template-columns:260px 1fr;gap:26px;margin-top:24px}.side{padding:18px}.new-chat{width:100%;height:46px;border:0;border-radius:22px;background:var(--dark);color:#fff;font-weight:900;cursor:pointer;box-shadow:var(--soft)}.side-card{margin-top:14px;padding:16px;border-radius:18px;background:#fff;box-shadow:var(--soft)}.side-card strong{display:block;margin-bottom:8px}.side-card p{margin:0;color:var(--muted);font-size:13px;line-height:1.5}.content{min-width:0}.view{display:none}.view.active{display:block}.hero-card{padding:24px 26px;border-radius:26px;background:#fff;box-shadow:var(--shadow)}.hero-head{display:flex;align-items:center;justify-content:space-between;gap:16px}.hero-title h1{margin:0;font-size:28px}.hero-title p{margin:7px 0 0;color:var(--muted)}.panel-actions{display:flex;gap:10px;flex-wrap:wrap}button.primary,button.secondary,button.danger,button.warn{border:0;cursor:pointer;border-radius:14px;padding:12px 17px;font-weight:900;transition:.18s ease}button.primary{color:#fff;background:var(--blue);box-shadow:0 8px 18px rgba(22,132,226,.2)}button.secondary{color:#2563eb;background:#edf6ff}button.danger{color:#fff;background:var(--red);box-shadow:0 8px 18px rgba(222,48,56,.18)}button.warn{color:#5a3a00;background:var(--amber);box-shadow:0 8px 18px rgba(255,196,92,.22)}button:hover{transform:translateY(-1px);filter:brightness(1.03)}button:disabled{opacity:.45;cursor:not-allowed;transform:none}.cards{display:grid;grid-template-columns:repeat(5,minmax(138px,1fr));gap:20px;margin-top:24px}.card{background:#fff;border-radius:22px;box-shadow:var(--shadow);border:1px solid rgba(255,255,255,.8)}.stat{min-height:124px;min-width:0;padding:20px;text-align:center;display:grid;align-content:center;gap:12px;overflow:hidden}.stat span{color:#4c5566;font-size:16px}.stat strong{max-width:100%;font-size:clamp(30px,3vw,42px);line-height:1;font-weight:900;letter-spacing:-.05em;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}.green{color:var(--green)}.blue{color:var(--blue)}.red{color:var(--red)}.amber{color:#f7b23c}.violet{color:var(--violet)}.grid-2{display:grid;grid-template-columns:1fr 1fr;gap:24px;margin-top:24px}.panel{padding:24px}.panel-head{display:flex;align-items:center;justify-content:space-between;gap:16px;margin-bottom:18px}.panel h2{margin:0;font-size:22px}.panel p{margin:6px 0 0;color:var(--muted)}.control-grid{display:grid;grid-template-columns:150px 1fr;gap:20px;align-items:center}.meta{display:grid;gap:12px}.meta div{display:grid;gap:4px}.meta span{font-size:12px;color:var(--muted)}.meta strong{font-size:13px;word-break:break-all;white-space:pre-wrap}.status-text{max-height:130px;overflow:auto}.warnbox{border:1px solid #ffe0a3;background:#fff8e8;color:#7a4f00;border-radius:14px;padding:12px 14px;margin-top:16px;font-size:13px;line-height:1.55}.chart{height:220px;border-top:1px solid var(--line);display:grid;grid-template-columns:repeat(7,1fr);align-items:end;gap:18px;padding:24px 12px 4px}.bar-wrap{text-align:center;color:var(--muted);font-size:13px}.bar-num{height:22px;color:#4c5566}.bar{width:58px;max-width:100%;height:8px;margin:6px auto 10px;border-radius:8px 8px 2px 2px;background:linear-gradient(180deg,var(--green),#10c6aa);box-shadow:0 8px 18px rgba(8,185,158,.2)}.records{margin-top:24px;padding:24px}.table-wrap{overflow:auto;border-top:1px solid var(--line);padding-top:18px}table{width:100%;border-collapse:collapse;min-width:900px;table-layout:fixed}th{background:#f6f8fb;color:#4c5566;text-align:left;font-size:15px;padding:14px 12px}th:nth-child(1){width:160px}th:nth-child(2){width:170px}th:nth-child(5){width:92px}td{padding:14px 12px;border-bottom:1px solid var(--line);font-size:14px;vertical-align:top}.badge{display:inline-flex;align-items:center;justify-content:center;min-width:64px;padding:6px 10px;border-radius:999px;font-size:12px;font-weight:900;color:#fff}.badge.info{background:var(--blue)}.badge.error{background:var(--red)}.badge.warn{background:#f0a81f}.badge.ok{background:var(--green)}.content-cell{line-height:1.55}.clip-cell{max-height:5.1em;overflow:auto;overflow-wrap:anywhere;word-break:break-word;padding-right:4px}.clip-cell::-webkit-scrollbar{width:6px}.clip-cell::-webkit-scrollbar-thumb{background:#d5dce8;border-radius:999px}.log-panel{overflow:hidden}.log-head{display:flex;justify-content:space-between;align-items:center;gap:16px;padding:22px 24px;border-bottom:1px solid var(--line)}.terminal{height:min(56vh,590px);overflow:auto;background:#101724;color:#d9e7ff;padding:20px 24px;border-radius:0 0 22px 22px}pre{margin:0;white-space:pre-wrap;word-break:break-word;font:13px/1.55 ui-monospace,SFMono-Regular,Menlo,Consolas,"Liberation Mono",monospace}.empty{color:var(--muted);display:grid;place-items:center;text-align:center;min-height:230px;background:#fff}.settings-hero{display:grid;grid-template-columns:120px 1fr;gap:22px;align-items:center;padding:26px;border-radius:24px;background:linear-gradient(135deg,#fff7fb,#eef6ff);border:1px solid #fff;box-shadow:var(--soft)}.settings-hero h2{margin:0 0 8px;font-size:28px}.settings-hero p{margin:0;color:var(--muted);line-height:1.65}.settings-grid{display:grid;grid-template-columns:repeat(2,1fr);gap:16px;margin-top:18px}.setting{position:relative;overflow:hidden;padding:20px;border:1px solid var(--line);border-radius:20px;background:#fbfcfe}.setting:before{content:"";position:absolute;inset:0 0 auto;height:4px;background:linear-gradient(90deg,var(--blue),#ff91b8)}.setting span{display:block;color:var(--muted);font-size:13px;margin-bottom:9px}.setting strong{display:block;font-size:17px;line-height:1.45;word-break:break-all}.setting small{display:block;margin-top:8px;color:var(--muted);line-height:1.5}.setting-wide{grid-column:1/-1}.setting-actions{display:flex;gap:10px;flex-wrap:wrap;margin-top:18px}.token-summary{display:grid;grid-template-columns:repeat(2,1fr);gap:12px}.token-summary div{padding:18px;border:1px solid var(--line);border-radius:18px;background:#fbfcfe}.token-summary span{display:block;color:var(--muted);font-size:13px;margin-bottom:8px}.token-summary strong{font-size:32px}.mobile-tabs{display:none}
    @media(max-width:1180px){.cards{grid-template-columns:repeat(2,1fr)}.grid-2{grid-template-columns:1fr}.layout{grid-template-columns:1fr}.side{display:none}.navlinks{display:none}.mobile-tabs{display:block;margin-top:18px}.mobile-tabs select{background:#fff}.topnav{position:relative;top:0}.brand{min-width:0}.right-tools .tool-pill:first-child{display:none}}@media(max-width:700px){.shell{width:min(100vw - 24px,1420px);padding-top:12px}.topnav{border-radius:20px}.cards{grid-template-columns:1fr}.hero-head,.panel-head,.log-head{align-items:stretch;flex-direction:column}.control-grid,.settings-hero,.settings-grid{grid-template-columns:1fr}.content-cell{white-space:normal}.chart{gap:10px;padding-inline:4px}.bar{width:34px}.stat strong{font-size:36px}}
  </style>
</head>
<body data-authed="{{.Authed}}">
  <main class="shell">
    <header class="topnav">
      <div class="brand"><div class="logo">猫</div><div><strong>小黑盒猫娘</strong><br><small>VPS 控制台</small></div></div>
      <nav class="navlinks"><button class="nav active" data-view="home">主控台</button><button class="nav" data-view="logs">日志管理</button><button class="nav" data-view="service">服务控制</button><button class="nav" data-view="status">系统状态</button></nav>
      <div class="right-tools"><div id="topStatus" class="tool-pill"><span class="dot"></span><span>未连接</span></div><button id="adminMenuBtn" class="avatar-button" type="button" aria-label="打开管理员设置"><img src="/assets/admin-avatar.png" alt="管理员"></button></div>
    </header>

    <section id="loginView" class="login hidden">
      <form id="loginForm" class="login-card">
        <div class="catgirl"><b>猫娘</b></div>
        <h1>进入猫娘控制台</h1>
        <p>使用首次启动时打印的随机强密码登录。公网访问建议在云安全组只放行可信 IP。</p>
        <input id="password" class="input" type="password" placeholder="随机强密码" autocomplete="current-password" autofocus>
        <div style="margin-top:14px"><button class="primary" type="submit">登录</button></div>
        <div id="loginToast" class="toast"></div>
      </form>
    </section>

    <section id="appView" class="hidden">
      <div class="mobile-tabs"><select id="mobileNav"><option value="home">主控台</option><option value="logs">日志管理</option><option value="service">服务控制</option><option value="status">系统状态</option></select></div>
      <div class="layout">
        <aside class="side">
          <button class="new-chat" data-view-button="home">主控首页</button>
          <div class="side-card"><strong>猫娘面板</strong><p>主控台放首页，其他功能通过顶部导航跳转。</p></div>
          <div class="side-card"><strong>当前服务</strong><p>{{.Service}}</p></div>
        </aside>
        <div class="content">
          <section class="view active" id="view-home">
            <div class="hero-card"><div class="hero-head"><div class="hero-title"><h1>主控台</h1><p>只展示小黑盒用户对机器人说的话、机器人回复和失败统计。</p></div><div class="panel-actions"><button id="homeRefreshBtn" class="secondary" type="button">刷新</button><button id="homeRestartBtn" class="warn" type="button">重启服务</button></div></div></div>
            <section class="cards"><div class="card stat"><span>提问次数</span><strong id="metricStatus" class="violet">0</strong></div><div class="card stat"><span>回复成功</span><strong id="metricLines" class="green">0</strong></div><div class="card stat"><span>失败次数</span><strong id="metricErrors" class="red">0</strong></div><div class="card stat"><span>待处理</span><strong id="metricFiles" class="blue">0</strong></div><div class="card stat"><span>Web 端口</span><strong id="metricPort" class="amber">29173</strong></div></section>
            <section class="grid-2"><div class="card panel"><div class="panel-head"><div><h2>Token 记录</h2><p>按最近 1 小时和最近 24 小时汇总 AI token 消耗。</p></div></div><div class="token-summary"><div><span>最近 1 小时</span><strong id="tokenHour" class="blue">0</strong></div><div><span>最近 24 小时</span><strong id="tokenDay" class="violet">0</strong></div></div><div id="appToast" class="toast"></div></div><div class="card panel"><div class="panel-head"><div><h2>最近 7 次日志趋势</h2><p>按日志日期聚合；无日期时归入今日。</p></div></div><div id="chart" class="chart"></div></div></section>
            <section class="card records"><div class="panel-head"><div><h2>最近 20 次对话</h2><p>按顺序配对“小黑盒用户提问”和“机器人回复”，失败会单独标记。</p></div></div><div class="table-wrap"><table><thead><tr><th>时间</th><th>用户</th><th>用户说</th><th>机器人回复</th><th>状态</th></tr></thead><tbody id="recordsBody"><tr><td colspan="5">等待日志...</td></tr></tbody></table></div></section>
          </section>

          <section class="view" id="view-logs"><div class="card log-panel"><div class="log-head"><div><h2>日志管理</h2><p id="currentSource">等待日志源...</p></div><select id="logSelect"></select></div><div class="terminal"><pre id="logOutput" class="empty">等待日志...</pre></div></div></section>

          <section class="view" id="view-service"><div class="card panel"><div class="panel-head"><div><h2>服务控制</h2><p>启动、停止或重启 Openxhh systemd 服务。</p></div></div><div class="panel-actions"><button id="serviceStartBtn" class="primary">启动服务</button><button id="serviceRestartBtn" class="warn">重启服务</button><button id="serviceStopBtn" class="danger">停止服务</button><button id="serviceRefreshBtn" class="secondary">刷新状态</button></div><div class="warnbox">如果按钮报错，请确认 Web UI 运行用户有权限执行 systemctl。</div></div></section>

          <section class="view" id="view-status"><div class="card panel"><div class="panel-head"><div><h2>系统状态</h2><p>当前 Web UI 与 Openxhh 服务信息。</p></div></div><div class="meta"><div><span>监听地址</span><strong id="listenAddr">—</strong></div><div><span>工作目录</span><strong id="rootDir">—</strong></div><div><span>systemctl status</span><strong id="statusText" class="status-text">—</strong></div></div></div></section>

          <section class="view" id="view-settings"><div class="card panel"><div class="settings-hero"><button class="avatar-button" type="button" aria-label="管理员头像"><img src="/assets/admin-avatar.png" alt="管理员"></button><div><h2>管理员设置</h2><p>这里集中展示 Web UI 的公开访问、认证和运行配置。敏感文件仍保持只读，不在公网面板直接编辑。</p></div></div><div class="settings-grid"><div class="setting"><span>systemd 服务</span><strong>{{.Service}}</strong><small>主控台按钮会对这个服务执行 start / stop / restart。</small></div><div class="setting"><span>Web UI 端口</span><strong>29173</strong><small>默认公网监听；建议只在云安全组放行你的固定 IP。</small></div><div class="setting"><span>认证方式</span><strong>随机强密码</strong><small>首次启动打印密码，本地仅保存 salted hash。</small></div><div class="setting"><span>失败限速</span><strong>5 次失败锁定 5 分钟</strong><small>降低公网暴力尝试风险。</small></div><div class="setting setting-wide"><span>安全建议</span><strong>公网访问建议配合 HTTPS 反代或安全组白名单</strong><small>如果只是自己使用，优先只开放可信来源 IP；不要把 webui_auth.json、config.json、cookie.json 上传到公开仓库。</small></div></div><div class="setting-actions"><button id="settingsHomeBtn" class="secondary" type="button">返回主控台</button><button id="logoutBtn" class="danger" type="button">退出登录</button></div></div></section>
        </div>
      </div>
    </section>
  </main>
<script>
const authed=document.body.dataset.authed==='true';
const loginView=document.querySelector('#loginView');
const appView=document.querySelector('#appView');
const topStatus=document.querySelector('#topStatus');
const loginToast=document.querySelector('#loginToast');
const appToast=document.querySelector('#appToast');
const logSelect=document.querySelector('#logSelect');
const logOutput=document.querySelector('#logOutput');
const recordsBody=document.querySelector('#recordsBody');
const chart=document.querySelector('#chart');
let currentLog='';
let currentLogLabel='';
let logTimer=null;
let statusTimer=null;

function showApp(ok){loginView.classList.toggle('hidden',ok);appView.classList.toggle('hidden',!ok);if(ok){switchView('home');bootstrap()}}
async function api(path,options={}){const res=await fetch(path,{headers:{'Content-Type':'application/json'},credentials:'same-origin',...options});const data=await res.json().catch(()=>({}));if(!res.ok)throw new Error(data.error||'请求失败');return data}
function switchView(name){document.querySelectorAll('.view').forEach(view=>view.classList.toggle('active',view.id==='view-'+name));document.querySelectorAll('.nav').forEach(btn=>btn.classList.toggle('active',btn.dataset.view===name));document.querySelector('#adminMenuBtn')?.classList.toggle('active',name==='settings');const mobile=document.querySelector('#mobileNav');if(mobile&&name!=='settings')mobile.value=name}
document.querySelectorAll('[data-view], [data-view-button]').forEach(el=>el.addEventListener('click',()=>switchView(el.dataset.view||el.dataset.viewButton)));
document.querySelector('#adminMenuBtn')?.addEventListener('click',()=>switchView('settings'));
document.querySelector('#settingsHomeBtn')?.addEventListener('click',()=>switchView('home'));
document.querySelector('#mobileNav')?.addEventListener('change',event=>switchView(event.target.value));

document.querySelector('#loginForm').addEventListener('submit',async event=>{event.preventDefault();loginToast.textContent='';try{await api('/login',{method:'POST',body:JSON.stringify({password:document.querySelector('#password').value})});showApp(true)}catch(err){loginToast.textContent=err.message}});
function bindAction(ids,path,text){for(const id of ids){const el=document.querySelector('#'+id);if(el)el.addEventListener('click',()=>action(path,text))}}
bindAction(['serviceStartBtn'],'/api/start','启动命令已发送');
bindAction(['serviceStopBtn'],'/api/stop','停止命令已发送');
bindAction(['serviceRestartBtn','homeRestartBtn'],'/api/restart','重启命令已发送');
for(const id of ['homeRefreshBtn','serviceRefreshBtn']){const el=document.querySelector('#'+id);if(el)el.addEventListener('click',()=>{refreshStatus();loadLogs()})}
document.querySelector('#logoutBtn').addEventListener('click',async()=>{await api('/logout',{method:'POST'});location.reload()});
logSelect.addEventListener('change',()=>{currentLog=logSelect.value;currentLogLabel=logSelect.selectedOptions[0]?.textContent||currentLog;loadCurrentLog()});

async function action(path,text){if(appToast)appToast.textContent='';try{await api(path,{method:'POST'});if(appToast)appToast.textContent=text;setTimeout(refreshStatus,900);setTimeout(loadCurrentLog,1200)}catch(err){if(appToast)appToast.textContent=err.message}}
async function bootstrap(){await refreshStatus();await loadLogs();clearInterval(logTimer);clearInterval(statusTimer);statusTimer=setInterval(refreshStatus,4000);logTimer=setInterval(loadCurrentLog,1800)}

async function refreshStatus(){try{const data=await api('/api/status');const running=data.running;const serviceState=document.querySelector('#serviceState');if(serviceState)serviceState.textContent=(data.active||'unknown')+(data.detail?' · '+data.detail:'');document.querySelector('#listenAddr').textContent=data.listenAddr||'—';document.querySelector('#rootDir').textContent=data.rootDir||'—';document.querySelector('#statusText').textContent=data.statusText||'—';document.querySelector('#metricPort').textContent=extractPort(data.listenAddr)||'29173';for(const id of ['serviceStartBtn'])document.querySelector('#'+id).disabled=running;for(const id of ['serviceStopBtn'])document.querySelector('#'+id).disabled=!running;topStatus.innerHTML='<span class="dot '+(running?'on':'')+'"></span><span>'+(running?'运行中':'待机')+'</span>'}catch(err){topStatus.innerHTML='<span class="dot"></span><span>认证失效</span>'}}

async function loadLogs(){const data=await api('/api/logs');const files=data.files||[];const previous=currentLog;logSelect.innerHTML='';for(const file of files){const option=document.createElement('option');option.value=file.name;option.textContent=(file.label||file.name)+(file.size?' · '+formatBytes(file.size):'')+(file.modTime?' · '+file.modTime:'');logSelect.appendChild(option)}currentLog=files.some(file=>file.name===previous)?previous:(files[0]?.name||'');logSelect.value=currentLog;currentLogLabel=logSelect.selectedOptions[0]?.textContent||currentLog;await loadCurrentLog()}
async function loadCurrentLog(){if(!currentLog){renderLog('');return}try{const data=await api('/api/logs/read?file='+encodeURIComponent(currentLog));renderLog(data.content||'')}catch(err){renderLog('日志读取失败: '+err.message)}}

function renderLog(content){const box=logOutput.parentElement;const scrollTop=box.scrollTop;document.querySelector('#currentSource').textContent=currentLogLabel||'暂无日志源';logOutput.textContent=content||'暂无日志。';logOutput.classList.toggle('empty',!content);box.scrollTop=Math.min(scrollTop,box.scrollHeight);const lines=content?content.split('\n').filter(Boolean):[];const interactions=parseInteractions(lines);const completed=interactions.filter(item=>item.status==='已回复'&&item.question&&item.reply);const failed=interactions.filter(item=>item.status==='失败'&&item.question&&item.reply).length;const pending=interactions.filter(item=>item.status==='待回复'||item.status==='待重试').length;const records=interactions.filter(item=>(item.status==='已回复'||item.status==='失败')&&item.question&&item.reply);document.querySelector('#metricStatus').textContent=interactions.length;document.querySelector('#metricLines').textContent=completed.length;document.querySelector('#metricErrors').textContent=failed;document.querySelector('#metricFiles').textContent=pending;renderRecords(records.slice(-20).reverse());renderTrend(completed);renderTokenRecords(completed)}
function renderRecords(items){recordsBody.innerHTML='';if(!items.length){const row=document.createElement('tr');const cell=document.createElement('td');cell.colSpan=5;cell.textContent='暂无可识别的用户提问/机器人回复记录';row.appendChild(cell);recordsBody.appendChild(row);return}for(const item of items){const row=document.createElement('tr');appendCell(row,item.time);appendCell(row,item.user||'未知用户');appendCell(row,item.question,'content-cell');appendCell(row,item.reply||'—','content-cell');const statusCell=document.createElement('td');const badge=document.createElement('span');badge.className='badge '+(item.status==='已回复'?'ok':item.status==='失败'?'error':'warn');badge.textContent=item.status;statusCell.appendChild(badge);row.appendChild(statusCell);recordsBody.appendChild(row)}}
function appendCell(row,text,className){const cell=document.createElement('td');if(className){cell.className=className;const inner=document.createElement('div');inner.className='clip-cell';inner.textContent=text||'—';cell.appendChild(inner)}else{cell.textContent=text||'—'}row.appendChild(cell)}
function renderTokenRecords(items){const hourEl=document.querySelector('#tokenHour');const dayEl=document.querySelector('#tokenDay');const now=Date.now();let hour=0;let day=0;for(const item of items){if(!item.tokens)continue;const time=parseItemTime(item.time);if(!time)continue;const age=now-time.getTime();if(age>=0&&age<=3600000)hour+=item.tokens;if(age>=0&&age<=86400000)day+=item.tokens}if(hourEl)hourEl.textContent=formatCount(hour);if(dayEl)dayEl.textContent=formatCount(day)}
function parseInteractions(lines){const items=[];let pending=null;for(const line of lines){if(line.includes('[Ai]正在询问Ai')){if(pending&&pending.question)items.push(finalizePending(pending));pending=parseQuestionLine(line);continue}if(line.includes('[Ai]Ai说：')){if(!pending||!pending.question){pending=null;continue}pending.reply=extractJsonField(line,'text')||stripLogPrefix(line);pending.tokens=extractToken(line);pending.status='已回复';items.push(pending);pending=null;continue}if(isFailureLine(line)&&pending&&pending.question&&!pending.lastError){pending.lastError=stripLogPrefix(line);pending.status='待重试'}}if(pending&&pending.question)items.push(finalizePending(pending));return items}
function finalizePending(item){if(item.status==='待重试'||item.lastError){item.reply=item.lastError||item.reply||'AI 回复失败';item.status='失败'}return item}
function parseQuestionLine(line){const content=extractContentArray(line);const userQuestion=extractUserQuestion(content);return{time:extractTime(line),user:userQuestion.user,question:userQuestion.question,reply:'',status:'待回复'}}
function extractUserQuestion(content){const candidates=[];for(const item of content){const text=item&&item.text;if(!text||!/评论.*上下文/.test(text))continue;for(const line of text.split('\n')){const parsed=parseContextLine(line);if(parsed)candidates.push(parsed)}}if(!candidates.length)return{user:'未知用户',question:''};const mentioned=[...candidates].reverse().find(item=>item.text.includes('@'));const picked=mentioned||candidates[candidates.length-1];return{user:picked.user||'未知用户',question:picked.text||''}}
function extractContentArray(line){const obj=parseZapJSON(line);if(obj&&Array.isArray(obj.Content))return obj.Content;return[]}
function parseZapJSON(line){const start=line.indexOf('{');if(start<0)return null;try{return JSON.parse(line.slice(start))}catch(err){return null}}
function extractQuestion(content){for(let i=content.length-1;i>=0;i--){const text=content[i]&&content[i].text;if(!text)continue;const index=text.lastIndexOf('以上是帖子内容。');if(index>=0)return cleanText(text.slice(index+'以上是帖子内容。'.length));}return''}
function extractUserFromContent(content,question){let fallback='未知用户';const plainQuestion=normalizeText(question).replace(/^@[^\s]+/,'');for(const item of content){const text=item&&item.text;if(!text)continue;const body=text.split('\n');for(const line of body){const parsed=parseContextLine(line);if(!parsed)continue;fallback=parsed.user;if(plainQuestion&&normalizeText(parsed.text).includes(plainQuestion))return parsed.user}}return fallback}
function parseContextLine(line){const trimmed=cleanText(line);let match=trimmed.match(/^(.+?) 回复 .+?：(.+)$/);if(match)return{user:match[1],text:match[2]};match=trimmed.match(/^(.+?)：(.+)$/);if(match)return{user:match[1],text:match[2]};return null}
function extractJsonField(line,field){const obj=parseZapJSON(line);if(!obj)return'';return cleanText(obj[field]||'')}
function extractToken(line){const obj=parseZapJSON(line);if(!obj)return 0;const value=obj['本次消耗token']??obj.total_tokens??obj.totalToken??obj.tokens;const token=Number(value);return Number.isFinite(token)&&token>0?token:0}
function isFailureLine(line){return /Ai返回错误|无法回复评论|评论发送失败|图片评论处理失败|无法整理@消息|comment\/create image reply failed|error|failed|panic|fatal|错误|失败|异常/i.test(line)&&!line.includes('[Ai]正在询问Ai')}
function extractTime(line){const match=line.match(/(20\d{2}-\d{2}-\d{2})(?:[ T](\d{2}:\d{2}:\d{2}))?/);return match?match[1]+(match[2]?' '+match[2]:''):'—'}
function stripLogPrefix(line){return cleanText(line.replace(/^.*?\]\s*/,''))}
function cleanText(text){return String(text||'').replace(/<[^>]+>/g,'').replace(/&nbsp;/g,' ').trim()}
function normalizeText(text){return cleanText(text).replace(/\s+/g,'')}
function renderTrend(items){const buckets=[];const now=new Date();for(let i=6;i>=0;i--){const date=new Date(now);date.setDate(now.getDate()-i);const key=date.toISOString().slice(0,10);buckets.push({key,label:(date.getMonth()+1).toString().padStart(2,'0')+'-'+date.getDate().toString().padStart(2,'0'),count:0})}for(const item of items){const match=(item.time||'').match(/20\d{2}-\d{2}-\d{2}/);const key=match?match[0]:buckets[buckets.length-1].key;const bucket=buckets.find(value=>value.key===key);if(bucket)bucket.count++}const max=Math.max(1,...buckets.map(item=>item.count));chart.innerHTML='';for(const item of buckets){const wrap=document.createElement('div');wrap.className='bar-wrap';const num=document.createElement('div');num.className='bar-num';num.textContent=item.count||'';const bar=document.createElement('div');bar.className='bar';bar.style.height=Math.max(8,Math.round(item.count/max*140))+'px';const label=document.createElement('div');label.textContent=item.label;wrap.appendChild(num);wrap.appendChild(bar);wrap.appendChild(label);chart.appendChild(wrap)}}
function extractPort(addr){if(!addr)return'';const parts=addr.split(':');return parts[parts.length-1]||''}
function formatBytes(size){if(size<1024)return size+' B';if(size<1024*1024)return(size/1024).toFixed(1)+' KB';return(size/1024/1024).toFixed(1)+' MB'}
function parseItemTime(value){if(!value||value==='—')return null;const date=new Date(String(value).replace(' ','T'));return Number.isNaN(date.getTime())?null:date}
function formatCount(value){return Number(value||0).toLocaleString('zh-CN')}
showApp(authed);
</script>
</body>
</html>`
