package ui

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"meshlink/internal/certutil"
	"meshlink/internal/config"
	"meshlink/internal/diagnose"
	"meshlink/internal/onboarding"
	"meshlink/internal/rdp"
	"meshlink/internal/winservice"
)

//go:embed static/*
var staticFS embed.FS

type Server struct {
	log     *slog.Logger
	baseDir string
}

func NewServer(logger *slog.Logger) http.Handler {
	return NewServerWithBaseDir(logger, "")
}

func NewServerWithBaseDir(logger *slog.Logger, baseDir string) http.Handler {
	server := &Server{log: logger, baseDir: baseDir}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/info", server.handleInfo)
	mux.HandleFunc("GET /api/onboarding/defaults", server.handleOnboardingDefaults)
	mux.HandleFunc("POST /api/onboarding/start-server", server.handleOnboardingStartServer)
	mux.HandleFunc("POST /api/onboarding/create-hub", server.handleOnboardingCreateHub)
	mux.HandleFunc("POST /api/onboarding/invite", server.handleOnboardingInvite)
	mux.HandleFunc("POST /api/onboarding/join", server.handleOnboardingJoin)
	mux.HandleFunc("GET /api/onboarding/devices", server.handleOnboardingDevices)
	mux.HandleFunc("GET /api/service/status", server.handleServiceStatus)
	mux.HandleFunc("POST /api/service", server.handleServiceAction)
	mux.HandleFunc("POST /api/certs/init-ca", server.handleInitCA)
	mux.HandleFunc("POST /api/certs/issue", server.handleIssueCert)
	mux.HandleFunc("GET /api/config", server.handleGetConfig)
	mux.HandleFunc("POST /api/config", server.handleSaveConfig)
	mux.HandleFunc("GET /api/logs", server.handleLogs)
	mux.HandleFunc("POST /api/rdp/open", server.handleOpenRDP)
	mux.HandleFunc("GET /api/diagnostics", server.handleDiagnostics)
	mux.HandleFunc("GET /api/diagnostics/rdp", server.handleRDPDiagnostics)

	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err)
	}
	mux.Handle("/", http.FileServer(http.FS(sub)))
	return localOnly(mux)
}

func localOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.RemoteAddr
		if !strings.HasPrefix(host, "127.0.0.1:") {
			http.Error(w, "local access only", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	cwd := s.effectiveBaseDir()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":  true,
		"cwd": cwd,
	})
}

func (s *Server) handleOnboardingDefaults(w http.ResponseWriter, r *http.Request) {
	host, _ := os.Hostname()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":          true,
		"base_dir":    s.effectiveBaseDir(),
		"node_name":   host,
		"network":     "我的组网",
		"listen_port": 8443,
		"protocol":    "tcp_tls_v1",
		"config_path": filepath.Join(s.effectiveBaseDir(), "configs", "active.json"),
	})
}

func (s *Server) handleOnboardingStartServer(w http.ResponseWriter, r *http.Request) {
	var req onboarding.StartServerRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	result, err := s.onboardingManager().StartServerMode(req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": result})
}

func (s *Server) handleOnboardingCreateHub(w http.ResponseWriter, r *http.Request) {
	var req onboarding.CreateHubRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	result, err := s.onboardingManager().CreateHub(req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": result})
}

func (s *Server) handleOnboardingInvite(w http.ResponseWriter, r *http.Request) {
	var req onboarding.CreateInviteRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	invite, err := s.onboardingManager().CreateInvite(req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "invite": invite})
}

func (s *Server) handleOnboardingJoin(w http.ResponseWriter, r *http.Request) {
	var req onboarding.JoinSpokeRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	result, err := s.onboardingManager().JoinSpoke(req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": result})
}

func (s *Server) handleOnboardingDevices(w http.ResponseWriter, r *http.Request) {
	devices, err := s.onboardingManager().Devices(r.URL.Query().Get("service_name"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "devices": devices})
}

func (s *Server) handleServiceStatus(w http.ResponseWriter, r *http.Request) {
	status, err := winservice.Status(r.URL.Query().Get("service_name"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": status})
}

func (s *Server) handleServiceAction(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Action      string `json:"action"`
		ServiceName string `json:"service_name"`
		ConfigPath  string `json:"config_path"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}

	var err error
	switch req.Action {
	case "install":
		err = installAgentService(req.ServiceName, req.ConfigPath)
	case "uninstall":
		err = winservice.Uninstall(req.ServiceName)
	case "start":
		err = winservice.Start(req.ServiceName)
	case "stop":
		err = winservice.Stop(req.ServiceName)
	default:
		err = errors.New("unknown service action")
	}
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleInitCA(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OutDir string `json:"out_dir"`
		Name   string `json:"name"`
		Days   int    `json:"days"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	result, err := certutil.InitCA(certutil.CAOptions{
		OutDir: req.OutDir,
		Name:   req.Name,
		Days:   req.Days,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": result})
}

func (s *Server) handleIssueCert(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OutDir    string `json:"out_dir"`
		Name      string `json:"name"`
		CAPath    string `json:"ca_path"`
		CAKeyPath string `json:"ca_key_path"`
		DNS       string `json:"dns"`
		IPs       string `json:"ips"`
		Days      int    `json:"days"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	result, err := certutil.Issue(certutil.IssueOptions{
		OutDir:    req.OutDir,
		Name:      req.Name,
		CAPath:    req.CAPath,
		CAKeyPath: req.CAKeyPath,
		DNSNames:  certutil.SplitCSV(req.DNS),
		IPAddrs:   certutil.SplitCSV(req.IPs),
		Days:      req.Days,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": result})
}

func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		writeError(w, errors.New("path is required"))
		return
	}
	b, err := os.ReadFile(path)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"content": string(b),
	})
}

func (s *Server) handleSaveConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Path == "" {
		writeError(w, errors.New("path is required"))
		return
	}
	var cfg config.Config
	if err := json.Unmarshal([]byte(req.Content), &cfg); err != nil {
		writeError(w, err)
		return
	}
	if err := cfg.Validate(); err != nil {
		writeError(w, err)
		return
	}
	pretty, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		writeError(w, err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(req.Path), 0o700); err != nil {
		writeError(w, err)
		return
	}
	if err := os.WriteFile(req.Path, append(pretty, '\n'), 0o600); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "content": string(pretty)})
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	configPath := r.URL.Query().Get("config_path")
	serviceName := r.URL.Query().Get("service_name")
	if serviceName == "" {
		serviceName = winservice.DefaultName
	}
	if configPath == "" {
		writeError(w, errors.New("config_path is required"))
		return
	}
	logPath := filepath.Join(filepath.Dir(configPath), "logs", serviceName+".log")
	n := int64(65536)
	if raw := r.URL.Query().Get("bytes"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err == nil && parsed > 0 && parsed <= 1024*1024 {
			n = parsed
		}
	}
	text, err := tailFile(logPath, n)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "path": logPath, "content": text})
}

func (s *Server) handleOpenRDP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Target string `json:"target"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := rdp.Open(req.Target); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleDiagnostics(w http.ResponseWriter, r *http.Request) {
	report := diagnose.Run(r.URL.Query().Get("config_path"), r.URL.Query().Get("service_name"))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "report": report})
}

func (s *Server) handleRDPDiagnostics(w http.ResponseWriter, r *http.Request) {
	check := diagnose.CheckRDP(r.URL.Query().Get("target"))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "check": check})
}

func tailFile(path string, maxBytes int64) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	start := info.Size() - maxBytes
	if start < 0 {
		start = 0
	}
	if _, err := file.Seek(start, io.SeekStart); err != nil {
		return "", err
	}
	b, err := io.ReadAll(file)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func installAgentService(name, configPath string) error {
	if name == "" {
		name = winservice.DefaultName
	}
	agentPath, err := findAgentExe()
	if err != nil {
		return err
	}
	configPath, err = filepath.Abs(configPath)
	if err != nil {
		return err
	}
	cmd := exec.Command(agentPath, "-service", "install", "-service-name", name, "-config", configPath)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(out.String())
		if msg != "" {
			return fmt.Errorf("%w: %s", err, msg)
		}
		return err
	}
	return nil
}

func findAgentExe() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	cwd, _ := os.Getwd()
	candidates := []string{
		filepath.Join(filepath.Dir(exe), "mesh-agent.exe"),
		filepath.Join(cwd, "mesh-agent.exe"),
		filepath.Join(cwd, "bin", "mesh-agent.exe"),
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return filepath.Abs(candidate)
		}
	}
	return "", fmt.Errorf("找不到 mesh-agent.exe，请确认它和当前程序在同一目录，或位于当前目录的 bin 目录下")
}

func (s *Server) effectiveBaseDir() string {
	if s.baseDir != "" {
		return s.baseDir
	}
	cwd, _ := os.Getwd()
	return cwd
}

func (s *Server) onboardingManager() onboarding.Manager {
	return onboarding.Manager{BaseDir: s.effectiveBaseDir()}
}

func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if r.Body == nil {
		writeError(w, errors.New("missing request body"))
		return false
	}
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, 2<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, err)
		return false
	}
	return true
}

func writeError(w http.ResponseWriter, err error) {
	writeJSON(w, http.StatusBadRequest, map[string]any{
		"ok":    false,
		"error": err.Error(),
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
