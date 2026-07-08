package ui

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"meshlink/internal/certutil"
	"meshlink/internal/onboarding"
)

func TestOnboardingCreateHubAndInviteAPI(t *testing.T) {
	dir := t.TempDir()
	handler := NewServerWithBaseDir(slog.New(slog.NewTextHandler(io.Discard, nil)), dir)

	createResp := postJSONForTest(t, handler, "/api/onboarding/create-hub", map[string]any{
		"network_name": "mesh",
		"node_name":    "hub",
		"listen_port":  8443,
	})
	if createResp["ok"] != true {
		t.Fatalf("create response: %+v", createResp)
	}
	result := createResp["result"].(map[string]any)
	if result["config_path"] != filepath.Join(dir, "configs", "active.json") {
		t.Fatalf("config_path = %v", result["config_path"])
	}

	inviteResp := postJSONForTest(t, handler, "/api/onboarding/invite", map[string]any{
		"server": "example.com:8443",
	})
	if inviteResp["ok"] != true {
		t.Fatalf("invite response: %+v", inviteResp)
	}
	invite := inviteResp["invite"].(map[string]any)
	if invite["code"] == "" || invite["link"] == "" {
		t.Fatalf("invite missing code/link: %+v", invite)
	}
}

func TestOnboardingStartServerModeAPIReturnsInvite(t *testing.T) {
	dir := t.TempDir()
	handler := NewServerWithBaseDir(slog.New(slog.NewTextHandler(io.Discard, nil)), dir)

	resp := postJSONForTest(t, handler, "/api/onboarding/start-server", map[string]any{
		"server_address": "desk.example.com",
		"listen_port":    9443,
	})
	if resp["ok"] != true {
		t.Fatalf("start server response: %+v", resp)
	}
	result := resp["result"].(map[string]any)
	if result["virtual_ip"] != "10.77.0.1" {
		t.Fatalf("virtual_ip = %v, want 10.77.0.1", result["virtual_ip"])
	}
	invite := result["invite"].(map[string]any)
	if invite["server"] != "desk.example.com:9443" {
		t.Fatalf("invite.server = %v, want desk.example.com:9443", invite["server"])
	}
	if invite["code"] == "" || invite["link"] == "" {
		t.Fatalf("invite missing code/link: %+v", invite)
	}
}

func TestOnboardingInviteAPIExposesLongLivedDeviceLimit(t *testing.T) {
	dir := t.TempDir()
	handler := NewServerWithBaseDir(slog.New(slog.NewTextHandler(io.Discard, nil)), dir)

	inviteResp := postJSONForTest(t, handler, "/api/onboarding/invite", map[string]any{
		"server":     "example.com:8443",
		"long_lived": true,
		"max_uses":   3,
	})
	if inviteResp["ok"] != true {
		t.Fatalf("invite response: %+v", inviteResp)
	}
	invite := inviteResp["invite"].(map[string]any)
	maxUses, _ := invite["max_uses"].(float64)
	if invite["long_lived"] != true || int(maxUses) != 3 {
		t.Fatalf("invite = %+v, want long-lived max_uses=3", invite)
	}
}

func TestOnboardingDeviceAdminAPI(t *testing.T) {
	dir := t.TempDir()
	mgr := onboarding.Manager{BaseDir: dir, LocalIPv4: func() (string, error) { return "192.168.1.23", nil }}
	if _, err := mgr.CreateHub(onboarding.CreateHubRequest{NetworkName: "mesh", NodeName: "hub", ListenPort: 8443}); err != nil {
		t.Fatal(err)
	}
	invite, err := mgr.CreateInvite(onboarding.CreateInviteRequest{Server: "example.com:8443"})
	if err != nil {
		t.Fatal(err)
	}
	csr, err := certutil.CreateCSR(certutil.CSROptions{OutDir: t.TempDir(), Name: "laptop"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.HandleEnroll(onboarding.EnrollRequest{
		Token:    invite.Token,
		Code:     invite.Code,
		NodeName: "laptop",
		CSRPEM:   csr.CSRPEM,
	}); err != nil {
		t.Fatal(err)
	}

	handler := NewServerWithBaseDir(slog.New(slog.NewTextHandler(io.Discard, nil)), dir)
	renameResp := postJSONForTest(t, handler, "/api/onboarding/device/rename", map[string]any{
		"node_id":      "laptop",
		"display_name": "Alice laptop",
	})
	if renameResp["ok"] != true {
		t.Fatalf("rename response: %+v", renameResp)
	}
	disableResp := postJSONForTest(t, handler, "/api/onboarding/device/disable", map[string]any{
		"node_id": "laptop",
	})
	if disableResp["ok"] != true {
		t.Fatalf("disable response: %+v", disableResp)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/onboarding/devices", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("devices returned %d: %s", rec.Code, rec.Body.String())
	}
	var devicesResp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &devicesResp); err != nil {
		t.Fatal(err)
	}
	devices := devicesResp["devices"].(map[string]any)["nodes"].([]any)
	var found map[string]any
	for _, item := range devices {
		node := item.(map[string]any)
		if node["node_id"] == "laptop" {
			found = node
			break
		}
	}
	if found == nil || found["status"] != "disabled" || found["display_name"] != "Alice laptop" {
		t.Fatalf("devices = %+v, want disabled renamed laptop", devices)
	}

	removeResp := postJSONForTest(t, handler, "/api/onboarding/device/remove", map[string]any{
		"node_id": "laptop",
	})
	if removeResp["ok"] != true {
		t.Fatalf("remove response: %+v", removeResp)
	}
}

func TestStaticHomeHighlightsSimpleModes(t *testing.T) {
	b, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(b)
	for _, want := range []string{
		"我有公网 IP，创建服务器",
		"我没有公网 IP，使用官方 Hub",
		"我有云服务器，帮我自建中继",
		"加入已有网络",
		"设备列表",
		"长期接入码风险",
		"设备数限制",
		"重命名",
		"禁用",
		"移除",
		"云服务器地址",
		"SSH 端口",
		"SSH 用户名",
		"SSH 密码",
		"私钥内容",
		"Meshlink 监听端口",
		"服务器公网访问地址",
		"检查云服务器",
		"部署自建中继",
		"远程服务状态",
		"主机指纹",
		"有效期",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("home page does not contain %q", want)
		}
	}
	for _, unavailable := range []string{"官方 Hub 尚未开放"} {
		if !strings.Contains(html, unavailable) {
			t.Fatalf("home page should mark unavailable entry %q", unavailable)
		}
	}
	if strings.Contains(html, "自建中继向导尚未开放") {
		t.Fatal("self-hosted relay entry should open a real deployment wizard")
	}
	advancedIndex := strings.Index(html, "高级设置")
	if advancedIndex < 0 {
		t.Fatal("home page should keep advanced settings collapsed")
	}
	for _, hidden := range []string{"JSON 配置", "CA 名称", "签发节点证书", "服务名", "配置文件路径", "开始诊断", "读取日志"} {
		index := strings.Index(html, hidden)
		if index >= 0 && index < advancedIndex {
			t.Fatalf("home page exposes advanced field %q before advanced settings", hidden)
		}
	}
}

func TestSelfHostedRelayDeployAPIValidatesRequiredFields(t *testing.T) {
	dir := t.TempDir()
	handler := NewServerWithBaseDir(slog.New(slog.NewTextHandler(io.Discard, nil)), dir)
	req := httptest.NewRequest(http.MethodPost, "/api/self-relay/deploy", bytes.NewReader([]byte(`{"cloud_server_address":""}`)))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("deploy returned %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "云服务器地址不能为空") {
		t.Fatalf("body = %s, want validation error", rec.Body.String())
	}
}

func TestSelfHostedRelayUIUsesTOFUOnlyForCheck(t *testing.T) {
	b, err := staticFS.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(b)
	for _, want := range []string{
		"selfRelayRequest(true)",
		"selfRelayRequest(false)",
	} {
		if !strings.Contains(js, want) {
			t.Fatalf("app.js missing %q", want)
		}
	}
}

func TestRDPDiagnosticsAPIIncludesUserContext(t *testing.T) {
	dir := t.TempDir()
	handler := NewServerWithBaseDir(slog.New(slog.NewTextHandler(io.Discard, nil)), dir)

	req := httptest.NewRequest(http.MethodGet, "/api/diagnostics/rdp?target=10.77.0.9&target_device=office-pc&tunnel_status=offline", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("diagnostics returned %d: %s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	check := out["check"].(map[string]any)
	detail := check["detail"].(string)
	for _, want := range []string{"隧道状态：offline", "目标设备：office-pc", "目标 IP：10.77.0.9", "RDP 端口：3389", "开启 Windows 远程桌面"} {
		if !strings.Contains(detail, want) {
			t.Fatalf("RDP detail = %q, want %q", detail, want)
		}
	}
}

func postJSONForTest(t *testing.T, handler http.Handler, path string, body any) map[string]any {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s returned %d: %s", path, rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}
