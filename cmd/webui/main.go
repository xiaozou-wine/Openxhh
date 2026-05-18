package main

import (
	"bufio"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	defaultAddr       = "127.0.0.1:29173"
	windowsRunKey     = `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`
	windowsRunValue   = "OpenxhhWebUI"
	maxConfigBodySize = 1 << 20
)

var indexTemplate = template.Must(template.New("index").Parse(indexHTML))

type authStore struct {
	Salt string `json:"salt"`
	Hash string `json:"hash"`
}

type appConfig struct {
	Xhh struct {
		CheckTime       int    `json:"checkTime"`
		ReplyTime       int    `json:"replyTime"`
		MaxReplyThreads int    `json:"maxReplyThreads"`
		EnableWhitelist bool   `json:"enableWhitelist"`
		Owner           string `json:"owner"`
		DeviceID        string `json:"deviceID"`
		BaseURL         string `json:"baseUrl"`
		WebVer          string `json:"webver"`
		Ver             string `json:"version"`
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
		Model   string `json:"model"`
		Prompt  string `json:"prompt"`
		BaseURL string `json:"baseUrl"`
		Token   string `json:"token"`
	} `json:"ai"`
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

type serverState struct {
	rootDir           string
	robotBin          string
	authPath          string
	listenAddr        string
	bootstrapPassword string
	sessions          map[string]time.Time
	sessionsMu        sync.Mutex
	processMu         sync.Mutex
	process           *exec.Cmd
	processDone       chan error
	processMode       string
	startedAt         time.Time
	stdoutPath        string
}

type logFile struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	ModTime string `json:"modTime"`
}

type webUIOptions struct {
	addr     string
	root     string
	robotBin string
}

type runningServer struct {
	state    *serverState
	server   *http.Server
	listener net.Listener
	url      string
}

func main() {
	addr := flag.String("addr", defaultAddr, "web ui listen address")
	root := flag.String("root", defaultRoot(), "Openxhh working directory")
	bin := flag.String("bin", "", "Openxhh executable path")
	browserFlag := flag.Bool("browser", false, "open the Web UI in the system browser")
	openBrowserFlag := flag.Bool("open-browser", false, "deprecated alias for -browser")
	serverOnlyFlag := flag.Bool("server-only", false, "only start the local Web UI server")
	desktopFlag := flag.Bool("desktop", runtime.GOOS == "windows", "open the Web UI in an Openxhh desktop window")
	flag.Parse()

	running, err := startWebUIServer(webUIOptions{addr: *addr, root: *root, robotBin: *bin})
	if err != nil {
		log.Fatal(err)
	}
	defer running.close()

	if *browserFlag || *openBrowserFlag {
		go running.logServe()
		if err := openBrowser(running.url); err != nil {
			log.Printf("打开浏览器失败: %v", err)
		}
		select {}
	}

	if *serverOnlyFlag || !*desktopFlag {
		log.Fatal(running.serve())
	}

	go running.logServe()
	if err := runDesktop(running.url); err != nil {
		log.Printf("桌面窗口启动失败: %v", err)
		if err := openBrowser(running.url); err != nil {
			log.Printf("打开浏览器失败: %v", err)
		}
		select {}
	}
}

func startWebUIServer(options webUIOptions) (*runningServer, error) {
	rootDir, err := filepath.Abs(options.root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(rootDir, 0755); err != nil {
		return nil, err
	}

	robotBin := strings.TrimSpace(options.robotBin)
	if robotBin != "" {
		robotBin, err = filepath.Abs(robotBin)
		if err != nil {
			return nil, err
		}
	}

	state := &serverState{
		rootDir:  rootDir,
		robotBin: robotBin,
		authPath: filepath.Join(rootDir, "webui_auth.json"),
		sessions: map[string]time.Time{},
	}

	password, created, err := ensureAuth(state.authPath)
	if err != nil {
		return nil, err
	}
	if created {
		state.bootstrapPassword = password
		fmt.Println("Openxhh Web UI 已生成随机强密码")
		fmt.Println("登录密码:", password)
		fmt.Println("请保存该密码；如需重置，停止 Web UI 后删除 webui_auth.json 再启动。")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", state.handleIndex)
	mux.HandleFunc("/login", state.handleLogin)
	mux.HandleFunc("/logout", state.requireAuth(state.handleLogout))
	mux.HandleFunc("/api/status", state.requireAuth(state.handleStatus))
	mux.HandleFunc("/api/config", state.requireAuth(state.handleConfig))
	mux.HandleFunc("/api/start", state.requireAuth(state.handleStart))
	mux.HandleFunc("/api/robot-login", state.requireAuth(state.handleRobotLogin))
	mux.HandleFunc("/api/qrcode", state.requireAuth(state.handleQRCode))
	mux.HandleFunc("/api/stop", state.requireAuth(state.handleStop))
	mux.HandleFunc("/api/autostart", state.requireAuth(state.handleAutoStart))
	mux.HandleFunc("/api/logs", state.requireAuth(state.handleLogs))
	mux.HandleFunc("/api/logs/read", state.requireAuth(state.handleReadLog))

	listener, err := net.Listen("tcp", options.addr)
	if err != nil {
		return nil, fmt.Errorf("监听 %s 失败: %w", options.addr, err)
	}

	state.listenAddr = listener.Addr().String()
	url := "http://" + state.listenAddr
	fmt.Printf("Openxhh Web UI: %s\n", url)
	fmt.Printf("工作目录: %s\n", rootDir)
	if robotBin != "" {
		fmt.Printf("主程序: %s\n", robotBin)
	}

	return &runningServer{
		state:    state,
		server:   &http.Server{Handler: withSecurityHeaders(mux)},
		listener: listener,
		url:      url,
	}, nil
}

func (s *runningServer) serve() error {
	if err := s.server.Serve(s.listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func (s *runningServer) logServe() {
	if err := s.serve(); err != nil {
		log.Printf("Web UI 服务退出: %v", err)
	}
}

func (s *runningServer) close() {
	_ = s.server.Close()
}

func defaultRoot() string {
	if runtime.GOOS == "windows" {
		if programData := os.Getenv("ProgramData"); programData != "" {
			return filepath.Join(programData, "Openxhh")
		}
	}
	return "."
}

func ensureAuth(path string) (string, bool, error) {
	if _, err := os.Stat(path); err == nil {
		return "", false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
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
	store := authStore{
		Salt: salt,
		Hash: hashPassword(password, salt),
	}
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

func (s *serverState) createSession() (string, error) {
	token, err := randomPassword(48)
	if err != nil {
		return "", err
	}
	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()
	s.sessions[token] = time.Now().Add(24 * time.Hour)
	return token, nil
}

func (s *serverState) validSession(r *http.Request) bool {
	cookie, err := r.Cookie("xhh_webui_session")
	if err != nil || cookie.Value == "" {
		return false
	}
	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()
	expiresAt, ok := s.sessions[cookie.Value]
	if !ok || time.Now().After(expiresAt) {
		delete(s.sessions, cookie.Value)
		return false
	}
	return true
}

func (s *serverState) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = indexTemplate.Execute(w, map[string]any{"Authed": s.validSession(r), "BootstrapPassword": s.bootstrapPassword})
}

func (s *serverState) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
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
		writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "密码错误"})
		return
	}
	token, err := s.createSession()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "无法创建会话"})
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "xhh_webui_session",
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Expires:  time.Now().Add(24 * time.Hour),
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *serverState) handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie("xhh_webui_session"); err == nil {
		s.sessionsMu.Lock()
		delete(s.sessions, cookie.Value)
		s.sessionsMu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: "xhh_webui_session", Path: "/", MaxAge: -1})
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
	running, mode, startedAt, stdoutPath := s.processStatus()
	autoStartEnabled, autoStartSupported, autoStartError := s.autoStartStatus()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                 true,
		"running":            running,
		"processMode":        mode,
		"startedAt":          formatTime(startedAt),
		"rootDir":            s.rootDir,
		"robotBin":           s.detectRobotBin(),
		"listenPort":         s.listenAddr,
		"stdoutLog":          baseName(stdoutPath),
		"configExists":       fileExists(s.configPath()),
		"cookieExists":       fileExists(filepath.Join(s.rootDir, "cookie.json")),
		"qrCodeExists":       fileExists(s.qrCodePath()),
		"autoStartEnabled":   autoStartEnabled,
		"autoStartSupported": autoStartSupported,
		"autoStartError":     autoStartError,
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

func (s *serverState) qrCodePath() string {
	return filepath.Join(s.rootDir, "qrcode.png")
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

func (s *serverState) handleStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.startManagedProcess(w, "start")
}

func (s *serverState) handleRobotLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := os.Remove(s.qrCodePath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.startManagedProcess(w, "login")
}

func (s *serverState) handleQRCode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !fileExists(s.qrCodePath()) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "image/png")
	http.ServeFile(w, r, s.qrCodePath())
}

func (s *serverState) startManagedProcess(w http.ResponseWriter, mode string) {
	s.processMu.Lock()
	defer s.processMu.Unlock()
	if s.process != nil && s.process.Process != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": "已有 Openxhh 进程由 Web UI 启动"})
		return
	}
	if err := os.MkdirAll(filepath.Join(s.rootDir, "log"), 0755); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	stdoutPath := filepath.Join(s.rootDir, "log", "webui-"+mode+"-"+time.Now().Format("2006-01-02_15_04_05")+".log")
	stdoutFile, err := os.Create(stdoutPath)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	cmd := buildRobotCommand(s.rootDir, s.robotBin, mode)
	cmd.Stdout = io.MultiWriter(stdoutFile)
	cmd.Stderr = io.MultiWriter(stdoutFile)
	if err := cmd.Start(); err != nil {
		_ = stdoutFile.Close()
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	s.process = cmd
	s.processDone = make(chan error, 1)
	s.processMode = mode
	s.startedAt = time.Now()
	s.stdoutPath = stdoutPath
	go func() {
		err := cmd.Wait()
		_ = stdoutFile.Close()
		s.processDone <- err
		s.processMu.Lock()
		if s.process == cmd {
			s.process = nil
			s.processMode = ""
			s.startedAt = time.Time{}
		}
		s.processMu.Unlock()
	}()

	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "log": baseName(stdoutPath)})
}

func buildRobotCommand(rootDir, robotBin, mode string) *exec.Cmd {
	if mode == "start" {
		if custom := strings.TrimSpace(os.Getenv("OPENXHH_COMMAND")); custom != "" {
			return shellCommand(custom, rootDir)
		}
	}
	if robotBin != "" {
		cmd := exec.Command(robotBin, "-mode", mode)
		cmd.Dir = rootDir
		return cmd
	}
	for _, candidate := range robotCandidates(rootDir) {
		if fileExists(candidate) {
			cmd := exec.Command(candidate, "-mode", mode)
			cmd.Dir = rootDir
			return cmd
		}
	}
	cmd := exec.Command("go", "run", ".", "-mode", mode)
	cmd.Dir = rootDir
	return cmd
}

func (s *serverState) detectRobotBin() string {
	if s.robotBin != "" {
		return s.robotBin
	}
	for _, candidate := range robotCandidates(s.rootDir) {
		if fileExists(candidate) {
			return candidate
		}
	}
	return ""
}

func robotCandidates(rootDir string) []string {
	exeDir := executableDir()
	if runtime.GOOS == "windows" {
		return []string{
			filepath.Join(rootDir, "Openxhh.exe"),
			filepath.Join(exeDir, "Openxhh.exe"),
		}
	}
	return []string{
		filepath.Join(rootDir, "Openxhh"),
		filepath.Join(exeDir, "Openxhh"),
	}
}

func executableDir() string {
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	return filepath.Dir(exe)
}

func shellCommand(command, dir string) *exec.Cmd {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd", "/C", command)
	} else {
		cmd = exec.Command("sh", "-c", command)
	}
	cmd.Dir = dir
	return cmd
}

func (s *serverState) handleStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.processMu.Lock()
	cmd := s.process
	done := s.processDone
	s.processMu.Unlock()
	if cmd == nil || cmd.Process == nil || done == nil {
		writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": "当前没有由 Web UI 启动的进程"})
		return
	}
	if err := stopProcess(cmd); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		if err := cmd.Process.Kill(); err != nil && !strings.Contains(err.Error(), "process already finished") {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func stopProcess(cmd *exec.Cmd) error {
	if runtime.GOOS == "windows" {
		return exec.Command("taskkill", "/T", "/F", "/PID", fmt.Sprint(cmd.Process.Pid)).Run()
	}
	return cmd.Process.Signal(os.Interrupt)
}

func (s *serverState) processStatus() (bool, string, time.Time, string) {
	s.processMu.Lock()
	defer s.processMu.Unlock()
	return s.process != nil && s.process.Process != nil, s.processMode, s.startedAt, s.stdoutPath
}

func (s *serverState) handleAutoStart(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		enabled, supported, errText := s.autoStartStatus()
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "enabled": enabled, "supported": supported, "error": errText})
	case http.MethodPost:
		if runtime.GOOS != "windows" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "开机自启当前只支持 Windows"})
			return
		}
		var payload struct {
			Enabled bool `json:"enabled"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&payload); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "请求格式错误"})
			return
		}
		if err := s.setAutoStart(payload.Enabled); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		enabled, supported, errText := s.autoStartStatus()
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "enabled": enabled, "supported": supported, "error": errText})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *serverState) autoStartStatus() (bool, bool, string) {
	if runtime.GOOS != "windows" {
		return false, false, ""
	}
	out, err := exec.Command("reg", "query", windowsRunKey, "/v", windowsRunValue).CombinedOutput()
	if err != nil {
		return false, true, ""
	}
	return strings.Contains(string(out), windowsRunValue), true, ""
}

func (s *serverState) setAutoStart(enabled bool) error {
	if enabled {
		return exec.Command("reg", "add", windowsRunKey, "/v", windowsRunValue, "/t", "REG_SZ", "/d", s.autoStartCommand(), "/f").Run()
	}
	current, _, _ := s.autoStartStatus()
	if !current {
		return nil
	}
	return exec.Command("reg", "delete", windowsRunKey, "/v", windowsRunValue, "/f").Run()
}

func (s *serverState) autoStartCommand() string {
	exe, err := os.Executable()
	if err != nil {
		exe = os.Args[0]
	}
	parts := []string{exe, "-addr", s.listenAddr, "-root", s.rootDir, "-desktop"}
	if s.robotBin != "" {
		parts = append(parts, "-bin", s.robotBin)
	}
	quoted := make([]string, 0, len(parts))
	for _, part := range parts {
		quoted = append(quoted, quoteCommandArg(part))
	}
	return strings.Join(quoted, " ")
}

func quoteCommandArg(arg string) string {
	if arg == "" {
		return `""`
	}
	if !strings.ContainsAny(arg, " \t\"") {
		return arg
	}
	var b strings.Builder
	b.WriteByte('"')
	backslashes := 0
	for _, r := range arg {
		if r == '\\' {
			backslashes++
			continue
		}
		if r == '"' {
			b.WriteString(strings.Repeat("\\", backslashes*2+1))
			b.WriteRune(r)
			backslashes = 0
			continue
		}
		if backslashes > 0 {
			b.WriteString(strings.Repeat("\\", backslashes))
			backslashes = 0
		}
		b.WriteRune(r)
	}
	if backslashes > 0 {
		b.WriteString(strings.Repeat("\\", backslashes*2))
	}
	b.WriteByte('"')
	return b.String()
}

func openBrowser(url string) error {
	switch runtime.GOOS {
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		return exec.Command("open", url).Start()
	default:
		return exec.Command("xdg-open", url).Start()
	}
}

func (s *serverState) handleLogs(w http.ResponseWriter, r *http.Request) {
	files, err := listLogFiles(filepath.Join(s.rootDir, "log"))
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "files": []logFile{}})
		return
	}
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
	if name == "" || strings.Contains(name, "/") || strings.Contains(name, "\\") || strings.Contains(name, "..") {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "日志文件名无效"})
		return
	}
	path := filepath.Join(s.rootDir, "log", name)
	content, err := tailFile(path, 800)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "content": content})
}

func tailFile(path string, maxLines int) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	var lines []string
	scanner := bufio.NewScanner(file)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)
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
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; connect-src 'self'; img-src 'self' data:")
		next.ServeHTTP(w, r)
	})
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("2006-01-02 15:04:05")
}

func baseName(path string) string {
	if path == "" {
		return ""
	}
	return filepath.Base(path)
}

const indexHTML = `<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Openxhh 控制台</title>
  <style>
    :root {
      color-scheme: dark;
      --bg: #080b10;
      --panel: rgba(17, 23, 33, .84);
      --panel-strong: rgba(23, 31, 44, .96);
      --line: rgba(141, 170, 202, .18);
      --text: #e8eef8;
      --muted: #7f8da3;
      --cyan: #52e0ff;
      --green: #7dffb2;
      --amber: #ffcf67;
      --red: #ff6b7a;
      --shadow: 0 24px 80px rgba(0, 0, 0, .42);
    }
    * { box-sizing: border-box; }
    body {
      margin: 0;
      min-height: 100vh;
      font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, "Liberation Mono", monospace;
      color: var(--text);
      background:
        radial-gradient(circle at 12% 18%, rgba(82, 224, 255, .16), transparent 34rem),
        radial-gradient(circle at 78% 6%, rgba(125, 255, 178, .10), transparent 30rem),
        linear-gradient(135deg, #05070b 0%, #0b111b 48%, #071018 100%);
      overflow-x: hidden;
    }
    body::before {
      content: "";
      position: fixed;
      inset: 0;
      pointer-events: none;
      opacity: .24;
      background-image: linear-gradient(rgba(255,255,255,.045) 1px, transparent 1px), linear-gradient(90deg, rgba(255,255,255,.035) 1px, transparent 1px);
      background-size: 44px 44px;
      mask-image: linear-gradient(to bottom, black, transparent 82%);
    }
    .shell { width: min(1220px, calc(100vw - 40px)); margin: 0 auto; padding: 34px 0 40px; position: relative; }
    .topbar { display: flex; align-items: center; justify-content: space-between; gap: 18px; margin-bottom: 24px; }
    .brand { display: flex; align-items: center; gap: 14px; }
    .mark { width: 42px; height: 42px; border: 1px solid rgba(82,224,255,.45); background: linear-gradient(145deg, rgba(82,224,255,.2), rgba(125,255,178,.08)); box-shadow: 0 0 28px rgba(82,224,255,.2); transform: rotate(45deg); }
    .brand h1 { font-size: clamp(24px, 3.4vw, 44px); letter-spacing: -.06em; line-height: .95; margin: 0; }
    .brand p { margin: 6px 0 0; color: var(--muted); font-size: 13px; }
    .status-pill { display: inline-flex; align-items: center; gap: 8px; padding: 10px 13px; border: 1px solid var(--line); border-radius: 999px; background: rgba(10,14,21,.62); color: var(--muted); }
    .dot { width: 8px; height: 8px; border-radius: 50%; background: var(--red); box-shadow: 0 0 14px currentColor; color: var(--red); }
    .dot.on { background: var(--green); color: var(--green); }
    .login { min-height: 72vh; display: grid; place-items: center; }
    .login-card { width: min(520px, 100%); padding: 30px; border: 1px solid var(--line); border-radius: 26px; background: linear-gradient(180deg, rgba(23,31,44,.92), rgba(10,14,21,.9)); box-shadow: var(--shadow); }
    .login-card h2 { margin: 0 0 8px; font-size: 28px; letter-spacing: -.04em; }
    .login-card p { margin: 0 0 22px; color: var(--muted); line-height: 1.65; }
    .first-password { padding: 14px; border: 1px solid rgba(255,207,103,.35); background: rgba(255,207,103,.08); border-radius: 16px; margin-bottom: 16px; color: var(--amber); word-break: break-all; }
    input, select, button, textarea { font: inherit; }
    .input, textarea, select { width: 100%; border: 1px solid var(--line); background: rgba(2,5,9,.62); color: var(--text); border-radius: 16px; padding: 12px 13px; outline: none; }
    textarea { min-height: 110px; resize: vertical; line-height: 1.55; }
    .input:focus, textarea:focus, select:focus { border-color: rgba(82,224,255,.65); box-shadow: 0 0 0 4px rgba(82,224,255,.09); }
    .button-row { display: flex; gap: 10px; flex-wrap: wrap; }
    button { border: 0; cursor: pointer; color: #041016; background: var(--cyan); padding: 12px 16px; border-radius: 14px; font-weight: 800; transition: transform .18s ease, filter .18s ease, background .18s ease; }
    button:hover { transform: translateY(-1px); filter: brightness(1.08); }
    button.secondary { color: var(--text); background: rgba(255,255,255,.08); border: 1px solid var(--line); }
    button.danger { color: #220206; background: var(--red); }
    button:disabled { opacity: .45; cursor: not-allowed; transform: none; }
    .app { display: grid; gap: 18px; }
    .grid { display: grid; grid-template-columns: 360px 1fr; gap: 18px; align-items: start; }
    .panel { border: 1px solid var(--line); border-radius: 26px; background: var(--panel); box-shadow: var(--shadow); backdrop-filter: blur(18px); }
    .panel-pad { padding: 22px; }
    .stack { display: grid; gap: 18px; }
    .section-title { margin: 0 0 16px; color: var(--muted); font-size: 12px; letter-spacing: .18em; text-transform: uppercase; }
    .section-head { display: flex; justify-content: space-between; align-items: flex-start; gap: 14px; margin-bottom: 18px; }
    .section-head h2 { margin: 0; font-size: 22px; letter-spacing: -.04em; }
    .section-head p { margin: 7px 0 0; color: var(--muted); line-height: 1.6; }
    .metric { display: grid; gap: 6px; padding: 14px 0; border-bottom: 1px solid var(--line); }
    .metric:last-child { border-bottom: 0; }
    .metric span { color: var(--muted); font-size: 12px; }
    .metric strong { font-size: 15px; word-break: break-all; }
    .actions { display: grid; grid-template-columns: 1fr 1fr; gap: 10px; margin-top: 18px; }
    .form-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 13px; }
    .field { display: grid; gap: 7px; }
    .field.wide { grid-column: 1 / -1; }
    .field label { color: var(--muted); font-size: 12px; }
    .form-group { grid-column: 1 / -1; display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 13px; padding-top: 12px; border-top: 1px solid var(--line); }
    .form-group h3 { grid-column: 1 / -1; margin: 0; font-size: 15px; color: var(--cyan); }
    .switch { display: flex; align-items: center; justify-content: space-between; gap: 12px; border: 1px solid var(--line); border-radius: 16px; padding: 13px; background: rgba(2,5,9,.3); }
    .switch input { width: 22px; height: 22px; }
    .qr-card { display: grid; gap: 10px; margin-top: 14px; padding: 14px; border: 1px solid rgba(82,224,255,.3); border-radius: 18px; background: rgba(82,224,255,.07); }
    .qr-card img { width: 220px; height: 220px; padding: 10px; border-radius: 16px; background: #fff; justify-self: center; }
    .qr-card small { color: var(--muted); line-height: 1.55; }
    .log-head { display: flex; align-items: center; justify-content: space-between; gap: 14px; padding: 18px; border-bottom: 1px solid var(--line); }
    .terminal { height: min(56vh, 620px); overflow: auto; padding: 20px; background: rgba(0,0,0,.42); border-radius: 0 0 26px 26px; }
    pre { margin: 0; white-space: pre-wrap; word-break: break-word; line-height: 1.55; color: #c7d6e8; font-size: 13px; }
    .empty { color: var(--muted); display: grid; place-items: center; min-height: 260px; text-align: center; line-height: 1.7; }
    .toast { min-height: 22px; margin-top: 14px; color: var(--amber); font-size: 13px; }
    .hidden { display: none !important; }
    @media (max-width: 980px) {
      .grid, .form-grid, .form-group { grid-template-columns: 1fr; }
      .topbar, .section-head, .log-head { align-items: flex-start; flex-direction: column; }
    }
  </style>
</head>
<body data-authed="{{.Authed}}">
  <main class="shell">
    <div class="topbar">
      <div class="brand">
        <div class="mark"></div>
        <div>
          <h1>Openxhh</h1>
          <p>Windows 本地控制台 · 默认监听 127.0.0.1:29173</p>
        </div>
      </div>
      <div id="topStatus" class="status-pill"><span class="dot"></span><span>未连接</span></div>
    </div>

    <section id="loginView" class="login hidden">
      <form id="loginForm" class="login-card">
        <h2>进入控制台</h2>
        <p>默认只绑定本机地址。首次启动会在下方显示随机密码，请保存到安全位置。</p>
        {{if .BootstrapPassword}}<div class="first-password">首次登录密码：<strong>{{.BootstrapPassword}}</strong></div>{{end}}
        <input id="password" class="input" type="password" placeholder="控制台密码" autocomplete="current-password" autofocus>
        <div class="button-row" style="margin-top:14px"><button type="submit">登录</button></div>
        <div id="loginToast" class="toast"></div>
      </form>
    </section>

    <section id="appView" class="app hidden">
      <div class="grid">
        <aside class="panel panel-pad">
          <p class="section-title">Runtime</p>
          <div class="metric"><span>运行状态</span><strong id="robotState">读取中</strong></div>
          <div class="metric"><span>启动时间</span><strong id="startedAt">—</strong></div>
          <div class="metric"><span>工作目录</span><strong id="rootDir">—</strong></div>
          <div class="metric"><span>主程序</span><strong id="robotBin">—</strong></div>
          <div class="metric"><span>配置文件</span><strong id="configState">—</strong></div>
          <div class="metric"><span>登录 Cookie</span><strong id="cookieState">—</strong></div>
          <div class="metric"><span>捕获日志</span><strong id="stdoutLog">—</strong></div>
          <div class="actions">
            <button id="startBtn">启动</button>
            <button id="stopBtn" class="danger">停止</button>
          </div>
          <div class="button-row" style="margin-top:10px">
            <button id="robotLoginBtn" class="secondary" type="button">扫码登录</button>
            <button id="refreshBtn" class="secondary" type="button">刷新日志</button>
            <button id="logoutBtn" class="secondary" type="button">退出</button>
          </div>
          <div id="qrCard" class="qr-card hidden">
            <strong>小黑盒扫码登录</strong>
            <img id="qrCode" alt="小黑盒登录二维码">
            <small>请使用小黑盒 App 扫描二维码。扫码成功后会自动保存 cookie.json。</small>
          </div>
          <label class="switch" style="margin-top:14px">
            <span><strong>开机自启控制台</strong><br><small id="autoStartText">读取中</small></span>
            <input id="autoStartToggle" type="checkbox">
          </label>
          <div id="appToast" class="toast"></div>
        </aside>

        <section class="panel panel-pad">
          <div class="section-head">
            <div>
              <p class="section-title">Setup Wizard</p>
              <h2>配置向导</h2>
              <p>保存后会写入工作目录下的 <span id="configPath">config.json</span>，再扫码登录并启动机器人。</p>
            </div>
            <button id="saveConfigBtn" type="submit" form="configForm">保存配置</button>
          </div>
          <form id="configForm" class="form-grid">
            <div class="form-group">
              <h3>小黑盒</h3>
              <div class="field"><label>检查间隔/秒</label><input class="input" data-path="xhh.checkTime" data-type="number"></div>
              <div class="field"><label>回复间隔/秒</label><input class="input" data-path="xhh.replyTime" data-type="number"></div>
              <div class="field"><label>最高回复线程</label><input class="input" data-path="xhh.maxReplyThreads" data-type="number"></div>
              <label class="switch field wide"><span>启用白名单（关闭时回复所有 @，仍识别 owner）</span><input data-path="xhh.enableWhitelist" data-type="bool" type="checkbox"></label>
              <div class="field wide"><label>Owner / 白名单 UID（英文逗号分隔）</label><input class="input" data-path="xhh.owner"></div>
              <div class="field"><label>Device ID</label><input class="input" data-path="xhh.deviceID"></div>
              <div class="field wide"><label>API Base URL</label><input class="input" data-path="xhh.baseUrl"></div>
              <div class="field"><label>Web Version</label><input class="input" data-path="xhh.webver"></div>
              <div class="field"><label>Version</label><input class="input" data-path="xhh.version"></div>
            </div>
            <div class="form-group">
              <h3>数据库</h3>
              <div class="field"><label>类型</label><select data-path="database.type"><option value="sqlite">sqlite</option><option value="pg">pg</option></select></div>
              <div class="field"><label>数据库名</label><input class="input" data-path="database.db"></div>
              <div class="field"><label>Host</label><input class="input" data-path="database.host"></div>
              <div class="field"><label>Port</label><input class="input" data-path="database.port"></div>
              <div class="field"><label>User</label><input class="input" data-path="database.user"></div>
              <div class="field"><label>Password</label><input class="input" data-path="database.passwd" type="password"></div>
            </div>
            <div class="form-group">
              <h3>AI 回复</h3>
              <div class="field"><label>模型</label><input class="input" data-path="ai.model"></div>
              <div class="field"><label>Token</label><input class="input" data-path="ai.token" type="password"></div>
              <div class="field wide"><label>Chat Completions URL</label><input class="input" data-path="ai.baseUrl"></div>
              <div class="field wide"><label>回复策略 Prompt</label><textarea data-path="ai.prompt"></textarea></div>
            </div>
            <div class="form-group">
              <h3>图片能力</h3>
              <div class="field"><label>模型</label><input class="input" data-path="image.model"></div>
              <div class="field"><label>Token</label><input class="input" data-path="image.token" type="password"></div>
              <div class="field wide"><label>Images Generations URL</label><input class="input" data-path="image.baseUrl"></div>
              <div class="field"><label>尺寸</label><input class="input" data-path="image.size"></div>
              <div class="field"><label>Response Format</label><input class="input" data-path="image.responseFormat"></div>
              <div class="field"><label>输出目录</label><input class="input" data-path="image.outputDir"></div>
              <div class="field"><label>上传模式</label><select data-path="image.uploadMode"><option value="external">external</option><option value="cos">cos</option><option value="">禁用/空</option></select></div>
              <div class="field"><label>外部图片目录</label><input class="input" data-path="image.externalDir"></div>
              <div class="field wide"><label>外部图片访问 URL</label><input class="input" data-path="image.externalBaseUrl"></div>
              <label class="switch field wide"><span>启用图片 Prompt 优化</span><input data-path="image.promptRefine" data-type="bool" type="checkbox"></label>
              <div class="field"><label>Prompt 优化模型</label><input class="input" data-path="image.promptModel"></div>
              <div class="field"><label>Prompt 最大字符数</label><input class="input" data-path="image.promptMaxChars" data-type="number"></div>
              <div class="field wide"><label>Prompt 优化 URL</label><input class="input" data-path="image.promptBaseUrl"></div>
              <div class="field wide"><label>Prompt 优化 Token</label><input class="input" data-path="image.promptToken" type="password"></div>
            </div>
          </form>
        </section>
      </div>

      <section class="panel">
        <div class="log-head">
          <div>
            <p class="section-title" style="margin-bottom:6px">Live Logs</p>
            <strong>日志监控</strong>
          </div>
          <select id="logSelect"></select>
        </div>
        <div class="terminal"><pre id="logOutput" class="empty">等待日志文件...</pre></div>
      </section>
    </section>
  </main>

  <script>
    const authed = document.body.dataset.authed === 'true';
    const loginView = document.querySelector('#loginView');
    const appView = document.querySelector('#appView');
    const topStatus = document.querySelector('#topStatus');
    const loginToast = document.querySelector('#loginToast');
    const appToast = document.querySelector('#appToast');
    const logSelect = document.querySelector('#logSelect');
    const logOutput = document.querySelector('#logOutput');
    const configForm = document.querySelector('#configForm');
    const autoStartToggle = document.querySelector('#autoStartToggle');
    const qrCard = document.querySelector('#qrCard');
    const qrCode = document.querySelector('#qrCode');
    let currentLog = '';
    let logTimer = null;
    let statusTimer = null;

    function showApp(isAuthed) {
      loginView.classList.toggle('hidden', isAuthed);
      appView.classList.toggle('hidden', !isAuthed);
      if (isAuthed) bootstrap();
    }

    async function api(path, options = {}) {
      const res = await fetch(path, {
        headers: { 'Content-Type': 'application/json' },
        credentials: 'same-origin',
        ...options
      });
      const data = await res.json().catch(() => ({}));
      if (!res.ok) throw new Error(data.error || '请求失败');
      return data;
    }

    document.querySelector('#loginForm').addEventListener('submit', async (event) => {
      event.preventDefault();
      loginToast.textContent = '';
      try {
        await api('/login', { method: 'POST', body: JSON.stringify({ password: document.querySelector('#password').value }) });
        showApp(true);
      } catch (error) {
        loginToast.textContent = error.message;
      }
    });

    document.querySelector('#startBtn').addEventListener('click', () => action('/api/start', '启动命令已发送'));
    document.querySelector('#stopBtn').addEventListener('click', () => action('/api/stop', '停止命令已发送'));
    document.querySelector('#robotLoginBtn').addEventListener('click', startRobotLogin);
    document.querySelector('#refreshBtn').addEventListener('click', loadLogs);
    document.querySelector('#logoutBtn').addEventListener('click', async () => {
      await api('/logout', { method: 'POST' });
      location.reload();
    });
    logSelect.addEventListener('change', () => {
      currentLog = logSelect.value;
      loadCurrentLog();
    });
    configForm.addEventListener('submit', async (event) => {
      event.preventDefault();
      appToast.textContent = '';
      try {
        await api('/api/config', { method: 'POST', body: JSON.stringify(collectConfig()) });
        appToast.textContent = '配置已保存';
        await refreshStatus();
      } catch (error) {
        appToast.textContent = error.message;
      }
    });
    autoStartToggle.addEventListener('change', async () => {
      appToast.textContent = '';
      try {
        await api('/api/autostart', { method: 'POST', body: JSON.stringify({ enabled: autoStartToggle.checked }) });
        appToast.textContent = autoStartToggle.checked ? '已启用开机自启' : '已关闭开机自启';
        await refreshStatus();
      } catch (error) {
        appToast.textContent = error.message;
        await refreshStatus();
      }
    });

    async function action(path, okText) {
      appToast.textContent = '';
      try {
        await api(path, { method: 'POST' });
        appToast.textContent = okText;
        await refreshStatus();
        await loadLogs();
      } catch (error) {
        appToast.textContent = error.message;
      }
    }

    async function startRobotLogin() {
      appToast.textContent = '';
      try {
        await api('/api/robot-login', { method: 'POST' });
        appToast.textContent = '登录流程已启动，二维码生成后会显示在左侧';
        await refreshStatus();
        await loadLogs();
      } catch (error) {
        appToast.textContent = error.message;
      }
    }

    function updateQRCode(data) {
      const visible = !!data.qrCodeExists && (data.processMode === 'login' || !data.cookieExists);
      qrCard.classList.toggle('hidden', !visible);
      if (visible) {
        qrCode.src = '/api/qrcode?t=' + Date.now();
      } else {
        qrCode.removeAttribute('src');
      }
    }

    async function bootstrap() {
      await refreshStatus();
      await loadConfig();
      await loadLogs();
      clearInterval(statusTimer);
      clearInterval(logTimer);
      statusTimer = setInterval(refreshStatus, 4000);
      logTimer = setInterval(loadCurrentLog, 1800);
    }

    async function refreshStatus() {
      try {
        const data = await api('/api/status');
        const runningText = data.running ? (data.processMode === 'login' ? '登录中' : '运行中') : '未运行';
        document.querySelector('#robotState').textContent = runningText;
        document.querySelector('#startedAt').textContent = data.startedAt || '—';
        document.querySelector('#rootDir').textContent = data.rootDir || '—';
        document.querySelector('#robotBin').textContent = data.robotBin || '未找到，安装版请检查 -bin 参数';
        document.querySelector('#configState').textContent = data.configExists ? '已保存' : '未保存';
        document.querySelector('#cookieState').textContent = data.cookieExists ? '已登录' : '未登录';
        document.querySelector('#stdoutLog').textContent = data.stdoutLog || '—';
        document.querySelector('#startBtn').disabled = data.running;
        document.querySelector('#stopBtn').disabled = !data.running;
        document.querySelector('#robotLoginBtn').disabled = data.running;
        autoStartToggle.disabled = !data.autoStartSupported;
        autoStartToggle.checked = !!data.autoStartEnabled;
        updateQRCode(data);
        document.querySelector('#autoStartText').textContent = data.autoStartSupported ? (data.autoStartEnabled ? '已启用' : '未启用') : '当前系统不支持';
        topStatus.innerHTML = '<span class="dot ' + (data.running ? 'on' : '') + '"></span><span>' + runningText + '</span>';
      } catch (error) {
        topStatus.innerHTML = '<span class="dot"></span><span>认证失效</span>';
      }
    }

    async function loadConfig() {
      const data = await api('/api/config');
      document.querySelector('#configPath').textContent = data.path || 'config.json';
      populateConfig(data.config || {});
    }

    function populateConfig(config) {
      for (const field of configFields()) {
        const value = getPath(config, field.dataset.path);
        if (field.type === 'checkbox') {
          field.checked = !!value;
        } else {
          field.value = value ?? '';
        }
      }
    }

    function collectConfig() {
      const config = {};
      for (const field of configFields()) {
        let value;
        if (field.type === 'checkbox') {
          value = field.checked;
        } else if (field.dataset.type === 'number') {
          value = Number(field.value || 0);
        } else {
          value = field.value;
        }
        setPath(config, field.dataset.path, value);
      }
      return config;
    }

    function configFields() {
      return Array.from(configForm.querySelectorAll('[data-path]'));
    }

    function getPath(target, path) {
      return path.split('.').reduce((value, key) => value && value[key], target);
    }

    function setPath(target, path, value) {
      const parts = path.split('.');
      let cursor = target;
      for (const part of parts.slice(0, -1)) {
        cursor[part] ||= {};
        cursor = cursor[part];
      }
      cursor[parts.at(-1)] = value;
    }

    async function loadLogs() {
      const data = await api('/api/logs');
      const files = data.files || [];
      const previous = currentLog;
      logSelect.innerHTML = '';
      for (const file of files) {
        const option = document.createElement('option');
        option.value = file.name;
        option.textContent = file.name + ' · ' + formatBytes(file.size) + ' · ' + file.modTime;
        logSelect.appendChild(option);
      }
      currentLog = files.some(file => file.name === previous) ? previous : (files[0]?.name || '');
      logSelect.value = currentLog;
      await loadCurrentLog();
    }

    async function loadCurrentLog() {
      if (!currentLog) {
        logOutput.textContent = '暂无日志文件。扫码登录或启动机器人后会自动创建 log/*.log。';
        logOutput.classList.add('empty');
        return;
      }
      const data = await api('/api/logs/read?file=' + encodeURIComponent(currentLog));
      logOutput.textContent = data.content || '日志文件为空。';
      logOutput.classList.toggle('empty', !data.content);
      const terminal = logOutput.parentElement;
      terminal.scrollTop = terminal.scrollHeight;
    }

    function formatBytes(size) {
      if (size < 1024) return size + ' B';
      if (size < 1024 * 1024) return (size / 1024).toFixed(1) + ' KB';
      return (size / 1024 / 1024).toFixed(1) + ' MB';
    }

    showApp(authed);
  </script>
</body>
</html>`
