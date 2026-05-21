package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html"
	"html/template"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/mattn/go-sqlite3"
)

const defaultAddr = ":29173"
const journalName = "__journal__"
const tokenRecordFileName = "token_records.jsonl"
const maxConfigBodySize = 1 << 20
const defaultFeedReplyPrompt = "你正在作为小黑盒用户回复帖子。请结合帖子内容写一句自然、有信息量、不像机器人的短评论；如果帖子不适合回复，或容易引战、广告、抽奖、敏感内容，请只输出 SKIP。"
const maxRecordLinkLookupIDs = 300
const webuiSessionCookieName = "xhh_vps_webui_session"
const webuiSessionDuration = 7 * 24 * time.Hour

var indexTemplate = template.Must(template.New("index").Parse(indexHTML))
var serviceNamePattern = regexp.MustCompile(`^[A-Za-z0-9_.@-]+$`)
var logTimePattern = regexp.MustCompile(`(20\d{2}-\d{2}-\d{2})(?:[ T](\d{2}:\d{2}:\d{2}))?`)
var htmlTagPattern = regexp.MustCompile(`<[^>]+>`)

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

type tokenRecord struct {
	Time   string `json:"time"`
	Model  string `json:"model,omitempty"`
	Tokens int64  `json:"tokens"`
}

type recordLinkLookup struct {
	ByMsg             map[int64]int64  `json:"byMsg"`
	ByComment         map[int64]int64  `json:"byComment"`
	QuestionByMsg     map[int64]string `json:"questionByMsg"`
	QuestionByComment map[int64]string `json:"questionByComment"`
}

type regenerateCandidate struct {
	MsgID     int64
	CommentID int64
	UserID    int64
	UserName  string
	Question  string
}

type regenerateMessageRequest struct {
	MsgID     int64  `json:"msgId"`
	CommentID int64  `json:"commentId"`
	LinkID    int64  `json:"linkId"`
	UserID    int64  `json:"userId"`
	UserName  string `json:"userName"`
	Question  string `json:"question"`
}

type feedReplyRecord struct {
	LinkID    int64  `json:"linkId"`
	Title     string `json:"title"`
	AuthorID  int64  `json:"authorId"`
	Author    string `json:"author"`
	PostText  string `json:"postText"`
	ReplyText string `json:"replyText"`
	Status    string `json:"status"`
	Reason    string `json:"reason"`
	CreatedAt int64  `json:"createdAt"`
	RepliedAt int64  `json:"repliedAt"`
}

type appConfig struct {
	Xhh struct {
		CheckTime                int    `json:"checkTime"`
		ReplyTime                int    `json:"replyTime"`
		MaxReplyThreads          int    `json:"maxReplyThreads"`
		MaxPendingReplies        int    `json:"maxPendingReplies"`
		MaxPendingRepliesPerUser int    `json:"maxPendingRepliesPerUser"`
		EnableWhitelist          bool   `json:"enableWhitelist"`
		Owner                    string `json:"owner"`
		DeviceID                 string `json:"deviceID"`
		BaseURL                  string `json:"baseUrl"`
		WebVer                   string `json:"webver"`
		Ver                      string `json:"version"`
	} `json:"xhh"`
	DataBase struct {
		Type   string `json:"type"`
		DB     string `json:"db"`
		Host   string `json:"host"`
		Port   string `json:"port"`
		User   string `json:"user"`
		Passwd string `json:"passwd"`
	} `json:"database"`
	AI struct {
		Model             string `json:"model"`
		Prompt            string `json:"prompt"`
		BaseURL           string `json:"baseUrl"`
		Token             string `json:"token"`
		WebSearch         *bool  `json:"webSearch,omitempty"`
		ForceWebSearch    *bool  `json:"forceWebSearch,omitempty"`
		SearchContextSize string `json:"searchContextSize"`
	} `json:"ai"`
	FeedReply struct {
		Enabled   bool   `json:"enabled"`
		Interval  int    `json:"interval"`
		MaxPerRun int    `json:"maxPerRun"`
		MaxPerDay int    `json:"maxPerDay"`
		DryRun    *bool  `json:"dryRun,omitempty"`
		Prompt    string `json:"prompt"`
	} `json:"feedReply"`
	Image struct {
		Model           string `json:"model"`
		BaseURL         string `json:"baseUrl"`
		Token           string `json:"token"`
		Size            string `json:"size"`
		ResponseFormat  string `json:"responseFormat"`
		OutputDir       string `json:"outputDir"`
		UploadMode      string `json:"uploadMode"`
		ExternalDir     string `json:"externalDir"`
		ExternalBaseURL string `json:"externalBaseUrl"`
		PromptRefine    bool   `json:"promptRefine"`
		PromptModel     string `json:"promptModel"`
		PromptBaseURL   string `json:"promptBaseUrl"`
		PromptToken     string `json:"promptToken"`
		PromptMaxChars  int    `json:"promptMaxChars"`
	} `json:"image"`
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
	mux.HandleFunc("/api/config", state.requireAuth(state.handleConfig))
	mux.HandleFunc("/api/start", state.requireAuth(state.handleStart))
	mux.HandleFunc("/api/stop", state.requireAuth(state.handleStop))
	mux.HandleFunc("/api/restart", state.requireAuth(state.handleRestart))
	mux.HandleFunc("/api/messages/regenerate", state.requireAuth(state.handleRegenerateMessage))
	mux.HandleFunc("/api/logs", state.requireAuth(state.handleLogs))
	mux.HandleFunc("/api/logs/read", state.requireAuth(state.handleReadLog))
	mux.HandleFunc("/api/records", state.requireAuth(state.handleRecords))
	mux.HandleFunc("/api/feed-records", state.requireAuth(state.handleFeedRecords))

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

func (s *serverState) readAuthStore() (authStore, error) {
	data, err := os.ReadFile(s.authPath)
	if err != nil {
		return authStore{}, err
	}
	var store authStore
	if err := json.Unmarshal(data, &store); err != nil {
		return authStore{}, err
	}
	return store, nil
}

func (s *serverState) validPassword(password string) bool {
	store, err := s.readAuthStore()
	if err != nil {
		return false
	}
	actual := hashPassword(password, store.Salt)
	return subtle.ConstantTimeCompare([]byte(actual), []byte(store.Hash)) == 1
}

func (s *serverState) createSessionToken() (string, time.Time, error) {
	store, err := s.readAuthStore()
	if err != nil {
		return "", time.Time{}, err
	}
	nonce, err := randomPassword(32)
	if err != nil {
		return "", time.Time{}, err
	}
	expiresAt := time.Now().Add(webuiSessionDuration)
	body := fmt.Sprintf("v1.%d.%s", expiresAt.Unix(), nonce)
	return body + "." + sessionSignature(store, body), expiresAt, nil
}

func (s *serverState) validSessionToken(token string) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 4 || parts[0] != "v1" {
		return false
	}
	var expiresUnix int64
	if _, err := fmt.Sscan(parts[1], &expiresUnix); err != nil || expiresUnix <= 0 {
		return false
	}
	if time.Now().After(time.Unix(expiresUnix, 0)) {
		return false
	}
	store, err := s.readAuthStore()
	if err != nil {
		return false
	}
	expected := sessionSignature(store, strings.Join(parts[:3], "."))
	return hmac.Equal([]byte(parts[3]), []byte(expected))
}

func sessionSignature(store authStore, body string) string {
	mac := hmac.New(sha256.New, []byte(store.Salt+":"+store.Hash))
	_, _ = mac.Write([]byte(body))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
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
	token, expiresAt, err := s.createSessionToken()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "无法创建会话"})
		return
	}
	s.mu.Lock()
	s.sessions[token] = expiresAt
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{
		Name:     webuiSessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  expiresAt,
		MaxAge:   int(time.Until(expiresAt).Seconds()),
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
	cookie, err := r.Cookie(webuiSessionCookieName)
	if err != nil || cookie.Value == "" {
		return false
	}
	if s.validSessionToken(cookie.Value) {
		return true
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
	if cookie, err := r.Cookie(webuiSessionCookieName); err == nil {
		s.mu.Lock()
		delete(s.sessions, cookie.Value)
		s.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: webuiSessionCookieName, Path: "/", MaxAge: -1, Expires: time.Unix(0, 0), SameSite: http.SameSiteLaxMode})
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

func (s *serverState) handleConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		cfg, exists, err := s.loadConfig()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "exists": exists, "path": s.configPath(), "config": cfg})
	case http.MethodPost:
		var cfg appConfig
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxConfigBodySize))
		if err := decoder.Decode(&cfg); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "配置格式错误: " + err.Error()})
			return
		}
		applyConfigDefaults(&cfg)
		data, err := json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		if err := os.MkdirAll(s.rootDir, 0755); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		if err := os.WriteFile(s.configPath(), append(data, '\n'), 0600); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "path": s.configPath()})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *serverState) loadConfig() (appConfig, bool, error) {
	cfg := defaultConfig()
	data, err := os.ReadFile(s.configPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, false, nil
		}
		return cfg, false, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, true, err
	}
	if applyConfigDefaults(&cfg) {
		if data, err := json.MarshalIndent(cfg, "", "  "); err == nil {
			_ = os.WriteFile(s.configPath(), append(data, '\n'), 0600)
		}
	}
	return cfg, true, nil
}

func (s *serverState) configPath() string {
	return filepath.Join(s.rootDir, "config.json")
}

func defaultConfig() appConfig {
	var cfg appConfig
	applyConfigDefaults(&cfg)
	return cfg
}

func applyConfigDefaults(cfg *appConfig) bool {
	changed := false
	if cfg.Xhh.CheckTime == 0 {
		cfg.Xhh.CheckTime = 60
		changed = true
	}
	if cfg.Xhh.ReplyTime == 0 {
		cfg.Xhh.ReplyTime = 30
		changed = true
	}
	if cfg.Xhh.MaxReplyThreads <= 0 {
		cfg.Xhh.MaxReplyThreads = 3
		changed = true
	}
	if cfg.Xhh.MaxPendingReplies <= 0 {
		cfg.Xhh.MaxPendingReplies = 50
		changed = true
	}
	if cfg.Xhh.MaxPendingRepliesPerUser <= 0 {
		cfg.Xhh.MaxPendingRepliesPerUser = 5
		changed = true
	}
	if cfg.Xhh.BaseURL == "" {
		cfg.Xhh.BaseURL = "https://api.xiaoheihe.cn"
		changed = true
	}
	if cfg.Xhh.WebVer == "" {
		cfg.Xhh.WebVer = "2.5"
		changed = true
	}
	if cfg.Xhh.Ver == "" {
		cfg.Xhh.Ver = "999.0.4"
		changed = true
	}
	if cfg.DataBase.Type == "" {
		cfg.DataBase.Type = "sqlite"
		changed = true
	}
	if cfg.AI.Prompt == "" {
		cfg.AI.Prompt = "请根据评论内容自然回复。"
		changed = true
	}
	if cfg.AI.WebSearch == nil {
		cfg.AI.WebSearch = boolPtr(true)
		changed = true
	}
	if cfg.AI.SearchContextSize == "" {
		cfg.AI.SearchContextSize = "medium"
		changed = true
	}
	if cfg.FeedReply.Interval <= 0 {
		cfg.FeedReply.Interval = 900
		changed = true
	}
	if cfg.FeedReply.MaxPerRun <= 0 {
		cfg.FeedReply.MaxPerRun = 1
		changed = true
	}
	if cfg.FeedReply.MaxPerDay <= 0 {
		cfg.FeedReply.MaxPerDay = 10
		changed = true
	}
	if cfg.FeedReply.DryRun == nil {
		cfg.FeedReply.DryRun = boolPtr(true)
		changed = true
	}
	if cfg.FeedReply.Prompt == "" {
		cfg.FeedReply.Prompt = defaultFeedReplyPrompt
		changed = true
	}
	if cfg.Image.Model == "" {
		cfg.Image.Model = "gpt-image-2"
		changed = true
	}
	if cfg.Image.Size == "" {
		cfg.Image.Size = "1024x1024"
		changed = true
	}
	if cfg.Image.ResponseFormat == "" {
		cfg.Image.ResponseFormat = "b64_json"
		changed = true
	}
	if cfg.Image.OutputDir == "" {
		cfg.Image.OutputDir = "images"
		changed = true
	}
	if cfg.Image.UploadMode == "" {
		cfg.Image.UploadMode = "external"
		changed = true
	}
	if cfg.Image.PromptMaxChars == 0 {
		cfg.Image.PromptMaxChars = 1000
		changed = true
	}
	return changed
}

func boolPtr(v bool) *bool {
	return &v
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

func (s *serverState) handleRegenerateMessage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var payload regenerateMessageRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "请求格式错误"})
		return
	}
	payload.UserName = strings.TrimSpace(payload.UserName)
	payload.Question = strings.TrimSpace(payload.Question)
	if payload.MsgID <= 0 && payload.CommentID <= 0 && payload.Question == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "缺少可回查的消息信息"})
		return
	}
	cfg, _, err := s.loadConfig()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	affected, err := s.markMessageUnreplied(cfg, payload)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if affected == 0 {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "未找到对应消息"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *serverState) markMessageUnreplied(cfg appConfig, req regenerateMessageRequest) (int64, error) {
	switch strings.ToLower(strings.TrimSpace(cfg.DataBase.Type)) {
	case "", "sqlite":
		return s.markSQLiteMessageUnreplied(req)
	case "pg", "postgres", "postgresql":
		return markPostgresMessageUnreplied(cfg, req)
	default:
		return 0, fmt.Errorf("不支持的数据库类型: %s", cfg.DataBase.Type)
	}
}

func (s *serverState) markSQLiteMessageUnreplied(req regenerateMessageRequest) (int64, error) {
	database, err := s.openSQLiteDatabase()
	if err != nil {
		return 0, err
	}
	defer database.Close()
	if req.MsgID > 0 {
		affected, err := execSQLiteRegenerate(database, "msg_id=?", req.MsgID)
		if err != nil || affected > 0 {
			return affected, err
		}
	}
	if req.CommentID > 0 {
		affected, err := execSQLiteRegenerate(database, "comment_a_id=?", req.CommentID)
		if err != nil || affected > 0 {
			return affected, err
		}
	}
	return markSQLiteMessageByText(database, req)
}

func (s *serverState) openSQLiteDatabase() (*sql.DB, error) {
	database, err := sql.Open("sqlite3", filepath.Join(s.rootDir, "sql.db"))
	if err != nil {
		return nil, err
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	if _, err := database.Exec("PRAGMA busy_timeout=8000"); err != nil {
		database.Close()
		return nil, err
	}
	return database, nil
}

func execSQLiteRegenerate(database *sql.DB, where string, args ...any) (int64, error) {
	query := "UPDATE at SET reply=? WHERE " + where
	result, err := database.Exec(query, append([]any{false}, args...)...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func markSQLiteMessageByText(database *sql.DB, req regenerateMessageRequest) (int64, error) {
	if req.Question == "" {
		return 0, nil
	}
	userName := validRegenerateUserName(req.UserName)
	attempts := []struct {
		where string
		args  []any
	}{
		{"comment_text=? AND user_a_id=?", []any{req.Question, req.UserID}},
		{"comment_text=? AND user_a_name=?", []any{req.Question, userName}},
		{"comment_text=?", []any{req.Question}},
	}
	for _, attempt := range attempts {
		if strings.Contains(attempt.where, "user_a_id") && req.UserID <= 0 {
			continue
		}
		if strings.Contains(attempt.where, "user_a_name") && userName == "" {
			continue
		}
		affected, err := execSQLiteRegenerate(database, "msg_id=(SELECT msg_id FROM at WHERE "+attempt.where+" ORDER BY msg_id DESC LIMIT 1)", attempt.args...)
		if err != nil || affected > 0 {
			return affected, err
		}
	}
	return markSQLiteMessageByFuzzyText(database, req)
}

func markSQLiteMessageByFuzzyText(database *sql.DB, req regenerateMessageRequest) (int64, error) {
	rows, err := database.Query("SELECT msg_id, comment_a_id, user_a_id, COALESCE(user_a_name, ''), COALESCE(comment_text, '') FROM at WHERE comment_text IS NOT NULL AND comment_text<>'' ORDER BY msg_id DESC")
	if err != nil {
		return 0, err
	}
	var matched regenerateCandidate
	for rows.Next() {
		var candidate regenerateCandidate
		if err := rows.Scan(&candidate.MsgID, &candidate.CommentID, &candidate.UserID, &candidate.UserName, &candidate.Question); err != nil {
			rows.Close()
			return 0, err
		}
		if regenerateCandidateMatches(req, candidate) {
			matched = candidate
			break
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	if matched.MsgID > 0 {
		return execSQLiteRegenerate(database, "msg_id=?", matched.MsgID)
	}
	if matched.CommentID > 0 {
		return execSQLiteRegenerate(database, "comment_a_id=?", matched.CommentID)
	}
	return 0, nil
}

func markPostgresMessageUnreplied(cfg appConfig, req regenerateMessageRequest) (int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, postgresDSN(cfg))
	if err != nil {
		return 0, err
	}
	defer pool.Close()
	if req.MsgID > 0 {
		affected, err := execPostgresRegenerate(ctx, pool, "msg_id=$1", req.MsgID)
		if err != nil || affected > 0 {
			return affected, err
		}
	}
	if req.CommentID > 0 {
		affected, err := execPostgresRegenerate(ctx, pool, "comment_a_id=$1", req.CommentID)
		if err != nil || affected > 0 {
			return affected, err
		}
	}
	return markPostgresMessageByText(ctx, pool, req)
}

func execPostgresRegenerate(ctx context.Context, pool *pgxpool.Pool, where string, args ...any) (int64, error) {
	result, err := pool.Exec(ctx, "UPDATE at SET reply=false WHERE "+where, args...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected(), nil
}

func markPostgresMessageByText(ctx context.Context, pool *pgxpool.Pool, req regenerateMessageRequest) (int64, error) {
	if req.Question == "" {
		return 0, nil
	}
	userName := validRegenerateUserName(req.UserName)
	attempts := []struct {
		where string
		args  []any
	}{
		{"comment_text=$1 AND user_a_id=$2", []any{req.Question, req.UserID}},
		{"comment_text=$1 AND user_a_name=$2", []any{req.Question, userName}},
		{"comment_text=$1", []any{req.Question}},
	}
	for _, attempt := range attempts {
		if strings.Contains(attempt.where, "user_a_id") && req.UserID <= 0 {
			continue
		}
		if strings.Contains(attempt.where, "user_a_name") && userName == "" {
			continue
		}
		affected, err := execPostgresRegenerate(ctx, pool, "msg_id=(SELECT msg_id FROM at WHERE "+attempt.where+" ORDER BY msg_id DESC LIMIT 1)", attempt.args...)
		if err != nil || affected > 0 {
			return affected, err
		}
	}
	return markPostgresMessageByFuzzyText(ctx, pool, req)
}

func markPostgresMessageByFuzzyText(ctx context.Context, pool *pgxpool.Pool, req regenerateMessageRequest) (int64, error) {
	rows, err := pool.Query(ctx, "SELECT msg_id, comment_a_id, user_a_id, COALESCE(user_a_name, ''), COALESCE(comment_text, '') FROM at WHERE comment_text IS NOT NULL AND comment_text<>'' ORDER BY msg_id DESC")
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var candidate regenerateCandidate
		if err := rows.Scan(&candidate.MsgID, &candidate.CommentID, &candidate.UserID, &candidate.UserName, &candidate.Question); err != nil {
			return 0, err
		}
		if !regenerateCandidateMatches(req, candidate) {
			continue
		}
		if candidate.MsgID > 0 {
			return execPostgresRegenerate(ctx, pool, "msg_id=$1", candidate.MsgID)
		}
		if candidate.CommentID > 0 {
			return execPostgresRegenerate(ctx, pool, "comment_a_id=$1", candidate.CommentID)
		}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	return 0, nil
}

func regenerateCandidateMatches(req regenerateMessageRequest, candidate regenerateCandidate) bool {
	if req.UserID > 0 && candidate.UserID > 0 && req.UserID != candidate.UserID {
		return false
	}
	reqUserName := validRegenerateUserName(req.UserName)
	candidateUserName := validRegenerateUserName(candidate.UserName)
	if reqUserName != "" && candidateUserName != "" && normalizeRegenerateText(reqUserName) != normalizeRegenerateText(candidateUserName) {
		return false
	}
	return regenerateTextMatches(req.Question, candidate.Question)
}

func regenerateTextMatches(left string, right string) bool {
	leftVariants := regenerateTextVariants(left)
	rightVariants := regenerateTextVariants(right)
	for _, leftValue := range leftVariants {
		for _, rightValue := range rightVariants {
			if leftValue == "" || rightValue == "" {
				continue
			}
			if leftValue == rightValue {
				return true
			}
			if len(leftValue) >= 8 && strings.Contains(rightValue, leftValue) {
				return true
			}
			if len(rightValue) >= 8 && strings.Contains(leftValue, rightValue) {
				return true
			}
		}
	}
	return false
}

func regenerateTextVariants(text string) []string {
	base := normalizeRegenerateText(text)
	withoutMention := normalizeRegenerateText(stripLeadingMentions(text))
	if withoutMention == base {
		return []string{base}
	}
	return []string{base, withoutMention}
}

func normalizeRegenerateText(text string) string {
	text = html.UnescapeString(text)
	text = htmlTagPattern.ReplaceAllString(text, "")
	text = strings.ReplaceAll(text, string(rune(0x00a0)), " ")
	return strings.ToLower(strings.Join(strings.Fields(text), ""))
}

func stripLeadingMentions(text string) string {
	fields := strings.Fields(text)
	for len(fields) > 0 && strings.HasPrefix(fields[0], "@") {
		fields = fields[1:]
	}
	return strings.Join(fields, " ")
}

func validRegenerateUserName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" || name == "未知用户" || name == "—" {
		return ""
	}
	return name
}

func postgresDSN(cfg appConfig) string {
	host := strings.TrimSpace(cfg.DataBase.Host)
	if port := strings.TrimSpace(cfg.DataBase.Port); port != "" {
		host = net.JoinHostPort(host, port)
	}
	dsn := url.URL{
		Scheme: "postgresql",
		User:   url.UserPassword(cfg.DataBase.User, cfg.DataBase.Passwd),
		Host:   host,
		Path:   cfg.DataBase.DB,
	}
	return dsn.String()
}

func (s *serverState) handleLogs(w http.ResponseWriter, r *http.Request) {
	files := []logFile{{Name: journalName, Label: "systemd journal · " + s.service}}
	logFiles, _ := listLogFiles(filepath.Join(s.rootDir, "log"))
	files = append(files, logFiles...)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "files": files})
}

func (s *serverState) handleRecords(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	window := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("window")))
	recentOnly := window == "24h" || window == "recent"
	content, sources, err := s.readRecordLogs(recentOnly)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": content})
		return
	}
	tokens := s.readTokenRecords(content)
	links := s.readRecordLinkLookup(content)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "content": content, "sources": sources, "tokens": tokens, "links": links})
}

func (s *serverState) handleFeedRecords(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	window := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("window")))
	recentOnly := window == "24h" || window == "recent"
	cfg, err := s.readConfigForRecordLookup()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	records, err := s.readFeedReplyRecords(cfg, recentOnly)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "records": records})
}

func (s *serverState) readRecordLogs(recentOnly bool) (string, int, error) {
	logFiles, _ := listLogFiles(filepath.Join(s.rootDir, "log"))
	if len(logFiles) > 0 {
		sort.Slice(logFiles, func(i, j int) bool {
			return logFiles[i].ModTime < logFiles[j].ModTime
		})
		parts := make([]string, 0, len(logFiles))
		cutoff := time.Now().Add(-24 * time.Hour)
		for _, file := range logFiles {
			if recentOnly {
				modTime, err := time.ParseInLocation("2006-01-02 15:04:05", file.ModTime, time.Local)
				if err == nil && modTime.Before(cutoff) {
					continue
				}
			}
			path := filepath.Join(s.rootDir, "log", file.Name)
			var content string
			var err error
			if recentOnly {
				content, err = readRecentLogFile(path, cutoff)
			} else {
				content, err = readWholeFile(path)
			}
			if err != nil || strings.TrimSpace(content) == "" {
				continue
			}
			parts = append(parts, content)
		}
		if len(parts) > 0 {
			return strings.Join(parts, "\n"), len(parts), nil
		}
	}
	if recentOnly {
		content, err := s.readJournalSince(time.Now().Add(-24 * time.Hour))
		if err != nil {
			return content, 0, err
		}
		return content, 1, nil
	}
	content, err := s.readJournalAll()
	if err != nil {
		return content, 0, err
	}
	return content, 1, nil
}

func (s *serverState) readRecordLinkLookup(logContent string) recordLinkLookup {
	lookup := recordLinkLookup{ByMsg: map[int64]int64{}, ByComment: map[int64]int64{}, QuestionByMsg: map[int64]string{}, QuestionByComment: map[int64]string{}}
	msgIDs, commentIDs := recordLinkLookupIDs(logContent)
	if len(msgIDs) == 0 && len(commentIDs) == 0 {
		return lookup
	}
	cfg, err := s.readConfigForRecordLookup()
	if err != nil {
		return lookup
	}
	switch strings.ToLower(strings.TrimSpace(cfg.DataBase.Type)) {
	case "", "sqlite":
		_ = s.fillSQLiteRecordLinkLookup(msgIDs, commentIDs, &lookup)
	case "pg", "postgres", "postgresql":
		_ = fillPostgresRecordLinkLookup(cfg, msgIDs, commentIDs, &lookup)
	}
	return lookup
}

func (s *serverState) readConfigForRecordLookup() (appConfig, error) {
	cfg := defaultConfig()
	data, err := os.ReadFile(s.configPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, nil
		}
		return cfg, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, err
	}
	applyConfigDefaults(&cfg)
	return cfg, nil
}

func (s *serverState) readFeedReplyRecords(cfg appConfig, recentOnly bool) ([]feedReplyRecord, error) {
	switch strings.ToLower(strings.TrimSpace(cfg.DataBase.Type)) {
	case "", "sqlite":
		return s.readSQLiteFeedReplyRecords(recentOnly)
	case "pg", "postgres", "postgresql":
		return readPostgresFeedReplyRecords(cfg, recentOnly)
	default:
		return nil, fmt.Errorf("不支持的数据库类型: %s", cfg.DataBase.Type)
	}
}

func (s *serverState) readSQLiteFeedReplyRecords(recentOnly bool) ([]feedReplyRecord, error) {
	if _, err := os.Stat(filepath.Join(s.rootDir, "sql.db")); err != nil {
		return []feedReplyRecord{}, nil
	}
	database, err := s.openSQLiteDatabase()
	if err != nil {
		return nil, err
	}
	defer database.Close()
	query := "SELECT link_id,title,author_id,author_name,post_text,reply_text,status,reason,created_at,replied_at FROM feed_reply_records"
	args := []any{}
	if recentOnly {
		query += " WHERE replied_at >= ?"
		args = append(args, time.Now().Add(-24*time.Hour).Unix())
	}
	query += " ORDER BY replied_at DESC LIMIT 300"
	rows, err := database.Query(query, args...)
	if err != nil {
		if isMissingFeedReplyTable(err) {
			return []feedReplyRecord{}, nil
		}
		return nil, err
	}
	defer rows.Close()
	return scanFeedReplyRows(rows)
}

func readPostgresFeedReplyRecords(cfg appConfig, recentOnly bool) ([]feedReplyRecord, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, postgresDSN(cfg))
	if err != nil {
		return nil, err
	}
	defer pool.Close()
	query := "SELECT link_id,title,author_id,author_name,post_text,reply_text,status,reason,created_at,replied_at FROM feed_reply_records"
	args := []any{}
	if recentOnly {
		query += " WHERE replied_at >= $1"
		args = append(args, time.Now().Add(-24*time.Hour).Unix())
	}
	query += " ORDER BY replied_at DESC LIMIT 300"
	rows, err := pool.Query(ctx, query, args...)
	if err != nil {
		if isMissingFeedReplyTable(err) {
			return []feedReplyRecord{}, nil
		}
		return nil, err
	}
	defer rows.Close()
	records := []feedReplyRecord{}
	for rows.Next() {
		var record feedReplyRecord
		if err := rows.Scan(&record.LinkID, &record.Title, &record.AuthorID, &record.Author, &record.PostText, &record.ReplyText, &record.Status, &record.Reason, &record.CreatedAt, &record.RepliedAt); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func scanFeedReplyRows(rows *sql.Rows) ([]feedReplyRecord, error) {
	records := []feedReplyRecord{}
	for rows.Next() {
		var record feedReplyRecord
		if err := rows.Scan(&record.LinkID, &record.Title, &record.AuthorID, &record.Author, &record.PostText, &record.ReplyText, &record.Status, &record.Reason, &record.CreatedAt, &record.RepliedAt); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func isMissingFeedReplyTable(err error) bool {
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "no such table") || strings.Contains(text, "does not exist")
}

func recordLinkLookupIDs(content string) ([]int64, []int64) {
	msgSet := map[int64]struct{}{}
	commentSet := map[int64]struct{}{}
	for _, line := range strings.Split(content, "\n") {
		payload := logJSONPayload(line)
		if payload == nil {
			continue
		}
		if msgID := logIntField(payload, "msg_id", "msgId", "message_id"); msgID > 0 && len(msgSet) < maxRecordLinkLookupIDs {
			msgSet[msgID] = struct{}{}
		}
		if commentID := logIntField(payload, "comment_id", "commentId", "reply_id"); commentID > 0 && len(commentSet) < maxRecordLinkLookupIDs {
			commentSet[commentID] = struct{}{}
		}
	}
	return int64SetValues(msgSet), int64SetValues(commentSet)
}

func logJSONPayload(line string) map[string]any {
	start := strings.Index(line, "{")
	if start < 0 {
		return nil
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(line[start:]), &payload); err != nil {
		return nil
	}
	return payload
}

func logIntField(payload map[string]any, fields ...string) int64 {
	for _, field := range fields {
		value, ok := payload[field]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case float64:
			if typed > 0 {
				return int64(typed)
			}
		case int64:
			if typed > 0 {
				return typed
			}
		case int:
			if typed > 0 {
				return int64(typed)
			}
		case json.Number:
			parsed, err := typed.Int64()
			if err == nil && parsed > 0 {
				return parsed
			}
		case string:
			var parsed int64
			if _, err := fmt.Sscan(typed, &parsed); err == nil && parsed > 0 {
				return parsed
			}
		}
	}
	return 0
}

func int64SetValues(set map[int64]struct{}) []int64 {
	values := make([]int64, 0, len(set))
	for value := range set {
		values = append(values, value)
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	return values
}

func (s *serverState) fillSQLiteRecordLinkLookup(msgIDs, commentIDs []int64, lookup *recordLinkLookup) error {
	if _, err := os.Stat(filepath.Join(s.rootDir, "sql.db")); err != nil {
		return nil
	}
	database, err := s.openSQLiteDatabase()
	if err != nil {
		return err
	}
	defer database.Close()
	if err := fillSQLiteRecordLinkMap(database, "msg_id", msgIDs, lookup.ByMsg, lookup.QuestionByMsg); err != nil {
		return err
	}
	return fillSQLiteRecordLinkMap(database, "comment_a_id", commentIDs, lookup.ByComment, lookup.QuestionByComment)
}

func fillSQLiteRecordLinkMap(database *sql.DB, column string, ids []int64, links map[int64]int64, questions map[int64]string) error {
	if len(ids) == 0 {
		return nil
	}
	query := fmt.Sprintf("SELECT %s, link_id, COALESCE(comment_text, '') FROM at WHERE %s IN (%s)", column, column, sqlitePlaceholders(len(ids)))
	args := int64Args(ids)
	rows, err := database.Query(query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var linkID int64
		var question string
		if err := rows.Scan(&id, &linkID, &question); err != nil {
			return err
		}
		if id > 0 && linkID > 0 {
			links[id] = linkID
		}
		if id > 0 && strings.TrimSpace(question) != "" {
			questions[id] = question
		}
	}
	return rows.Err()
}

func fillPostgresRecordLinkLookup(cfg appConfig, msgIDs, commentIDs []int64, lookup *recordLinkLookup) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, postgresDSN(cfg))
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := fillPostgresRecordLinkMap(ctx, pool, "msg_id", msgIDs, lookup.ByMsg, lookup.QuestionByMsg); err != nil {
		return err
	}
	return fillPostgresRecordLinkMap(ctx, pool, "comment_a_id", commentIDs, lookup.ByComment, lookup.QuestionByComment)
}

func fillPostgresRecordLinkMap(ctx context.Context, pool *pgxpool.Pool, column string, ids []int64, links map[int64]int64, questions map[int64]string) error {
	if len(ids) == 0 {
		return nil
	}
	query := fmt.Sprintf("SELECT %s, link_id, COALESCE(comment_text, '') FROM at WHERE %s IN (%s)", column, column, postgresPlaceholders(len(ids)))
	args := int64Args(ids)
	rows, err := pool.Query(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var linkID int64
		var question string
		if err := rows.Scan(&id, &linkID, &question); err != nil {
			return err
		}
		if id > 0 && linkID > 0 {
			links[id] = linkID
		}
		if id > 0 && strings.TrimSpace(question) != "" {
			questions[id] = question
		}
	}
	return rows.Err()
}

func sqlitePlaceholders(count int) string {
	values := make([]string, count)
	for i := range values {
		values[i] = "?"
	}
	return strings.Join(values, ",")
}

func postgresPlaceholders(count int) string {
	values := make([]string, count)
	for i := range values {
		values[i] = fmt.Sprintf("$%d", i+1)
	}
	return strings.Join(values, ",")
}

func int64Args(values []int64) []any {
	args := make([]any, len(values))
	for i, value := range values {
		args[i] = value
	}
	return args
}

func readRecentLogFile(path string, cutoff time.Time) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	var lines []string
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		timeValue, ok := parseLogLineTime(line)
		if !ok || timeValue.Before(cutoff) {
			continue
		}
		lines = append(lines, line)
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return strings.Join(lines, "\n"), nil
}

func parseLogLineTime(line string) (time.Time, bool) {
	value := timeFromLogLine(line)
	if value == "" {
		return time.Time{}, false
	}
	layout := "2006-01-02"
	if strings.Contains(value, " ") {
		layout = "2006-01-02 15:04:05"
	}
	parsed, err := time.ParseInLocation(layout, value, time.Local)
	if err != nil {
		return time.Time{}, false
	}
	return parsed, true
}

func readWholeFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(data), "\n"), nil
}

func (s *serverState) readTokenRecords(logContent string) []tokenRecord {
	path := filepath.Join(s.rootDir, tokenRecordFileName)
	records, err := readTokenRecordFile(path)
	if err == nil && len(records) > 0 {
		return records
	}
	backfill := tokenRecordsFromLogs(logContent)
	if len(backfill) > 0 {
		writeTokenRecordFileIfEmpty(path, backfill)
	}
	return backfill
}

func readTokenRecordFile(path string) ([]tokenRecord, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var records []tokenRecord
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var record tokenRecord
		if err := json.Unmarshal([]byte(line), &record); err != nil || record.Tokens <= 0 {
			continue
		}
		records = append(records, record)
	}
	if err := scanner.Err(); err != nil {
		return records, err
	}
	return records, nil
}

func writeTokenRecordFileIfEmpty(path string, records []tokenRecord) {
	info, err := os.Stat(path)
	if err == nil && info.Size() > 0 {
		return
	}
	var builder strings.Builder
	encoder := json.NewEncoder(&builder)
	for _, record := range records {
		if record.Tokens > 0 {
			_ = encoder.Encode(record)
		}
	}
	if builder.Len() == 0 {
		return
	}
	_ = os.WriteFile(path, []byte(builder.String()), 0644)
}

func tokenRecordsFromLogs(content string) []tokenRecord {
	if content == "" {
		return nil
	}
	var records []tokenRecord
	for _, line := range strings.Split(content, "\n") {
		if !strings.Contains(line, "[Ai]Ai说：") {
			continue
		}
		tokens := tokenValueFromLogLine(line)
		if tokens <= 0 {
			continue
		}
		records = append(records, tokenRecord{Time: timeFromLogLine(line), Tokens: tokens})
	}
	return records
}

func tokenValueFromLogLine(line string) int64 {
	start := strings.Index(line, "{")
	if start < 0 {
		return 0
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(line[start:]), &payload); err != nil {
		return 0
	}
	for _, key := range []string{"本次消耗token", "total_tokens", "totalToken", "tokens"} {
		value, ok := payload[key]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case float64:
			if typed > 0 {
				return int64(typed)
			}
		case int64:
			if typed > 0 {
				return typed
			}
		case string:
			var parsed int64
			if _, err := fmt.Sscan(typed, &parsed); err == nil && parsed > 0 {
				return parsed
			}
		}
	}
	return 0
}

func timeFromLogLine(line string) string {
	match := logTimePattern.FindStringSubmatch(line)
	if len(match) == 0 {
		return ""
	}
	if len(match) > 2 && match[2] != "" {
		return match[1] + " " + match[2]
	}
	return match[1]
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

func (s *serverState) readJournalAll() (string, error) {
	return s.readJournalSince(time.Time{})
}

func (s *serverState) readJournalSince(since time.Time) (string, error) {
	args := []string{"-u", s.service}
	if !since.IsZero() {
		args = append(args, "--since", since.Format("2006-01-02 15:04:05"))
	}
	args = append(args, "--no-pager", "-o", "short-iso")
	cmd := exec.Command("journalctl", args...)
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
    button,input,select,textarea{font:inherit}.hidden{display:none!important}.shell{position:relative;width:min(1420px,calc(100vw - 56px));margin:0 auto;padding:18px 0 48px}.topnav{height:64px;display:flex;align-items:center;justify-content:space-between;gap:18px;padding:0 16px 0 20px;border:1px solid #dfe6ef;border-radius:28px;background:rgba(255,255,255,.84);box-shadow:var(--soft);backdrop-filter:blur(16px);position:sticky;top:14px;z-index:5}.brand{display:flex;align-items:center;gap:12px;min-width:220px}.logo{width:38px;height:38px;border-radius:14px;background:linear-gradient(145deg,#fff1f6,#ffd7e5);box-shadow:inset 0 -8px 18px rgba(255,135,174,.2);display:grid;place-items:center;color:#d45d88;font-weight:900}.brand strong{font-size:18px}.brand small{color:var(--muted);font-size:12px}.navlinks{display:flex;align-items:center;gap:8px;flex:1;justify-content:center}.navlinks button{border:0;background:transparent;color:#4b5563;border-radius:999px;padding:10px 16px;cursor:pointer;font-weight:800}.navlinks button.active{background:#eef3f8;color:#111827;box-shadow:inset 0 0 0 1px #e3eaf3}.right-tools{display:flex;align-items:center;gap:10px}.tool-pill{display:inline-flex;align-items:center;gap:8px;border:1px solid #dfe6ef;border-radius:999px;background:#fff;padding:9px 13px;color:#475467;font-weight:800}.avatar-button{width:42px;height:42px;border:3px solid #fff;border-radius:50%;padding:0;background:#fff;box-shadow:0 8px 20px rgba(36,50,74,.16);overflow:hidden;cursor:pointer}.avatar-button.active{outline:4px solid rgba(22,132,226,.14)}.avatar-button img{width:100%;height:100%;object-fit:cover;display:block}.dot{width:9px;height:9px;border-radius:50%;background:var(--red);box-shadow:0 0 0 5px rgba(222,48,56,.12)}.dot.on{background:var(--green);box-shadow:0 0 0 5px rgba(8,185,158,.13)}
    .login{min-height:72vh;display:grid;place-items:center}.login-card{width:min(470px,100%);padding:36px;border-radius:28px;background:var(--paper);box-shadow:var(--shadow);text-align:center}.catgirl{position:relative;width:126px;height:126px;margin:0 auto 18px;border-radius:40px;background:linear-gradient(145deg,#fff8fb,#ffe4ef 48%,#fff);box-shadow:inset 0 -12px 30px rgba(255,156,183,.22),var(--soft);display:grid;place-items:center;color:#d35d88;font-size:28px;font-weight:900}.catgirl:before,.catgirl:after{content:"";position:absolute;top:-12px;width:46px;height:46px;background:#ffe3ee;border:7px solid #fff;border-radius:14px;transform:rotate(45deg);box-shadow:var(--soft)}.catgirl:before{left:14px}.catgirl:after{right:14px}.catgirl b{position:relative;z-index:1}.login-card h1{margin:0 0 8px;font-size:30px}.login-card p{margin:0 0 22px;color:var(--muted);line-height:1.7}.input,select,textarea{width:100%;border:1px solid var(--line);background:#fbfcfe;color:var(--ink);border-radius:16px;padding:14px 15px;outline:none}textarea{min-height:110px;resize:vertical}.input:focus,select:focus,textarea:focus{border-color:rgba(22,132,226,.55);box-shadow:0 0 0 4px rgba(22,132,226,.09)}.toast{min-height:22px;margin-top:14px;color:var(--red);font-size:13px}
    .layout{display:grid;grid-template-columns:260px 1fr;gap:26px;margin-top:24px}.side{padding:18px}.new-chat{width:100%;height:46px;border:0;border-radius:22px;background:var(--dark);color:#fff;font-weight:900;cursor:pointer;box-shadow:var(--soft)}.side-link{width:100%;height:44px;margin-top:12px;border:1px solid #dfe6ef;border-radius:20px;background:#fff;color:#2563eb;font-weight:900;cursor:pointer;box-shadow:var(--soft)}.side-card{margin-top:14px;padding:16px;border-radius:18px;background:#fff;box-shadow:var(--soft)}.service-card{margin-top:22px}.side-card strong{display:block;margin-bottom:8px}.side-card p{margin:0;color:var(--muted);font-size:13px;line-height:1.5}.content{min-width:0}.view{display:none}.view.active{display:block}.hero-card{padding:24px 26px;border-radius:26px;background:#fff;box-shadow:var(--shadow)}.hero-head{display:flex;align-items:center;justify-content:space-between;gap:16px}.hero-title h1{margin:0;font-size:28px}.hero-title p{margin:7px 0 0;color:var(--muted)}.panel-actions{display:flex;gap:10px;flex-wrap:wrap}button.primary,button.secondary,button.danger,button.warn{border:0;cursor:pointer;border-radius:14px;padding:12px 17px;font-weight:900;transition:.18s ease}button.primary{color:#fff;background:var(--blue);box-shadow:0 8px 18px rgba(22,132,226,.2)}button.secondary{color:#2563eb;background:#edf6ff}button.danger{color:#fff;background:var(--red);box-shadow:0 8px 18px rgba(222,48,56,.18)}button.warn{color:#5a3a00;background:var(--amber);box-shadow:0 8px 18px rgba(255,196,92,.22)}button:hover{transform:translateY(-1px);filter:brightness(1.03)}button:disabled{opacity:.45;cursor:not-allowed;transform:none}.cards{display:grid;grid-template-columns:repeat(5,minmax(138px,1fr));gap:20px;margin-top:24px}.card{background:#fff;border-radius:22px;box-shadow:var(--shadow);border:1px solid rgba(255,255,255,.8)}.stat{min-height:124px;min-width:0;padding:20px;text-align:center;display:grid;align-content:center;gap:12px;overflow:hidden}.stat span{color:#4c5566;font-size:16px}.stat strong{max-width:100%;font-size:clamp(30px,3vw,42px);line-height:1;font-weight:900;letter-spacing:-.05em;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}.green{color:var(--green)}.blue{color:var(--blue)}.red{color:var(--red)}.amber{color:#f7b23c}.violet{color:var(--violet)}.grid-2{display:grid;grid-template-columns:1fr 1fr;gap:24px;margin-top:24px}.panel{padding:24px}.panel-head{display:flex;align-items:center;justify-content:space-between;gap:16px;margin-bottom:18px}.panel h2{margin:0;font-size:22px}.panel p{margin:6px 0 0;color:var(--muted)}.control-grid{display:grid;grid-template-columns:150px 1fr;gap:20px;align-items:center}.meta{display:grid;gap:12px}.meta div{display:grid;gap:4px}.meta span{font-size:12px;color:var(--muted)}.meta strong{font-size:13px;word-break:break-all;white-space:pre-wrap}.status-text{max-height:130px;overflow:auto}.warnbox{border:1px solid #ffe0a3;background:#fff8e8;color:#7a4f00;border-radius:14px;padding:12px 14px;margin-top:16px;font-size:13px;line-height:1.55}.chart{height:220px;border-top:1px solid var(--line);display:grid;grid-template-columns:repeat(7,1fr);align-items:end;gap:18px;padding:24px 12px 4px}.bar-wrap{text-align:center;color:var(--muted);font-size:13px}.bar-num{height:22px;color:#4c5566}.bar{width:58px;max-width:100%;height:8px;margin:6px auto 10px;border-radius:8px 8px 2px 2px;background:linear-gradient(180deg,var(--green),#10c6aa);box-shadow:0 8px 18px rgba(8,185,158,.2)}.records{margin-top:24px;padding:24px}.table-wrap{overflow:auto;border-top:1px solid var(--line);padding-top:18px}table{width:100%;border-collapse:collapse;min-width:900px;table-layout:fixed}th{background:#f6f8fb;color:#4c5566;text-align:left;font-size:15px;padding:14px 12px}th:nth-child(1){width:160px}th:nth-child(2){width:170px}th:nth-child(5){width:92px}th:nth-child(6){width:92px}th:nth-child(7){width:118px}td{padding:14px 12px;border-bottom:1px solid var(--line);font-size:14px;vertical-align:top}.badge{display:inline-flex;align-items:center;justify-content:center;min-width:64px;padding:6px 10px;border-radius:999px;font-size:12px;font-weight:900;color:#fff}.badge.info{background:var(--blue)}.badge.error{background:var(--red)}.badge.warn{background:#f0a81f}.badge.ok{background:var(--green)}.copy-btn{border:0;border-radius:999px;padding:6px 10px;background:#edf6ff;color:#2563eb;font-size:12px;font-weight:900;cursor:pointer;margin-left:8px}.action-stack{display:flex;flex-direction:column;align-items:flex-start;gap:7px}.action-stack .copy-btn{margin-left:0}.action-feedback{font-size:12px;line-height:1.35;color:var(--muted)}.action-feedback.ok{color:var(--green)}.action-feedback.error{color:var(--red)}.action-feedback.pending{color:var(--blue)}.content-cell{line-height:1.55;user-select:text}.clip-cell{max-height:5.1em;overflow:auto;overflow-wrap:anywhere;word-break:break-word;padding-right:4px}.clip-cell::-webkit-scrollbar{width:6px}.clip-cell::-webkit-scrollbar-thumb{background:#d5dce8;border-radius:999px}.log-panel{overflow:hidden}.log-head{display:grid;grid-template-columns:minmax(220px,1fr) minmax(520px,1.7fr);align-items:start;gap:18px;padding:22px 24px;border-bottom:1px solid var(--line)}.log-tools{display:grid;gap:10px}.log-filterbar,.log-buttonbar{display:flex;align-items:center;justify-content:flex-end;gap:10px;flex-wrap:wrap}.log-filterbar select{width:auto;min-width:150px}.log-filterbar input{width:min(260px,100%);padding:10px 12px}.log-buttonbar .copy-btn{margin-left:0;white-space:nowrap}.terminal{height:min(56vh,590px);overflow:auto;background:#101724;color:#d9e7ff;padding:18px 22px;border-radius:0 0 22px 22px;user-select:text;cursor:text}.terminal::-webkit-scrollbar{width:10px;height:10px}.terminal::-webkit-scrollbar-thumb{background:#2f3d52;border:2px solid #101724;border-radius:999px}.terminal::-webkit-scrollbar-track{background:#101724}pre{margin:0;white-space:pre-wrap;word-break:break-word;font:13px/1.62 ui-monospace,SFMono-Regular,Menlo,Consolas,"Liberation Mono",monospace;user-select:text;cursor:text}.log-line{display:block;min-height:1.62em;margin:4px 0;padding:7px 10px;border:1px solid rgba(255,255,255,.045);border-radius:10px;background:rgba(255,255,255,.025);cursor:pointer}.log-line:hover{background:rgba(255,255,255,.07)}.log-line.selected{background:rgba(22,132,226,.22);color:#fff}.log-line.copied{background:rgba(8,185,158,.18);color:#fff}.empty{color:var(--muted);display:grid;place-items:center;text-align:center;min-height:230px;background:#fff}.settings-hero{display:grid;grid-template-columns:120px 1fr;gap:22px;align-items:center;padding:26px;border-radius:24px;background:linear-gradient(135deg,#fff7fb,#eef6ff);border:1px solid #fff;box-shadow:var(--soft)}.settings-hero h2{margin:0 0 8px;font-size:28px}.settings-hero p{margin:0;color:var(--muted);line-height:1.65}.config-form{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:16px}.config-group{grid-column:1/-1;display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:14px;padding:18px;border:1px solid var(--line);border-radius:22px;background:linear-gradient(180deg,#fff,#fbfcfe)}.config-group h3{grid-column:1/-1;margin:0 0 2px;color:var(--blue);font-size:16px;display:flex;align-items:center;gap:8px}.config-group h3:before{content:"";width:8px;height:8px;border-radius:50%;background:var(--blue);box-shadow:0 0 0 5px rgba(22,132,226,.1)}.field{display:grid;gap:7px}.field label{font-size:12px;color:var(--muted)}.hint{color:var(--muted);font-size:12px;line-height:1.45}.field.wide{grid-column:1/-1}.switch{display:flex;align-items:center;justify-content:space-between;gap:12px;border:1px solid var(--line);border-radius:16px;padding:13px;background:#fbfcfe}.switch input{width:22px;height:22px}.settings-grid{display:grid;grid-template-columns:repeat(2,1fr);gap:16px;margin-top:18px}.setting{position:relative;overflow:hidden;padding:20px;border:1px solid var(--line);border-radius:20px;background:#fbfcfe}.setting:before{content:"";position:absolute;inset:0 0 auto;height:4px;background:linear-gradient(90deg,var(--blue),#ff91b8)}.setting span{display:block;color:var(--muted);font-size:13px;margin-bottom:9px}.setting strong{display:block;font-size:17px;line-height:1.45;word-break:break-all}.setting small{display:block;margin-top:8px;color:var(--muted);line-height:1.5}.setting-wide{grid-column:1/-1}.setting-actions{display:flex;gap:10px;flex-wrap:wrap;margin-top:18px}.token-summary{display:grid;grid-template-columns:repeat(3,1fr);gap:12px}.token-summary div{padding:18px;border:1px solid var(--line);border-radius:18px;background:#fbfcfe}.token-summary span{display:block;color:var(--muted);font-size:13px;margin-bottom:8px}.token-summary strong{font-size:32px}.mobile-tabs{display:none}
    @media(max-width:1180px){.cards{grid-template-columns:repeat(2,1fr)}.grid-2{grid-template-columns:1fr}.layout{grid-template-columns:1fr}.side{display:none}.navlinks{display:none}.mobile-tabs{display:block;margin-top:18px}.mobile-tabs select{background:#fff}.topnav{position:relative;top:0}.brand{min-width:0}.right-tools .tool-pill:first-child{display:none}}@media(max-width:700px){.shell{width:min(100vw - 24px,1420px);padding-top:12px}.topnav{border-radius:20px}.cards{grid-template-columns:1fr}.hero-head,.panel-head{align-items:stretch;flex-direction:column}.log-head{grid-template-columns:1fr}.log-filterbar,.log-buttonbar{justify-content:stretch}.log-filterbar select,.log-filterbar input,.log-buttonbar button{width:100%}.control-grid,.settings-hero,.settings-grid,.token-summary,.config-form,.config-group{grid-template-columns:1fr}.content-cell{white-space:normal}.chart{gap:10px;padding-inline:4px}.bar{width:34px}.stat strong{font-size:36px}}
  </style>
</head>
<body data-authed="{{.Authed}}">
  <main class="shell">
    <header class="topnav">
      <div class="brand"><div class="logo">猫</div><div><strong>小黑盒猫娘</strong><br><small>VPS 控制台</small></div></div>
      <nav class="navlinks"><button class="nav active" data-view="home">主控台</button><button class="nav" data-view="records">@ 回复记录</button><button class="nav" data-view="feed-records">自动刷帖记录</button><button class="nav" data-view="logs">日志管理</button><button class="nav" data-view="config">配置管理</button><button class="nav" data-view="service">服务控制</button><button class="nav" data-view="status">系统状态</button></nav>
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
      <div class="mobile-tabs"><select id="mobileNav"><option value="home">主控台</option><option value="records">@ 回复记录</option><option value="feed-records">自动刷帖记录</option><option value="logs">日志管理</option><option value="config">配置管理</option><option value="service">服务控制</option><option value="status">系统状态</option></select></div>
      <div class="layout">
        <aside class="side">
          <button class="new-chat" data-view-button="home">主控首页</button>
          <button class="side-link" data-view-button="records" type="button">最近24小时 @ 回复记录</button>
          <button class="side-link" data-view-button="feed-records" type="button">自动刷帖记录</button>
          <div class="side-card"><strong>猫娘面板</strong><p>主控台放首页，@ 回复记录和自动刷帖记录可从左侧单独查看。</p></div>
          <div class="side-card service-card"><strong>当前服务</strong><p>{{.Service}}</p></div>
        </aside>
        <div class="content">
          <section class="view active" id="view-home">
            <div class="hero-card"><div class="hero-head"><div class="hero-title"><h1>主控台</h1><p>汇总所有可读取日志中的提问、回复、失败和 token 消耗。</p></div><div class="panel-actions"><button id="homeRefreshBtn" class="secondary" type="button">刷新</button><button class="secondary" data-view-button="records" type="button">查看 @ 回复记录</button><button id="homeRestartBtn" class="warn" type="button">重启服务</button></div></div></div>
            <section class="cards"><div class="card stat"><span>提问次数</span><strong id="metricStatus" class="violet">0</strong></div><div class="card stat"><span>回复成功</span><strong id="metricLines" class="green">0</strong></div><div class="card stat"><span>失败次数</span><strong id="metricErrors" class="red">0</strong></div><div class="card stat"><span>待处理</span><strong id="metricFiles" class="blue">0</strong></div><div class="card stat"><span>Web 端口</span><strong id="metricPort" class="amber">29173</strong></div></section>
            <section class="grid-2"><div class="card panel"><div class="panel-head"><div><h2>Token 记录</h2><p>按全部记录、最近 1 小时和最近 24 小时汇总 AI token 消耗。</p></div></div><div class="token-summary"><div><span>总 token</span><strong id="tokenTotal" class="green">0</strong></div><div><span>最近 1 小时</span><strong id="tokenHour" class="blue">0</strong></div><div><span>最近 24 小时</span><strong id="tokenDay" class="violet">0</strong></div></div><div id="appToast" class="toast"></div></div><div class="card panel"><div class="panel-head"><div><h2>最近 7 次日志趋势</h2><p>按全部@ 回复记录聚合；无日期时归入今日。</p></div></div><div id="chart" class="chart"></div></div></section>
          </section>

          <section class="view" id="view-records">
            <div class="hero-card"><div class="hero-head"><div class="hero-title"><h1>@ 回复记录</h1><p id="recordsMeta">正在读取最近24小时 @ 回复记录...</p></div><div class="panel-actions"><button id="recordsRefreshBtn" class="secondary" type="button">刷新记录</button><button class="secondary" data-view-button="feed-records" type="button">查看自动刷帖记录</button><button class="secondary" data-view-button="home" type="button">返回主控台</button></div></div><div id="recordsToast" class="toast"></div></div>
            <section class="card records"><div class="panel-head"><div><h2>最近24小时 @ 回复</h2><p>按顺序配对最近 24 小时内的“小黑盒用户提问”和“机器人回复”，失败和异常发送会单独标记。</p></div></div><div class="table-wrap"><table><thead><tr><th>时间</th><th>用户</th><th>用户说</th><th>机器人回复</th><th>状态</th><th>帖子</th><th>操作</th></tr></thead><tbody id="recordsBody"><tr><td colspan="7">等待日志...</td></tr></tbody></table></div></section>
          </section>

          <section class="view" id="view-feed-records">
            <div class="hero-card"><div class="hero-head"><div class="hero-title"><h1>自动刷帖记录</h1><p id="feedRecordsMeta">正在读取最近24小时自动刷帖记录...</p></div><div class="panel-actions"><button id="feedRecordsRefreshBtn" class="secondary" type="button">刷新记录</button><button class="secondary" data-view-button="records" type="button">查看 @ 回复记录</button><button class="secondary" data-view-button="home" type="button">返回主控台</button></div></div><div id="feedRecordsToast" class="toast"></div></div>
            <section class="card records"><div class="panel-head"><div><h2>最近24小时自动刷帖</h2><p>显示 feeds 自动生成的试运行、已发送、跳过和失败记录。</p></div></div><div class="table-wrap"><table><thead><tr><th>时间</th><th>帖子</th><th>作者</th><th>AI 回复</th><th>状态</th><th>原因</th><th>打开</th></tr></thead><tbody id="feedRecordsBody"><tr><td colspan="7">等待记录...</td></tr></tbody></table></div></section>
          </section>

          <section class="view" id="view-logs"><div class="card log-panel"><div class="log-head"><div><h2>日志管理</h2><p id="currentSource">等待日志源...</p></div><div class="log-tools"><div class="log-filterbar"><select id="logSelect"></select><select id="logFilter"><option value="all">全部</option><option value="error">只看报错</option><option value="ask">用户提问</option><option value="reply">AI 回复</option><option value="image">图片/生图</option><option value="feed">自动刷帖</option></select><input id="logKeyword" class="input" type="search" placeholder="关键词筛选"></div><div class="log-buttonbar"><button id="copySelectedLogBtn" class="copy-btn" type="button">复制选中</button><button id="copyLogBtn" class="copy-btn" type="button">复制全部</button><button id="toggleLogRefreshBtn" class="copy-btn" type="button">暂停刷新</button></div></div></div><div class="terminal"><pre id="logOutput" class="empty" tabindex="0">等待日志...</pre></div></div></section>

          <section class="view" id="view-service"><div class="card panel"><div class="panel-head"><div><h2>服务控制</h2><p>启动、停止或重启 Openxhh systemd 服务。</p></div></div><div class="panel-actions"><button id="serviceStartBtn" class="primary">启动服务</button><button id="serviceRestartBtn" class="warn">重启服务</button><button id="serviceStopBtn" class="danger">停止服务</button><button id="serviceRefreshBtn" class="secondary">刷新状态</button></div><div class="warnbox">如果按钮报错，请确认 Web UI 运行用户有权限执行 systemctl。</div></div></section>

          <section class="view" id="view-config"><div class="card panel"><div class="panel-head"><div><h2>配置管理</h2><p>保存后写入工作目录下的 <strong id="configPath">config.json</strong>；运行中的机器人需要重启后读取新配置。</p></div><div class="panel-actions"><button id="saveConfigBtn" class="primary" type="submit" form="configForm">保存配置</button><button id="configRestartBtn" class="secondary" type="button">重启服务</button></div></div><form id="configForm" class="config-form"><div class="config-group"><h3>小黑盒</h3><div class="field"><label>检查间隔/秒</label><input class="input" data-path="xhh.checkTime" data-type="number"></div><div class="field"><label>回复间隔/秒</label><input class="input" data-path="xhh.replyTime" data-type="number"></div><div class="field"><label>最高回复线程</label><input class="input" data-path="xhh.maxReplyThreads" data-type="number"></div><div class="field"><label>最大待回复队列</label><input class="input" data-path="xhh.maxPendingReplies" data-type="number"></div><div class="field"><label>单用户待回复上限</label><input class="input" data-path="xhh.maxPendingRepliesPerUser" data-type="number"></div><label class="switch field wide"><span>启用白名单（关闭时回复所有 @，仍识别 owner）</span><input data-path="xhh.enableWhitelist" data-type="bool" type="checkbox"></label><div class="field wide"><label>Owner / 白名单 UID（英文逗号分隔）</label><input class="input" data-path="xhh.owner"></div><div class="field"><label>Device ID</label><input class="input" data-path="xhh.deviceID"></div><div class="field wide"><label>API Base URL</label><input class="input" data-path="xhh.baseUrl"></div><div class="field"><label>Web Version</label><input class="input" data-path="xhh.webver"></div><div class="field"><label>Version</label><input class="input" data-path="xhh.version"></div></div><div class="config-group"><h3>数据库</h3><div class="field"><label>类型</label><select data-path="database.type"><option value="sqlite">sqlite</option><option value="pg">pg</option></select></div><div class="field"><label>数据库名</label><input class="input" data-path="database.db"></div><div class="field"><label>Host</label><input class="input" data-path="database.host"></div><div class="field"><label>Port</label><input class="input" data-path="database.port"></div><div class="field"><label>User</label><input class="input" data-path="database.user"></div><div class="field"><label>Password</label><input class="input" data-path="database.passwd" type="password"></div></div><div class="config-group"><h3>AI 回复</h3><div class="field"><label>模型</label><input class="input" data-path="ai.model"></div><div class="field wide"><label>Chat Completions / Responses URL</label><input class="input" data-path="ai.baseUrl"><small class="hint">例如：https://xxx.com/v1/chat/completions 或 https://xxx.com/v1/responses</small></div><div class="field wide"><label>Token</label><input class="input" data-path="ai.token" type="password"></div><label class="switch field wide"><span>启用模型联网搜索</span><input data-path="ai.webSearch" data-type="bool" type="checkbox"></label><label class="switch field wide"><span>强制每次回复使用联网搜索</span><input data-path="ai.forceWebSearch" data-type="bool" type="checkbox"></label><div class="field"><label>搜索上下文大小</label><select data-path="ai.searchContextSize"><option value="low">low</option><option value="medium">medium</option><option value="high">high</option></select></div><div class="field wide"><label>回复策略 Prompt</label><textarea data-path="ai.prompt"></textarea></div></div><div class="config-group"><h3>自动刷帖回复</h3><label class="switch field wide"><span>启用自动刷帖回复</span><input data-path="feedReply.enabled" data-type="bool" type="checkbox"></label><label class="switch field wide"><span>仅试运行，不真正发送评论</span><input data-path="feedReply.dryRun" data-type="bool" type="checkbox"></label><div class="field"><label>刷帖间隔/秒</label><input class="input" data-path="feedReply.interval" data-type="number"></div><div class="field"><label>每轮最多处理</label><input class="input" data-path="feedReply.maxPerRun" data-type="number"></div><div class="field"><label>每日最多处理</label><input class="input" data-path="feedReply.maxPerDay" data-type="number"></div><div class="field wide"><label>自动刷帖 Prompt</label><textarea data-path="feedReply.prompt"></textarea></div></div><div class="config-group"><h3>图片能力</h3><div class="field"><label>模型</label><input class="input" data-path="image.model"></div><div class="field"><label>尺寸</label><input class="input" data-path="image.size"></div><div class="field wide"><label>Images Generations URL</label><input class="input" data-path="image.baseUrl"><small class="hint">例如：https://xxx.com/v1/images/generations</small></div><div class="field wide"><label>图片 Token</label><input class="input" data-path="image.token" type="password"></div><div class="field"><label>输出格式</label><input class="input" data-path="image.responseFormat"></div><div class="field"><label>输出目录</label><input class="input" data-path="image.outputDir"></div><div class="field"><label>上传模式</label><input class="input" data-path="image.uploadMode"></div><div class="field"><label>外部图片目录</label><input class="input" data-path="image.externalDir"></div><div class="field wide"><label>外部图片访问 URL</label><input class="input" data-path="image.externalBaseUrl"></div><label class="switch field wide"><span>启用图片 Prompt 优化</span><input data-path="image.promptRefine" data-type="bool" type="checkbox"></label><div class="field"><label>Prompt 优化模型</label><input class="input" data-path="image.promptModel"></div><div class="field"><label>Prompt 最大字符数</label><input class="input" data-path="image.promptMaxChars" data-type="number"></div><div class="field wide"><label>Prompt 优化 URL</label><input class="input" data-path="image.promptBaseUrl"><small class="hint">例如：https://xxx.com/v1/chat/completions</small></div><div class="field wide"><label>Prompt 优化 Token</label><input class="input" data-path="image.promptToken" type="password"></div></div></form><div id="configToast" class="toast"></div><div class="warnbox">保存配置不会自动重启服务；改白名单、线程数、模型或 token 后，请到“服务控制”重启 Openxhh。</div></div></section>

          <section class="view" id="view-status"><div class="card panel"><div class="panel-head"><div><h2>系统状态</h2><p>当前 Web UI 与 Openxhh 服务信息。</p></div></div><div class="meta"><div><span>监听地址</span><strong id="listenAddr">—</strong></div><div><span>工作目录</span><strong id="rootDir">—</strong></div><div><span>systemctl status</span><strong id="statusText" class="status-text">—</strong></div></div></div></section>

          <section class="view" id="view-settings"><div class="card panel"><div class="settings-hero"><button class="avatar-button" type="button" aria-label="管理员头像"><img src="/assets/admin-avatar.png" alt="管理员"></button><div><h2>管理员设置</h2><p>这里集中展示 Web UI 的公开访问、认证和运行配置。机器人配置可在配置管理页编辑，cookie 等登录态不在公网面板编辑。</p></div></div><div class="settings-grid"><div class="setting"><span>systemd 服务</span><strong>{{.Service}}</strong><small>主控台按钮会对这个服务执行 start / stop / restart。</small></div><div class="setting"><span>Web UI 端口</span><strong>29173</strong><small>默认公网监听；建议只在云安全组放行你的固定 IP。</small></div><div class="setting"><span>认证方式</span><strong>随机强密码</strong><small>首次启动打印密码，本地仅保存 salted hash。</small></div><div class="setting"><span>失败限速</span><strong>5 次失败锁定 5 分钟</strong><small>降低公网暴力尝试风险。</small></div><div class="setting setting-wide"><span>安全建议</span><strong>公网访问建议配合 HTTPS 反代或安全组白名单</strong><small>如果只是自己使用，优先只开放可信来源 IP；不要把 webui_auth.json、config.json、cookie.json 上传到公开仓库。</small></div></div><div class="setting-actions"><button id="settingsHomeBtn" class="secondary" type="button">返回主控台</button><button id="logoutBtn" class="danger" type="button">退出登录</button></div></div></section>
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
const recordsToast=document.querySelector('#recordsToast');
const recordsMeta=document.querySelector('#recordsMeta');
const feedRecordsToast=document.querySelector('#feedRecordsToast');
const feedRecordsMeta=document.querySelector('#feedRecordsMeta');
const logSelect=document.querySelector('#logSelect');
const logFilter=document.querySelector('#logFilter');
const logKeyword=document.querySelector('#logKeyword');
const logOutput=document.querySelector('#logOutput');
const recordsBody=document.querySelector('#recordsBody');
const feedRecordsBody=document.querySelector('#feedRecordsBody');
const chart=document.querySelector('#chart');
const configForm=document.querySelector('#configForm');
const configToast=document.querySelector('#configToast');
let currentLog='';
let currentLogLabel='';
let rawLogContent='';
let logTimer=null;
let statusTimer=null;
let recordsTimer=null;
let feedRecordsTimer=null;
let logFilterTimer=null;
let activeView='home';
let logScrollLatestOnce=false;
let logPaused=false;
let lastSelectedLogLine=-1;
let recordsSignature='';
let feedRecordsSignature='';
const recordScrollMemory=new Map();

function showApp(ok){loginView.classList.toggle('hidden',ok);appView.classList.toggle('hidden',!ok);if(ok){switchView('home');bootstrap()}}
async function api(path,options={}){const res=await fetch(path,{headers:{'Content-Type':'application/json'},credentials:'same-origin',...options});const data=await res.json().catch(()=>({}));if(!res.ok)throw new Error(data.error||'请求失败');return data}
function switchView(name){const previous=activeView;activeView=name;document.querySelectorAll('.view').forEach(view=>view.classList.toggle('active',view.id==='view-'+name));document.querySelectorAll('.nav').forEach(btn=>btn.classList.toggle('active',btn.dataset.view===name));document.querySelector('#adminMenuBtn')?.classList.toggle('active',name==='settings');const mobile=document.querySelector('#mobileNav');if(mobile&&name!=='settings')mobile.value=name;if(name==='logs'&&previous!=='logs'){logScrollLatestOnce=true;loadCurrentLog()}if(name==='records'){if(recordsMeta)recordsMeta.textContent='正在读取最近24小时 @ 回复记录...';loadAllRecords()}if(name==='feed-records'){if(feedRecordsMeta)feedRecordsMeta.textContent='正在读取最近24小时自动刷帖记录...';loadFeedRecords()}}
document.querySelectorAll('[data-view], [data-view-button]').forEach(el=>el.addEventListener('click',()=>switchView(el.dataset.view||el.dataset.viewButton)));
document.querySelector('#adminMenuBtn')?.addEventListener('click',()=>switchView('settings'));
document.querySelector('#settingsHomeBtn')?.addEventListener('click',()=>switchView('home'));
document.querySelector('#mobileNav')?.addEventListener('change',event=>switchView(event.target.value));

document.querySelector('#loginForm').addEventListener('submit',async event=>{event.preventDefault();loginToast.textContent='';try{await api('/login',{method:'POST',body:JSON.stringify({password:document.querySelector('#password').value})});showApp(true)}catch(err){loginToast.textContent=err.message}});
function bindAction(ids,path,text){for(const id of ids){const el=document.querySelector('#'+id);if(el)el.addEventListener('click',()=>action(path,text))}}
bindAction(['serviceStartBtn'],'/api/start','启动命令已发送');
bindAction(['serviceStopBtn'],'/api/stop','停止命令已发送');
bindAction(['serviceRestartBtn','homeRestartBtn','configRestartBtn'],'/api/restart','重启命令已发送');
for(const id of ['homeRefreshBtn','serviceRefreshBtn']){const el=document.querySelector('#'+id);if(el)el.addEventListener('click',()=>{refreshStatus();loadLogs();if(id==='homeRefreshBtn')loadAllRecords(true)})}
document.querySelector('#recordsRefreshBtn')?.addEventListener('click',()=>loadAllRecords(true));
document.querySelector('#feedRecordsRefreshBtn')?.addEventListener('click',()=>loadFeedRecords(true));
document.querySelector('#logoutBtn').addEventListener('click',async()=>{await api('/logout',{method:'POST'});location.reload()});
configForm?.addEventListener('submit',async event=>{event.preventDefault();if(configToast)configToast.textContent='';try{const data=await api('/api/config',{method:'POST',body:JSON.stringify(collectConfig())});if(configToast)configToast.textContent='配置已保存：'+(data.path||'config.json')+'；重启服务后生效'}catch(err){if(configToast)configToast.textContent=err.message}});
logSelect.addEventListener('change',()=>{currentLog=logSelect.value;currentLogLabel=logSelect.selectedOptions[0]?.textContent||currentLog;logScrollLatestOnce=true;loadCurrentLog()});
logFilter?.addEventListener('change',()=>rerenderCurrentLog());
logKeyword?.addEventListener('input',()=>{clearTimeout(logFilterTimer);logFilterTimer=setTimeout(()=>rerenderCurrentLog(),220)});
document.querySelector('#copySelectedLogBtn')?.addEventListener('click',()=>copySelectedLog());
document.querySelector('#copyLogBtn')?.addEventListener('click',()=>copyText(logOutput.dataset.raw||logOutput.textContent||''));
document.querySelector('#toggleLogRefreshBtn')?.addEventListener('click',event=>{logPaused=!logPaused;if(!logPaused){clearLogLineSelection();window.getSelection()?.removeAllRanges()}event.currentTarget.textContent=logPaused?'继续刷新':'暂停刷新';if(!logPaused)loadCurrentLog()});

async function action(path,text){if(appToast)appToast.textContent='';try{await api(path,{method:'POST'});if(appToast)appToast.textContent=text;setTimeout(refreshStatus,900);setTimeout(loadCurrentLog,1200);setTimeout(loadAllRecords,1500);setTimeout(loadFeedRecords,1500)}catch(err){if(appToast)appToast.textContent=err.message}}
async function regenerateMessage(item,button,feedback){const original=button.textContent;button.disabled=true;button.textContent='处理中';setFeedback(feedback,'正在加入待回复队列...','pending');if(recordsToast)recordsToast.textContent='';try{await api('/api/messages/regenerate',{method:'POST',body:JSON.stringify(regeneratePayload(item))});button.textContent='已加入';setFeedback(feedback,'已加入待回复队列','ok');if(recordsToast)recordsToast.textContent='已加入待回复队列，机器人下一轮会重新生成';setTimeout(loadAllRecords,900)}catch(err){button.disabled=false;button.textContent=original;setFeedback(feedback,'失败：'+err.message,'error');if(recordsToast)recordsToast.textContent=err.message}}
function setFeedback(el,text,state){if(!el)return;el.textContent=text;el.className='action-feedback '+(state||'')}
function regeneratePayload(item){return{msgId:item.msgId||0,commentId:item.commentId||0,linkId:item.linkId||0,userId:item.userId||0,userName:item.user||'',question:item.question||''}}
async function bootstrap(){clearInterval(logTimer);clearInterval(statusTimer);clearInterval(recordsTimer);clearInterval(feedRecordsTimer);await refreshStatus();await loadConfig();await loadLogs();await loadAllRecords();await loadFeedRecords();statusTimer=setInterval(refreshStatus,4000);logTimer=setInterval(loadCurrentLog,1800);recordsTimer=setInterval(loadAllRecords,10000);feedRecordsTimer=setInterval(loadFeedRecords,10000)}

async function refreshStatus(){try{const data=await api('/api/status');const running=data.running;const serviceState=document.querySelector('#serviceState');if(serviceState)serviceState.textContent=(data.active||'unknown')+(data.detail?' · '+data.detail:'');document.querySelector('#listenAddr').textContent=data.listenAddr||'—';document.querySelector('#rootDir').textContent=data.rootDir||'—';document.querySelector('#statusText').textContent=data.statusText||'—';document.querySelector('#metricPort').textContent=extractPort(data.listenAddr)||'29173';for(const id of ['serviceStartBtn'])document.querySelector('#'+id).disabled=running;for(const id of ['serviceStopBtn'])document.querySelector('#'+id).disabled=!running;topStatus.innerHTML='<span class="dot '+(running?'on':'')+'"></span><span>'+(running?'运行中':'待机')+'</span>'}catch(err){topStatus.innerHTML='<span class="dot"></span><span>认证失效</span>'}}

async function loadConfig(){if(!configForm)return;try{const data=await api('/api/config');document.querySelector('#configPath').textContent=data.path||'config.json';populateConfig(data.config||{})}catch(err){if(configToast)configToast.textContent='配置读取失败：'+err.message}}
function populateConfig(config){for(const field of configFields()){const value=getPath(config,field.dataset.path);if(field.type==='checkbox'){field.checked=!!value}else{field.value=value??''}}}
function collectConfig(){const config={};for(const field of configFields()){let value;if(field.type==='checkbox'){value=field.checked}else if(field.dataset.type==='number'){value=Number(field.value||0)}else{value=field.value}setPath(config,field.dataset.path,value)}return config}
function configFields(){return Array.from(configForm.querySelectorAll('[data-path]'))}
function getPath(obj,path){return path.split('.').reduce((acc,key)=>acc&&acc[key],obj)}
function setPath(obj,path,value){const parts=path.split('.');let cur=obj;for(let i=0;i<parts.length-1;i++){cur[parts[i]]??={};cur=cur[parts[i]]}cur[parts[parts.length-1]]=value}

async function loadLogs(){const data=await api('/api/logs');const files=data.files||[];const previous=currentLog;logSelect.innerHTML='';for(const file of files){const option=document.createElement('option');option.value=file.name;option.textContent=(file.label||file.name)+(file.size?' · '+formatBytes(file.size):'')+(file.modTime?' · '+file.modTime:'');logSelect.appendChild(option)}currentLog=files.some(file=>file.name===previous)?previous:(files[0]?.name||'');logSelect.value=currentLog;currentLogLabel=logSelect.selectedOptions[0]?.textContent||currentLog;await loadCurrentLog()}
async function loadCurrentLog(){if(logPaused||hasLogSelection()||isUsingLogControls())return;if(!currentLog){renderLog('');return}try{const data=await api('/api/logs/read?file='+encodeURIComponent(currentLog));renderLog(data.content||'')}catch(err){renderLog('日志读取失败: '+err.message)}}

function renderLog(content){rawLogContent=content||'';const box=logOutput.parentElement;const scrollTop=box.scrollTop;const shouldScrollLatest=logScrollLatestOnce;logScrollLatestOnce=false;document.querySelector('#currentSource').textContent=currentLogLabel||'暂无日志源';renderLogLines(filterLogContent(rawLogContent));if(shouldScrollLatest){box.scrollTop=box.scrollHeight;requestAnimationFrame(()=>{box.scrollTop=box.scrollHeight})}else{box.scrollTop=Math.min(scrollTop,box.scrollHeight)}}
async function loadAllRecords(manual=false){
	if(!manual&&activeView!=='home'&&activeView!=='records')return
	const recentView=activeView==='records'
	const requestPath='/api/records'+(recentView?'?window=24h':'')
	if(manual&&recordsToast)recordsToast.textContent='正在刷新'+(recentView?'最近24小时':'所有')+'记录...'
	try{
		const data=await api(requestPath)
		const lines=(data.content||'').split('\n').filter(Boolean)
		const interactions=dedupeInteractions(parseInteractions(lines))
		applyRecordLinks(interactions,data.links)
		const completed=interactions.filter(item=>item.status==='已回复'&&item.question&&item.reply)
		const failed=interactions.filter(item=>isErrorStatus(item.status)&&item.question&&item.reply).length
		const pending=interactions.filter(item=>item.status==='待回复'||item.status==='待重试').length
		const records=interactions.filter(item=>(item.status==='已回复'||isErrorStatus(item.status))&&item.question&&item.reply)
		const recentCutoff=Date.now()-86400000
		const visibleRecords=recentView?records.filter(item=>{const time=parseItemTime(item.time);return time&&time.getTime()>=recentCutoff}):records
		const tokenItems=Array.isArray(data.tokens)&&data.tokens.length?data.tokens:interactions.filter(item=>item.tokens)
		if(!recentView){
			document.querySelector('#metricStatus').textContent=formatCount(interactions.length)
			document.querySelector('#metricLines').textContent=formatCount(completed.length)
			document.querySelector('#metricErrors').textContent=formatCount(failed)
			document.querySelector('#metricFiles').textContent=formatCount(pending)
			renderTrend(completed)
			renderTokenRecords(tokenItems)
		}
		if(recentView)renderRecords(visibleRecords.slice().reverse())
		if(recordsMeta)recordsMeta.textContent=recentView?'已读取最近24小时 '+formatCount(visibleRecords.length)+' 条@ 回复记录，来源 '+formatCount(data.sources||0)+' 个日志源':'已读取 '+formatCount(records.length)+' 条@ 回复记录，来源 '+formatCount(data.sources||0)+' 个日志源'
		if(manual&&recordsToast)recordsToast.textContent=recentView?'已刷新最近24小时记录':'已刷新所有记录'
	}catch(err){
		if(recordsMeta)recordsMeta.textContent='记录读取失败：'+err.message
		if(recordsToast)recordsToast.textContent=err.message
	}}
async function loadFeedRecords(manual=false){
	if(!manual&&activeView!=='feed-records')return
	if(manual&&feedRecordsToast)feedRecordsToast.textContent='正在刷新自动刷帖记录...'
	try{
		const data=await api('/api/feed-records?window=24h')
		const items=Array.isArray(data.records)?data.records:[]
		renderFeedRecords(items)
		if(feedRecordsMeta)feedRecordsMeta.textContent='已读取最近24小时 '+formatCount(items.length)+' 条自动刷帖记录'
		if(manual&&feedRecordsToast)feedRecordsToast.textContent='已刷新自动刷帖记录'
	}catch(err){
		if(feedRecordsMeta)feedRecordsMeta.textContent='自动刷帖记录读取失败：'+err.message
		if(feedRecordsToast)feedRecordsToast.textContent=err.message
	}}
function renderFeedRecords(items){if(!feedRecordsBody)return;const signature=JSON.stringify(items.map(item=>[item.linkId,item.repliedAt,item.replyText,item.status,item.reason].join('|')));if(signature===feedRecordsSignature)return;feedRecordsSignature=signature;feedRecordsBody.innerHTML='';if(!items.length){const row=document.createElement('tr');const cell=document.createElement('td');cell.colSpan=7;cell.textContent='暂无最近24小时自动刷帖记录';row.appendChild(cell);feedRecordsBody.appendChild(row);return}items.forEach(item=>{const row=document.createElement('tr');appendCell(row,formatUnixTime(item.repliedAt||item.createdAt));appendCell(row,item.title||('帖子 '+(item.linkId||'')),'content-cell');appendCell(row,item.author||String(item.authorId||'未知作者'));appendCell(row,item.replyText||'—','content-cell');const statusCell=document.createElement('td');const badge=document.createElement('span');badge.className='badge '+feedStatusClass(item.status);badge.textContent=feedStatusText(item.status);statusCell.appendChild(badge);row.appendChild(statusCell);appendCell(row,item.reason||'—','content-cell');appendPostCell(row,item);feedRecordsBody.appendChild(row)})}
function feedStatusText(status){switch(status){case'sent':return'已发送';case'dry_run':return'试运行';case'skipped':return'已跳过';case'failed':return'失败';default:return status||'未知'}}
function feedStatusClass(status){switch(status){case'sent':return'ok';case'dry_run':return'info';case'skipped':return'warn';case'failed':return'error';default:return'warn'}}
function formatUnixTime(value){const num=Number(value||0);if(!num)return'—';const date=new Date(num*1000);return Number.isNaN(date.getTime())?'—':date.getFullYear()+'-'+String(date.getMonth()+1).padStart(2,'0')+'-'+String(date.getDate()).padStart(2,'0')+' '+String(date.getHours()).padStart(2,'0')+':'+String(date.getMinutes()).padStart(2,'0')+':'+String(date.getSeconds()).padStart(2,'0')}
function renderRecords(items){if(!recordsBody)return;const signature=JSON.stringify(items.map(item=>recordKey(item)+'|'+(item.reply||'')+'|'+(item.status||'')+'|'+(item.tokens||0)+'|'+(item.linkId||0)));if(signature===recordsSignature)return;rememberRecordScrolls();recordsSignature=signature;recordsBody.innerHTML='';if(!items.length){const row=document.createElement('tr');const cell=document.createElement('td');cell.colSpan=7;cell.textContent='暂无最近24小时可识别的用户提问/机器人回复记录';row.appendChild(cell);recordsBody.appendChild(row);return}items.forEach((item,index)=>{const key=recordKey(item,index);const row=document.createElement('tr');appendCell(row,item.time);appendCell(row,item.user||'未知用户');appendCell(row,item.question,'content-cell',key+':question');appendCell(row,item.reply||'—','content-cell',key+':reply');const statusCell=document.createElement('td');const badge=document.createElement('span');badge.className='badge '+(item.status==='已回复'?'ok':isErrorStatus(item.status)?'error':'warn');badge.textContent=item.status;statusCell.appendChild(badge);const copyBtn=document.createElement('button');copyBtn.type='button';copyBtn.className='copy-btn';copyBtn.textContent='复制';copyBtn.addEventListener('click',async()=>{await copyText(recordText(item));copyBtn.textContent='已复制';setTimeout(()=>copyBtn.textContent='复制',900)});statusCell.appendChild(copyBtn);row.appendChild(statusCell);appendPostCell(row,item);const actionCell=document.createElement('td');const stack=document.createElement('div');stack.className='action-stack';const regenerateBtn=document.createElement('button');regenerateBtn.type='button';regenerateBtn.className='copy-btn';regenerateBtn.textContent='重新生成';const feedback=document.createElement('span');feedback.className='action-feedback';regenerateBtn.addEventListener('click',()=>regenerateMessage(item,regenerateBtn,feedback));stack.appendChild(regenerateBtn);stack.appendChild(feedback);actionCell.appendChild(stack);row.appendChild(actionCell);recordsBody.appendChild(row)})}
function rememberRecordScrolls(){recordsBody?.querySelectorAll('.clip-cell[data-scroll-key]').forEach(cell=>recordScrollMemory.set(cell.dataset.scrollKey,cell.scrollTop))}
function recordKey(item,index=0){if(item.msgId||item.commentId)return [item.msgId||'',item.commentId||''].join('|');const fallback=[item.linkId||'',item.time||'',normalizeText(item.user||''),normalizeText(item.question||''),normalizeText(item.reply||'')].join('|');return fallback||String(index)}
function applyRecordLinks(items,links){if(!links)return;const byMsg=links.byMsg||{};const byComment=links.byComment||{};const questionByMsg=links.questionByMsg||{};const questionByComment=links.questionByComment||{};for(const item of items){if(!item.linkId){const linkId=(item.msgId&&byMsg[String(item.msgId)])||(item.commentId&&byComment[String(item.commentId)]);if(linkId)item.linkId=Number(linkId)||0}const question=(item.msgId&&questionByMsg[String(item.msgId)])||(item.commentId&&questionByComment[String(item.commentId)]);if(question&&normalizeText(question).length>normalizeText(item.question||'').length)item.question=question}}
function renderLogLines(content){const selectedLines=selectedLogLineIndexes();logOutput.innerHTML='';logOutput.dataset.raw=content||'';logOutput.classList.toggle('empty',!content);if(!content){logOutput.textContent='暂无日志。';lastSelectedLogLine=-1;return}content.split('\n').forEach((line,index)=>{const item=document.createElement('span');item.className='log-line';item.dataset.index=String(index);item.dataset.raw=line;item.textContent=formatLogLine(line)||' ';item.title='点击选择这一行，Shift 点击选择范围，再点复制选中';item.classList.toggle('selected',selectedLines.has(index));item.addEventListener('click',event=>toggleLogLineSelection(index,event.shiftKey));logOutput.appendChild(item)})}
function rerenderCurrentLog(){clearLogLineSelection();if(!isUsingLogControls())window.getSelection()?.removeAllRanges();renderLog(rawLogContent)}
function filterLogContent(content){if(!content)return'';const mode=logFilter?.value||'all';const keyword=(logKeyword?.value||'').trim().toLowerCase();return content.split('\n').filter(line=>matchesLogFilter(line,mode)&&(!keyword||line.toLowerCase().includes(keyword))).join('\n')}
function matchesLogFilter(line,mode){switch(mode){case'error':return isFailureLine(line);case'ask':return line.includes('[Ai]正在询问Ai');case'reply':return line.includes('[Ai]Ai说：');case'image':return /图片|生图|画图|生成图片|image|upload|imgs/i.test(line);case'feed':return isFeedReplyLine(line);default:return true}}
function formatLogLine(line){const start=line.indexOf('{');if(start<0)return line;const obj=parseZapJSON(line);if(!obj)return line;const prefix=line.slice(0,start).trimEnd();const fields=[];for(const [key,value] of Object.entries(obj)){let text;if(Array.isArray(value)){text=value.map(item=>item&&typeof item==='object'?(item.text||JSON.stringify(item)):String(item||'')).filter(Boolean).join('\n')}else if(value&&typeof value==='object'){text=JSON.stringify(value)}else{text=String(value??'')}if(text)fields.push(key+'：'+cleanText(text))}return prefix+'\n  '+fields.join('\n  ')}
function appendCell(row,text,className,scrollKey){const cell=document.createElement('td');if(className){cell.className=className;const inner=document.createElement('div');inner.className='clip-cell';inner.textContent=text||'—';if(scrollKey){inner.dataset.scrollKey=scrollKey;inner.scrollTop=recordScrollMemory.get(scrollKey)||0;inner.addEventListener('scroll',()=>recordScrollMemory.set(scrollKey,inner.scrollTop),{passive:true})}cell.appendChild(inner)}else{cell.textContent=text||'—'}row.appendChild(cell)}
function appendPostCell(row,item){const cell=document.createElement('td');const href=postHref(item.linkId);if(href){const link=document.createElement('a');link.className='copy-btn';link.href=href;link.target='_blank';link.rel='noopener noreferrer';link.textContent='打开';cell.appendChild(link)}else{cell.textContent='—'}row.appendChild(cell)}
function postHref(linkId){const id=Number(linkId||0);if(!Number.isFinite(id)||id<=0)return'';return 'https://www.xiaoheihe.cn/app/bbs/link/'+id}
function renderTokenRecords(items){const totalEl=document.querySelector('#tokenTotal');const hourEl=document.querySelector('#tokenHour');const dayEl=document.querySelector('#tokenDay');const now=Date.now();let total=0;let hour=0;let day=0;for(const item of items){if(!item.tokens)continue;total+=item.tokens;const time=parseItemTime(item.time);if(!time)continue;const age=now-time.getTime();if(age>=0&&age<=3600000)hour+=item.tokens;if(age>=0&&age<=86400000)day+=item.tokens}setTokenText(totalEl,total);setTokenText(hourEl,hour);setTokenText(dayEl,day)}
function setTokenText(el,value){if(!el)return;el.textContent=formatTokenCount(value);el.title=formatCount(value)+' token'}
function parseInteractions(lines){const items=[];const pending=[];let lastAnswered=null;let currentMessage=null;for(const line of lines){if(isFeedReplyLine(line))continue;if(line.includes('[XHH]正在处理@消息')){currentMessage=parseProcessingLine(line);continue}if(line.includes('[Ai]正在询问Ai')){const context=parseProcessingLine(line)||currentMessage;const next=attachMessageContext(parseQuestionLine(line),context);if(next.question&&!pending.some(item=>sameInteraction(item,next)))pending.push(next);currentMessage=null;continue}if(line.includes('[Ai]Ai说：')){const context=parseProcessingLine(line);let index=findPendingIndex(pending,context);if(index<0&&pending.length)index=0;const item=index>=0?pending.splice(index,1)[0]:attachMessageContext({reply:'',status:'待回复'},context);if(!item.question)continue;item.reply=extractJsonField(line,'text')||stripLogPrefix(line);item.tokens=extractToken(line);item.status='已回复';items.push(item);lastAnswered=item;continue}if(isSendAnomalyLine(line)&&lastAnswered&&lastAnswered.status==='已回复'){attachMessageContext(lastAnswered,parseAnomalyLine(line));lastAnswered.status='异常发送';lastAnswered.lastError=stripLogPrefix(line);continue}if(isFailureLine(line)){const failed=attachMessageContext(parseStandaloneFailureLine(line),currentMessage);const index=findPendingIndex(pending,failed);if(index>=0){const item=pending.splice(index,1)[0];item.lastError=failed.reply;item.status='失败';items.push(finalizePending(item));currentMessage=null;continue}if(failed.question&&failed.reply){items.push(failed);currentMessage=null}}}for(const item of pending){if(item.question)items.push(finalizePending(item))}return items}
function dedupeInteractions(items){const result=[];const positions=new Map();for(const item of items){const key=interactionKey(item);if(!key){result.push(item);continue}const index=positions.get(key);if(index===undefined){positions.set(key,result.length);result.push(item);continue}result[index]=mergeInteraction(result[index],item)}return result}
function interactionKey(item){if(item?.msgId)return'msg:'+item.msgId;if(item?.commentId)return'comment:'+item.commentId;const user=normalizeText(item?.user||'');const question=normalizeText(item?.question||'');return user&&question?'text:'+user+'|'+question:''}
function mergeInteraction(existing,next){const primary=statusRank(next.status)>statusRank(existing.status)?next:existing;const secondary=primary===next?existing:next;return{...secondary,...primary,msgId:primary.msgId||secondary.msgId,commentId:primary.commentId||secondary.commentId,linkId:primary.linkId||secondary.linkId,userId:primary.userId||secondary.userId,user:primary.user||secondary.user,question:primary.question||secondary.question,reply:primary.reply||secondary.reply,tokens:primary.tokens||secondary.tokens,time:primary.time||secondary.time,lastError:primary.lastError||secondary.lastError}}
function statusRank(status){switch(status){case'异常发送':return 4;case'已回复':return 3;case'失败':return 2;case'待重试':return 1;default:return 0}}
function findPendingIndex(items,context){if(!context)return-1;let index=items.findIndex(item=>context.msgId&&item.msgId===context.msgId);if(index>=0)return index;index=items.findIndex(item=>context.commentId&&item.commentId===context.commentId);if(index>=0)return index;return items.findIndex(item=>sameInteraction(item,context))}
function sameInteraction(a,b){if(a?.msgId&&b?.msgId&&a.msgId!==b.msgId)return false;if(a?.commentId&&b?.commentId&&a.commentId!==b.commentId)return false;return normalizeText(a?.user)===normalizeText(b?.user)&&normalizeText(a?.question)===normalizeText(b?.question)}
function finalizePending(item){if(item.status==='待重试'||item.lastError){item.reply=item.lastError||item.reply||'AI 回复失败';item.status='失败'}return item}
function isErrorStatus(status){return status==='失败'||status==='异常发送'}
function isFeedReplyLine(line){if(line.includes('[FeedReply]'))return true;if(/"feed_reply"\s*:\s*(true|1|"true")/.test(line)||/"feedReply"\s*:\s*(true|1|"true")/.test(line))return true;const obj=parseZapJSON(line);const value=obj?.feed_reply??obj?.feedReply;return value===true||value==='true'||value===1}
function attachMessageContext(item,context){if(!context)return item;if(context.msgId)item.msgId=context.msgId;if(context.commentId)item.commentId=context.commentId;if(context.linkId)item.linkId=context.linkId;if(context.userId)item.userId=context.userId;if((!item.user||item.user==='未知用户')&&context.user)item.user=context.user;if(context.question)item.question=context.question;if((!item.time||item.time==='—')&&context.time)item.time=context.time;return item}
function parseProcessingLine(line){const obj=parseZapJSON(line);if(!obj)return null;return{msgId:numberField(obj,'msg_id','msgId','message_id'),commentId:numberField(obj,'comment_id','commentId','reply_id'),linkId:numberField(obj,'link_id','linkId'),userId:numberField(obj,'user_id','userId','userid'),user:cleanText(obj.user_name||obj.user||''),question:fullQuestionText(obj),time:extractTime(line)}}
function parseAnomalyLine(line){const obj=parseZapJSON(line);if(!obj)return null;return{commentId:numberField(obj,'reply_id','comment_id','commentId'),linkId:numberField(obj,'link_id','linkId'),time:extractTime(line)}}
function parseStandaloneFailureLine(line){const obj=parseZapJSON(line)||{};return{commentId:numberField(obj,'reply_id','comment_id','commentId'),linkId:numberField(obj,'link_id','linkId'),user:cleanText(obj.user_name||obj.user||''),question:fullQuestionText(obj),reply:failureText(line),status:'失败',time:extractTime(line)}}
function parseQuestionLine(line){const content=extractContentArray(line);const userQuestion=extractUserQuestion(content);return{time:extractTime(line),user:userQuestion.user,question:userQuestion.question,reply:'',status:'待回复'}}
function fullQuestionText(obj){return cleanText(obj?.raw_text||obj?.rawQuestion||obj?.raw_question||obj?.user_say||obj?.userSay||obj?.comment_text||obj?.text||obj?.question||'')}
function extractUserQuestion(content){const candidates=[];for(const item of content){const text=item&&item.text;if(!text||!/评论.*上下文/.test(text))continue;let current=null;for(const line of text.split('\n')){const parsed=parseContextLine(line);if(parsed){if(current)candidates.push(current);current={user:parsed.user,text:parsed.text};continue}if(current&&line.trim())current.text+='\n'+cleanText(line)}if(current)candidates.push(current)}if(!candidates.length)return{user:'未知用户',question:''};const mentioned=[...candidates].reverse().find(item=>item.text.includes('@'));const picked=mentioned||candidates[candidates.length-1];return{user:picked.user||'未知用户',question:picked.text||''}}
function extractContentArray(line){const obj=parseZapJSON(line);if(obj&&Array.isArray(obj.Content))return obj.Content;return[]}
function numberField(obj,...fields){for(const field of fields){const value=Number(obj?.[field]??0);if(Number.isFinite(value)&&value>0)return value}return 0}
function parseZapJSON(line){const start=line.indexOf('{');if(start<0)return null;try{return JSON.parse(line.slice(start))}catch(err){return null}}
function extractQuestion(content){for(let i=content.length-1;i>=0;i--){const text=content[i]&&content[i].text;if(!text)continue;const index=text.lastIndexOf('以上是帖子内容。');if(index>=0)return cleanText(text.slice(index+'以上是帖子内容。'.length));}return''}
function extractUserFromContent(content,question){let fallback='未知用户';const plainQuestion=normalizeText(question).replace(/^@[^\s]+/,'');for(const item of content){const text=item&&item.text;if(!text)continue;const body=text.split('\n');for(const line of body){const parsed=parseContextLine(line);if(!parsed)continue;fallback=parsed.user;if(plainQuestion&&normalizeText(parsed.text).includes(plainQuestion))return parsed.user}}return fallback}
function parseContextLine(line){const trimmed=cleanText(line);let match=trimmed.match(/^(.+?) 回复 .+?：(.+)$/);if(match)return{user:match[1],text:match[2]};match=trimmed.match(/^(.+?)：(.+)$/);if(match)return{user:match[1],text:match[2]};return null}
function extractJsonField(line,field){const obj=parseZapJSON(line);if(!obj)return'';return cleanText(obj[field]||'')}
function extractToken(line){const obj=parseZapJSON(line);if(!obj)return 0;const value=obj['本次消耗token']??obj.total_tokens??obj.totalToken??obj.tokens;const token=Number(value);return Number.isFinite(token)&&token>0?token:0}
function failureText(line){const obj=parseZapJSON(line);return cleanText(obj?.error||obj?.message||obj?.msg||'')||stripLogPrefix(line)}
function isSendAnomalyLine(line){return /异常发送|因为无法评论|评论发送失败|comment\/create image reply failed/i.test(line)&&!line.includes('[Ai]正在询问Ai')}
function isFailureLine(line){return /Ai返回错误|无法回复评论|评论发送失败|图片评论处理失败|无法整理@消息|comment\/create image reply failed|error|failed|panic|fatal|错误|失败|异常/i.test(line)&&!line.includes('[Ai]正在询问Ai')}
function extractTime(line){const match=line.match(/(20\d{2}-\d{2}-\d{2})(?:[ T](\d{2}:\d{2}:\d{2}))?/);return match?match[1]+(match[2]?' '+match[2]:''):'—'}
function stripLogPrefix(line){return cleanText(line.replace(/^.*?\]\s*/,''))}
function recordText(item){const lines=['时间：'+(item.time||'—'),'用户：'+(item.user||'未知用户'),'用户说：'+(item.question||'—'),'机器人回复：'+(item.reply||'—'),'状态：'+(item.status||'—')];if(item.msgId)lines.push('消息ID：'+item.msgId);if(item.commentId)lines.push('评论ID：'+item.commentId);if(item.linkId)lines.push('帖子ID：'+item.linkId);return lines.join('\n')}
function selectedLogLineIndexes(){return new Set([...logOutput.querySelectorAll('.log-line.selected')].map(line=>Number(line.dataset.index)))}
function selectedLogLineText(){const selected=[...logOutput.querySelectorAll('.log-line.selected')];if(!selected.length)return'';selected.sort((a,b)=>Number(a.dataset.index)-Number(b.dataset.index));return selected.map(line=>line.dataset.raw||line.textContent).join('\n')}
function toggleLogLineSelection(index,range){const lines=[...logOutput.querySelectorAll('.log-line')];if(range&&lastSelectedLogLine>=0){const start=Math.min(lastSelectedLogLine,index);const end=Math.max(lastSelectedLogLine,index);for(let i=start;i<=end;i++)lines[i]?.classList.add('selected')}else{lines[index]?.classList.toggle('selected');lastSelectedLogLine=index}if(selectedLogLineIndexes().size>0)logPaused=true;const button=document.querySelector('#toggleLogRefreshBtn');if(button)button.textContent=logPaused?'继续刷新':'暂停刷新'}
function clearLogLineSelection(){logOutput.querySelectorAll('.log-line.selected').forEach(line=>line.classList.remove('selected'));lastSelectedLogLine=-1}
function selectedLogText(){const selection=window.getSelection();if(!selection||selection.isCollapsed)return'';const anchor=selection.anchorNode;const focus=selection.focusNode;const inAnchor=anchor&&logOutput.contains(anchor);const inFocus=focus&&logOutput.contains(focus);if(!inAnchor&&!inFocus)return'';return selection.toString()}
function hasLogSelection(){return selectedLogLineIndexes().size>0||selectedLogText().trim()!==''}
function isUsingLogControls(){return document.activeElement===logKeyword||document.activeElement===logFilter||document.activeElement===logSelect}
function copySelectedLog(){copyText(selectedLogLineText()||selectedLogText()||logOutput.dataset.raw||logOutput.textContent||'')}
async function copyText(text){if(!text)return;try{await navigator.clipboard.writeText(text)}catch(err){const area=document.createElement('textarea');area.value=text;area.style.position='fixed';area.style.opacity='0';document.body.appendChild(area);area.select();document.execCommand('copy');area.remove()}}
function cleanText(text){return String(text||'').replace(/<[^>]+>/g,'').replace(/&nbsp;/g,' ').trim()}
function normalizeText(text){return cleanText(text).replace(/\s+/g,'')}
function renderTrend(items){const buckets=[];const now=new Date();for(let i=6;i>=0;i--){const date=new Date(now);date.setDate(now.getDate()-i);const key=date.toISOString().slice(0,10);buckets.push({key,label:(date.getMonth()+1).toString().padStart(2,'0')+'-'+date.getDate().toString().padStart(2,'0'),count:0})}for(const item of items){const match=(item.time||'').match(/20\d{2}-\d{2}-\d{2}/);const key=match?match[0]:buckets[buckets.length-1].key;const bucket=buckets.find(value=>value.key===key);if(bucket)bucket.count++}const max=Math.max(1,...buckets.map(item=>item.count));chart.innerHTML='';for(const item of buckets){const wrap=document.createElement('div');wrap.className='bar-wrap';const num=document.createElement('div');num.className='bar-num';num.textContent=item.count||'';const bar=document.createElement('div');bar.className='bar';bar.style.height=Math.max(8,Math.round(item.count/max*140))+'px';const label=document.createElement('div');label.textContent=item.label;wrap.appendChild(num);wrap.appendChild(bar);wrap.appendChild(label);chart.appendChild(wrap)}}
function extractPort(addr){if(!addr)return'';const parts=addr.split(':');return parts[parts.length-1]||''}
function formatBytes(size){if(size<1024)return size+' B';if(size<1024*1024)return(size/1024).toFixed(1)+' KB';return(size/1024/1024).toFixed(1)+' MB'}
function parseItemTime(value){if(!value||value==='—')return null;const date=new Date(String(value).replace(' ','T'));return Number.isNaN(date.getTime())?null:date}
function formatCount(value){return Number(value||0).toLocaleString('zh-CN')}
function formatTokenCount(value){const num=Number(value||0);if(num>=1e9)return trimUnit(num/1e9)+'b';if(num>=1e6)return trimUnit(num/1e6)+'m';if(num>=1e3)return trimUnit(num/1e3)+'k';return formatCount(num)}
function trimUnit(value){return value>=100?value.toFixed(0):value>=10?value.toFixed(1).replace(/\.0$/,''):value.toFixed(2).replace(/\.00$/,'').replace(/0$/,'')}
showApp(authed);
</script>
</body>
</html>`
