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

func TestStaticHomeHighlightsSimpleModes(t *testing.T) {
	b, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(b)
	for _, want := range []string{"服务器模式", "客户端模式", "组网机群节点列表"} {
		if !strings.Contains(html, want) {
			t.Fatalf("home page does not contain %q", want)
		}
	}
	for _, hidden := range []string{"高级设置", "JSON 配置", "CA 名称", "签发节点证书", "服务名", "配置文件路径", "开始诊断", "读取日志"} {
		if strings.Contains(html, hidden) {
			t.Fatalf("home page exposes advanced field %q", hidden)
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
