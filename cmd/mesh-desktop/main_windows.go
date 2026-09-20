//go:build windows

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"
	"golang.org/x/sys/windows"

	meshagent "meshlink/internal/agent"
	"meshlink/internal/certutil"
	"meshlink/internal/cloudhub"
	"meshlink/internal/config"
	"meshlink/internal/diagnose"
	"meshlink/internal/networklifecycle"
	"meshlink/internal/networkstate"
	"meshlink/internal/onboarding"
	"meshlink/internal/p2p"
	"meshlink/internal/productflags"
	"meshlink/internal/rdp"
	"meshlink/internal/runner"
	meshupdate "meshlink/internal/update"
	"meshlink/internal/version"
	"meshlink/internal/winservice"
)

type desktopApp struct {
	mw         *walk.MainWindow
	connecting bool

	serviceName   *walk.LineEdit
	configPath    *walk.LineEdit
	networkName   *walk.LineEdit
	hubNodeName   *walk.LineEdit
	listenPort    *walk.LineEdit
	inviteServer  *walk.LineEdit
	inviteLink    *walk.LineEdit
	inviteCode    *walk.LineEdit
	inviteMaxUses *walk.LineEdit
	longLivedCode *walk.CheckBox
	spokeNodeName *walk.LineEdit
	certOut       *walk.LineEdit
	caName        *walk.LineEdit
	nodeName      *walk.LineEdit
	dnsSANs       *walk.LineEdit
	ipSANs        *walk.LineEdit
	certDays      *walk.LineEdit
	rdpTarget     *walk.LineEdit
	updateURL     *walk.LineEdit
	updateHost    *walk.LineEdit
	updatePort    *walk.LineEdit

	serviceState       *walk.Label
	onboardingState    *walk.Label
	quickState         *walk.Label
	updateState        *walk.Label
	configEdit         *walk.TextEdit
	updateOutput       *walk.TextEdit
	inviteOutput       *walk.TextEdit
	meshList           *walk.ListBox
	meshDetail         *walk.TextEdit
	meshSummary        *walk.Label
	coordinatorSummary *walk.Label
	p2pPathSummary     *walk.Label
	meshModel          *meshNodeModel
	meshSelected       string
	networkState       networkstate.State
	coordinatorState   string
	p2pListen          string
	latestUpdate       *meshupdate.Manifest
	output             *walk.TextEdit
}

type desktopSettings struct {
	LastConfigPath string `json:"last_config_path"`
	LastUpdateURL  string `json:"last_update_url,omitempty"`
}

type meshNode struct {
	p2p.ConnectionStatus
	Key              string
	Kind             string
	Online           bool
	NodeID           string
	DisplayName      string
	Mode             string
	VirtualIP        string
	Listen           string
	Connect          string
	Routes           []string
	RemoteAddr       string
	Fingerprint      string
	CommonName       string
	ConnectedAt      time.Time
	LastSeen         time.Time
	DisconnectedAt   *time.Time
	UpdatedAt        time.Time
	LastHeartbeat    time.Time
	State            string
	CoordinatorState string
	ErrorCode        string
	StatusPath       string
}

type meshNodeModel struct {
	walk.ListModelBase
	items []meshNode
}

func (m *meshNodeModel) ItemCount() int {
	return len(m.items)
}

func (m *meshNodeModel) Value(index int) interface{} {
	if index < 0 || index >= len(m.items) {
		return ""
	}
	node := m.items[index]
	text := meshNodeConnectionLabel(node) + "  " + nodeTitle(node)
	if node.VirtualIP != "" {
		text += "  " + node.VirtualIP
	}
	return text
}

func (m *meshNodeModel) SetItems(items []meshNode) {
	m.items = items
	m.PublishItemsReset()
}

type meshListStyler struct {
	list  **walk.ListBox
	model *meshNodeModel
}

func (s *meshListStyler) ItemHeightDependsOnWidth() bool {
	return false
}

func (s *meshListStyler) DefaultItemHeight() int {
	return s.scale(58)
}

func (s *meshListStyler) ItemHeight(index, width int) int {
	return s.DefaultItemHeight()
}

func (s *meshListStyler) StyleItem(style *walk.ListItemStyle) {
	if s.model == nil || style.Index() < 0 || style.Index() >= len(s.model.items) {
		return
	}
	node := s.model.items[style.Index()]
	if style.Index()%2 == 1 {
		style.BackgroundColor = walk.RGB(250, 252, 255)
	}
	_ = style.DrawBackground()

	bounds := style.BoundsPixels()
	canvas := style.Canvas()
	if canvas == nil {
		return
	}

	dotColor := meshNodeDotColor(node)
	if brush, err := walk.NewSolidColorBrush(dotColor); err == nil {
		defer brush.Dispose()
		dot := walk.Rectangle{X: bounds.X + s.scale(15), Y: bounds.Y + s.scale(22), Width: s.scale(10), Height: s.scale(10)}
		_ = canvas.FillEllipsePixels(brush, dot)
	}

	textLeft := bounds.X + s.scale(36)
	textWidth := bounds.Width - s.scale(48)
	title := nodeTitle(node)
	if node.VirtualIP != "" {
		title += "    " + node.VirtualIP
	}
	subtitle := meshNodeConnectionSummary(node)
	if node.RemoteAddr != "" {
		subtitle += " · " + sourceIP(node.RemoteAddr)
	} else if node.Listen != "" {
		subtitle += " · 监听 " + node.Listen
	} else if node.Connect != "" {
		subtitle += " · 连接 " + node.Connect
	}

	titleRect := walk.Rectangle{X: textLeft, Y: bounds.Y + s.scale(9), Width: textWidth, Height: s.scale(22)}
	subtitleRect := walk.Rectangle{X: textLeft, Y: bounds.Y + s.scale(31), Width: textWidth, Height: s.scale(20)}
	_ = style.DrawText(title, titleRect, walk.TextSingleLine|walk.TextEndEllipsis)
	style.TextColor = walk.RGB(100, 116, 139)
	_ = style.DrawText(subtitle, subtitleRect, walk.TextSingleLine|walk.TextEndEllipsis)
}

func nodeTitle(node meshNode) string {
	if strings.TrimSpace(node.DisplayName) != "" {
		return strings.TrimSpace(node.DisplayName)
	}
	return node.NodeID
}

func (s *meshListStyler) scale(v int) int {
	dpi := 96
	if s.list != nil && *s.list != nil {
		dpi = (*s.list).DPI()
	}
	return walk.IntFrom96DPI(v, dpi)
}

var app = &desktopApp{}

func main() {
	if relaunched, err := relaunchAsAdministrator(); err != nil {
		walk.MsgBox(nil, "需要管理员权限", "Meshlink 需要管理员权限来管理连接服务和虚拟网卡。\r\n"+err.Error(), walk.MsgBoxIconError)
		os.Exit(1)
	} else if relaunched {
		return
	}
	if err := app.run(); err != nil {
		walk.MsgBox(nil, "启动失败", err.Error(), walk.MsgBoxIconError)
		os.Exit(1)
	}
}

func relaunchAsAdministrator() (bool, error) {
	if windows.GetCurrentProcessToken().IsElevated() {
		return false, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return false, err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return false, err
	}
	args := make([]string, 0, len(os.Args)-1)
	for _, arg := range os.Args[1:] {
		args = append(args, syscall.EscapeArg(arg))
	}
	verb, _ := windows.UTF16PtrFromString("runas")
	file, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		return false, err
	}
	dir, err := windows.UTF16PtrFromString(cwd)
	if err != nil {
		return false, err
	}
	params, err := windows.UTF16PtrFromString(strings.Join(args, " "))
	if err != nil {
		return false, err
	}
	err = windows.ShellExecute(0, verb, file, params, dir, windows.SW_SHOWNORMAL)
	return err == nil, err
}

func (a *desktopApp) run() error {
	bg := SolidColorBrush{Color: walk.RGB(246, 248, 251)}
	header := SolidColorBrush{Color: walk.RGB(20, 33, 48)}
	panel := SolidColorBrush{Color: walk.RGB(255, 255, 255)}
	ink := walk.RGB(24, 36, 52)
	muted := walk.RGB(101, 113, 128)
	accent := walk.RGB(9, 105, 98)
	mono := Font{Family: "Consolas", PointSize: 10}
	a.meshModel = &meshNodeModel{}
	meshStyler := &meshListStyler{list: &a.meshList, model: a.meshModel}

	window := MainWindow{
		AssignTo:   &a.mw,
		Title:      "Meshlink 远程桌面组网",
		MinSize:    Size{Width: 960, Height: 620},
		Size:       Size{Width: 1060, Height: 690},
		Layout:     VBox{MarginsZero: true, SpacingZero: true},
		Background: bg,
		MenuItems: []MenuItem{
			Menu{
				Text: "帮助",
				Items: []MenuItem{
					Action{Text: "关于 / 检查更新", OnTriggered: a.showAboutDialog},
				},
			},
		},
		Children: []Widget{
			Composite{
				Background: header,
				Layout:     HBox{Margins: Margins{Left: 18, Top: 14, Right: 18, Bottom: 14}, Spacing: 14},
				Children: []Widget{
					Composite{
						Layout:        VBox{MarginsZero: true, Spacing: 3},
						StretchFactor: 1,
						Background:    header,
						Children: []Widget{
							Label{
								Text:      "Meshlink 远程桌面组网",
								TextColor: walk.RGB(255, 255, 255),
								Font:      Font{Family: "Microsoft YaHei UI", PointSize: 16, Bold: true},
							},
							Label{
								Text:      "设备列表 · 创建服务器 · 加入已有网络",
								TextColor: walk.RGB(198, 210, 222),
							},
						},
					},
					Label{
						AssignTo:      &a.quickState,
						Text:          "就绪",
						TextColor:     walk.RGB(255, 255, 255),
						MinSize:       Size{Width: 130, Height: 28},
						TextAlignment: AlignCenter,
						Background:    SolidColorBrush{Color: accent},
					},
				},
			},
			HSplitter{
				HandleWidth:   6,
				StretchFactor: 1,
				Children: []Widget{
					Composite{
						MinSize:    Size{Width: 340, Height: 0},
						MaxSize:    Size{Width: 410, Height: 0},
						Background: bg,
						Layout:     VBox{Margins: Margins{Left: 12, Top: 12, Right: 8, Bottom: 12}, Spacing: 8},
						Children: []Widget{
							GroupBox{
								Title:      "使用入口",
								Background: panel,
								Layout:     Grid{Columns: 2, Margins: Margins{Left: 10, Top: 15, Right: 10, Bottom: 10}, Spacing: 6},
								Children: []Widget{
									PushButton{Text: "我有公网 IP，创建服务器", OnClicked: a.focusCreateServer, ColumnSpan: 2},
									PushButton{Text: "我没有公网 IP，使用官方 Hub", OnClicked: a.showOfficialHubMVPDialog, Visible: productflags.OfficialHubMVPEnabled(), ColumnSpan: 2},
									PushButton{Text: "加入已有网络", OnClicked: a.focusJoinNetwork, ColumnSpan: 2},
								},
							},
							GroupBox{
								Title:      "创建服务器",
								Background: panel,
								Layout:     Grid{Columns: 4, Margins: Margins{Left: 10, Top: 15, Right: 10, Bottom: 10}, Spacing: 6},
								Children: []Widget{
									Label{Text: "域名或公网地址", TextColor: muted},
									LineEdit{AssignTo: &a.inviteServer, CueBanner: "example.com:8443", ColumnSpan: 3},
									Label{Text: "监听端口", TextColor: muted},
									LineEdit{AssignTo: &a.listenPort, Text: "8443", ColumnSpan: 3},
									PushButton{Text: "启动服务器", OnClicked: a.createHubOnboarding, ColumnSpan: 2},
									PushButton{Text: "停止服务器", OnClicked: func() { a.serviceAction("stop") }, ColumnSpan: 2},
									PushButton{Text: "重新生成接入码", OnClicked: a.createInviteOnboarding, ColumnSpan: 4},
									TextEdit{AssignTo: &a.inviteOutput, ReadOnly: true, VScroll: true, MinSize: Size{Width: 0, Height: 76}, MaxSize: Size{Width: 10000, Height: 96}, ColumnSpan: 4},
									Label{AssignTo: &a.onboardingState, Text: "状态：服务器未启动", TextColor: muted, ColumnSpan: 4},
								},
							},
							GroupBox{
								Title:      "加入已有网络",
								Background: panel,
								Layout:     Grid{Columns: 4, Margins: Margins{Left: 10, Top: 15, Right: 10, Bottom: 10}, Spacing: 6},
								Children: []Widget{
									Label{Text: "邀请链接", TextColor: muted},
									LineEdit{AssignTo: &a.inviteLink, CueBanner: "meshlink://join?...", ColumnSpan: 3},
									Label{Text: "验证码", TextColor: muted},
									LineEdit{AssignTo: &a.inviteCode, ColumnSpan: 3},
									Label{Text: "本机名称", TextColor: muted},
									LineEdit{AssignTo: &a.spokeNodeName, Text: defaultNodeName("spoke"), ColumnSpan: 3},
									PushButton{Text: "连接", OnClicked: a.connectNetwork, ColumnSpan: 4},
									PushButton{Text: "断开连接", OnClicked: a.disconnectNetwork, ColumnSpan: 2},
									PushButton{Text: "退出网络", OnClicked: a.exitNetwork, ColumnSpan: 2},
								},
							},
						},
					},
					TabWidget{
						StretchFactor:  1,
						ContentMargins: Margins{Left: 10, Top: 10, Right: 10, Bottom: 10},
						Pages: []TabPage{
							{
								Title:  "组网机群节点列表",
								Layout: VBox{MarginsZero: true, Spacing: 10},
								Children: []Widget{
									Composite{
										Layout: HBox{MarginsZero: true, Spacing: 6},
										Children: []Widget{
											Label{Text: "协调服务器：", TextColor: muted},
											Label{AssignTo: &a.coordinatorSummary, Text: "已断开", TextColor: ink},
											Label{Text: "对端路径：", TextColor: muted},
											Label{AssignTo: &a.p2pPathSummary, Text: "离线或未知", TextColor: ink, StretchFactor: 1},
										},
									},
									Composite{
										Layout: HBox{MarginsZero: true, Spacing: 8},
										Children: []Widget{
											Label{Text: "节点列表", TextColor: ink, Font: Font{Family: "Microsoft YaHei UI", PointSize: 11, Bold: true}},
											Label{AssignTo: &a.meshSummary, Text: "等待刷新", TextColor: muted, StretchFactor: 1},
											PushButton{Text: "一键诊断", OnClicked: a.runOneClickDiagnostics},
											PushButton{Text: "打开远程桌面", OnClicked: a.openRDP},
											PushButton{Text: "诊断远程桌面", OnClicked: a.checkRDP},
											PushButton{Text: "重命名设备", OnClicked: a.renameSelectedDevice},
											PushButton{Text: "禁用设备", OnClicked: a.disableSelectedDevice},
											PushButton{Text: "移除设备", OnClicked: a.removeSelectedDevice},
											PushButton{Text: "刷新列表", OnClicked: a.loadMeshStatus},
										},
									},
									HSplitter{
										StretchFactor: 1,
										HandleWidth:   6,
										Children: []Widget{
											ListBox{
												AssignTo:              &a.meshList,
												Model:                 a.meshModel,
												ItemStyler:            meshStyler,
												Font:                  Font{Family: "Microsoft YaHei UI", PointSize: 10},
												MinSize:               Size{Width: 280, Height: 0},
												MaxSize:               Size{Width: 360, Height: 0},
												OnCurrentIndexChanged: a.showSelectedMeshNode,
											},
											TextEdit{
												AssignTo:      &a.meshDetail,
												ReadOnly:      true,
												Font:          mono,
												VScroll:       true,
												HScroll:       true,
												Text:          "左侧会显示当前组网节点；点击一个节点查看连接方式、延迟和最近在线时间。",
												StretchFactor: 1,
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}
	if err := window.Create(); err != nil {
		return err
	}
	a.restoreSimpleModeState()
	a.refreshServiceStatusLabels()
	a.loadMeshStatus()
	stopRefresh := a.startMeshStatusAutoRefresh()
	a.mw.Show()
	a.mw.Run()
	close(stopRefresh)
	return nil
}

func appBaseDir() string {
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		if strings.EqualFold(filepath.Base(dir), "bin") {
			return filepath.Dir(dir)
		}
		return dir
	}
	if cwd, err := os.Getwd(); err == nil {
		return cwd
	}
	return "."
}

func defaultNodeName(fallback string) string {
	if host, err := os.Hostname(); err == nil && strings.TrimSpace(host) != "" {
		return host
	}
	return fallback
}

func defaultConfigPath(baseDir string) string {
	return filepath.Join(baseDir, "configs", "active.json")
}

func monoFont() Font {
	return Font{Family: "Consolas", PointSize: 10}
}

func settingsPath(baseDir string) string {
	return filepath.Join(baseDir, "configs", "desktop-state.json")
}

func loadDesktopSettings(baseDir string) desktopSettings {
	b, err := os.ReadFile(settingsPath(baseDir))
	if err != nil {
		return desktopSettings{}
	}
	var settings desktopSettings
	if err := json.Unmarshal(b, &settings); err != nil {
		return desktopSettings{}
	}
	return settings
}

func saveDesktopSettings(baseDir string, settings desktopSettings) error {
	b, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	path := settingsPath(baseDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}

func rememberedConfigPath(baseDir string) string {
	defaultPath := defaultConfigPath(baseDir)
	settings := loadDesktopSettings(baseDir)
	path := strings.TrimSpace(settings.LastConfigPath)
	if path == "" {
		return defaultPath
	}
	if info, err := os.Stat(path); err == nil && !info.IsDir() {
		return path
	}
	return defaultPath
}

func (a *desktopApp) currentServiceName() string {
	if a != nil && a.serviceName != nil {
		if name := strings.TrimSpace(a.serviceName.Text()); name != "" {
			return name
		}
	}
	return winservice.DefaultName
}

func (a *desktopApp) currentConfigPath() string {
	if a != nil && a.configPath != nil {
		if path := strings.TrimSpace(a.configPath.Text()); path != "" {
			return path
		}
	}
	return discoverCurrentConfigPath(appBaseDir(), a.currentServiceName())
}

func (a *desktopApp) setCurrentConfigPath(configPath string) {
	configPath = strings.TrimSpace(configPath)
	if configPath == "" {
		return
	}
	absPath, err := filepath.Abs(configPath)
	if err == nil {
		configPath = absPath
	}
	if a != nil && a.configPath != nil {
		a.configPath.SetText(configPath)
	}
	_ = rememberConfigPath(appBaseDir(), configPath)
}

func rememberConfigPath(baseDir, configPath string) error {
	configPath = strings.TrimSpace(configPath)
	if configPath == "" {
		return nil
	}
	absPath, err := filepath.Abs(configPath)
	if err != nil {
		return err
	}
	settings := loadDesktopSettings(baseDir)
	settings.LastConfigPath = absPath
	return saveDesktopSettings(baseDir, settings)
}

func discoverCurrentConfigPath(baseDir, serviceName string) string {
	status, err := winservice.Status(serviceName)
	if err == nil {
		return configPathFromStatusOrSettings(baseDir, status)
	}
	return rememberedConfigPath(baseDir)
}

func configPathFromStatusOrSettings(baseDir string, status winservice.ServiceStatus) string {
	if status.Installed {
		configPath := strings.TrimSpace(status.ConfigPath)
		if configPath != "" {
			if info, err := os.Stat(configPath); err == nil && !info.IsDir() {
				return configPath
			}
		}
	}
	return rememberedConfigPath(baseDir)
}

func rememberedUpdateURL(baseDir string) string {
	settings := loadDesktopSettings(baseDir)
	updateURL := strings.TrimSpace(settings.LastUpdateURL)
	if updateURL == "" {
		return meshupdate.DefaultServerURL
	}
	return updateURL
}

func rememberUpdateURL(baseDir, updateURL string) error {
	updateURL = strings.TrimSpace(updateURL)
	if updateURL == "" {
		return nil
	}
	normalized, err := meshupdate.NormalizeBaseURL(updateURL)
	if err != nil {
		return err
	}
	settings := loadDesktopSettings(baseDir)
	settings.LastUpdateURL = normalized
	return saveDesktopSettings(baseDir, settings)
}

func updateBaseURLFromHostPort(host, port string) (string, error) {
	host = strings.TrimSpace(host)
	port = strings.TrimSpace(port)
	if host == "" {
		host = meshupdate.DefaultServerURL
	}
	if !strings.Contains(host, "://") {
		host = "http://" + host
	}
	u, err := url.Parse(host)
	if err != nil {
		return "", err
	}
	if u.Host == "" {
		return "", fmt.Errorf("更新地址为空")
	}
	if port != "" {
		hostname := u.Hostname()
		if hostname == "" {
			hostname = u.Host
		}
		u.Host = net.JoinHostPort(hostname, port)
	}
	return meshupdate.NormalizeBaseURL(u.String())
}

func splitUpdateBaseURL(raw string) (string, string) {
	normalized, err := meshupdate.NormalizeBaseURL(raw)
	if err != nil {
		normalized = meshupdate.DefaultServerURL
	}
	u, err := url.Parse(normalized)
	if err != nil {
		return "10.77.0.1", "1263"
	}
	host := u.Hostname()
	if host == "" {
		host = "10.77.0.1"
	}
	port := u.Port()
	if port == "" {
		port = "1263"
	}
	return host, port
}

func formatJSON(b []byte) (string, error) {
	var out bytes.Buffer
	if err := json.Indent(&out, b, "", "  "); err != nil {
		return "", err
	}
	return out.String(), nil
}

func (a *desktopApp) chooseConfigFile() {
	if a.configPath == nil {
		return
	}
	baseDir := appBaseDir()
	currentPath := strings.TrimSpace(a.configPath.Text())
	initialDir := filepath.Join(baseDir, "configs")
	if currentPath != "" {
		dir := filepath.Dir(currentPath)
		if dir != "." {
			if info, err := os.Stat(dir); err == nil && info.IsDir() {
				initialDir = dir
			}
		}
	}
	dlg := &walk.FileDialog{
		Title:          "选择配置文件",
		FilePath:       currentPath,
		InitialDirPath: initialDir,
		Filter:         "JSON 配置文件 (*.json)|*.json|所有文件 (*.*)|*.*",
		FilterIndex:    1,
	}
	accepted, err := dlg.ShowOpen(a.mw)
	if err != nil {
		a.fail("选择配置文件失败", err)
		return
	}
	if !accepted {
		return
	}
	a.configPath.SetText(dlg.FilePath)
	a.loadConfig()
}

func (a *desktopApp) loadConfig() {
	path := a.currentConfigPath()
	if err := a.loadConfigPath(path); err != nil {
		a.fail("读取配置失败", err)
	}
}

func (a *desktopApp) loadConfigSilently() {
	if a.configEdit == nil {
		return
	}
	path := a.currentConfigPath()
	if err := a.loadConfigPath(path); err != nil {
		a.configEdit.SetText("")
	}
}

func (a *desktopApp) loadConfigPath(path string) error {
	if path == "" {
		return fmt.Errorf("配置文件路径为空")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	text, err := formatJSON(b)
	if err != nil {
		return fmt.Errorf("配置 JSON 无效：%w", err)
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	a.setCurrentConfigPath(absPath)
	if a.configEdit != nil {
		a.configEdit.SetText(text)
	}
	if err := rememberConfigPath(appBaseDir(), absPath); err != nil {
		return err
	}
	a.info("配置已读取：\r\n" + absPath)
	return nil
}

func (a *desktopApp) saveConfig() {
	if a.configEdit == nil {
		a.fail("保存配置失败", fmt.Errorf("普通模式不支持直接编辑配置"))
		return
	}
	path := a.currentConfigPath()
	if path == "" {
		a.fail("保存配置失败", fmt.Errorf("配置文件路径为空"))
		return
	}
	var cfg config.Config
	if err := json.Unmarshal([]byte(a.configEdit.Text()), &cfg); err != nil {
		a.fail("配置 JSON 无效", err)
		return
	}
	if err := cfg.Validate(); err != nil {
		a.fail("配置校验失败", err)
		return
	}
	pretty, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		a.fail("格式化配置失败", err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		a.fail("创建目录失败", err)
		return
	}
	if err := os.WriteFile(path, append(pretty, '\n'), 0o600); err != nil {
		a.fail("保存配置失败", err)
		return
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		a.fail("保存配置失败", err)
		return
	}
	a.setCurrentConfigPath(absPath)
	a.configEdit.SetText(string(pretty))
	if err := rememberConfigPath(appBaseDir(), absPath); err != nil {
		a.fail("记住配置文件失败", err)
		return
	}
	a.info("配置已保存：\r\n" + absPath)
}

func (a *desktopApp) onboardingManager() onboarding.Manager {
	return onboarding.ManagerForConfig(appBaseDir(), a.currentConfigPath())
}

func (a *desktopApp) focusCreateServer() {
	if a.onboardingState != nil {
		a.onboardingState.SetText("状态：准备创建服务器")
	}
	if a.inviteServer != nil {
		_ = a.inviteServer.SetFocus()
	}
}

func (a *desktopApp) focusJoinNetwork() {
	if a.onboardingState != nil {
		a.onboardingState.SetText("状态：准备加入已有网络")
	}
	if a.inviteLink != nil {
		_ = a.inviteLink.SetFocus()
	}
}

func (a *desktopApp) showOfficialHubMVPDialog() {
	var hubURL, email, displayName, networkName, deviceName *walk.LineEdit
	var output *walk.TextEdit
	var dlg *walk.Dialog
	var closeButton *walk.PushButton
	var currentInvite cloudhub.InviteResult

	state, _ := a.onboardingManager().OfficialHubState()
	defaultHubURL := fallbackText(state.HubAPIURL, state.SuggestedHubAPIURL, onboarding.DefaultOfficialHubAPIURL)
	defaultEmail := state.AccountEmail
	defaultDisplayName := state.AccountName
	defaultNetworkName := fallbackText(state.NetworkName, "我的官方网络")
	defaultDeviceName := fallbackText(state.LocalDeviceName, defaultNodeName("meshlink-device"))

	setOutput := func(text string) {
		if output != nil {
			output.SetText(text)
		}
	}
	account := func() {
		setOutput("正在创建官方 Hub MVP/内测测试账号...")
		result, err := a.onboardingManager().CreateOfficialHubAccount(context.Background(), onboarding.OfficialHubAccountRequest{
			HubAPIURL:   strings.TrimSpace(hubURL.Text()),
			Email:       strings.TrimSpace(email.Text()),
			DisplayName: strings.TrimSpace(displayName.Text()),
		})
		if err != nil {
			setOutput("创建测试账号失败：\r\n" + err.Error())
			return
		}
		setOutput("创建测试账号完成。\r\n\r\n" + formatOfficialHubState(result.State))
	}
	network := func() {
		setOutput("正在创建官方网络...")
		result, err := a.onboardingManager().CreateOfficialHubNetwork(context.Background(), onboarding.OfficialHubNetworkRequest{
			HubAPIURL: strings.TrimSpace(hubURL.Text()),
			Name:      strings.TrimSpace(networkName.Text()),
		})
		if err != nil {
			setOutput("创建官方网络失败：\r\n" + err.Error())
			return
		}
		setOutput("创建官方网络完成。\r\n\r\n" + formatOfficialHubState(result.State))
	}
	invite := func() {
		setOutput("正在生成官方邀请...")
		result, err := a.onboardingManager().CreateOfficialHubInvite(context.Background(), onboarding.OfficialHubInviteRequest{
			HubAPIURL: strings.TrimSpace(hubURL.Text()),
			MaxUses:   2,
			OneTime:   false,
		})
		if err != nil {
			setOutput("生成官方邀请失败：\r\n" + err.Error())
			return
		}
		currentInvite = result.Invite
		setOutput("生成官方邀请完成。\r\n\r\n" + formatOfficialHubInvite(result.Invite) + "\r\n\r\n" + formatOfficialHubState(result.State))
	}
	join := func() {
		if currentInvite.Token == "" || currentInvite.Code == "" {
			setOutput("请先生成官方邀请，再加入当前设备。")
			return
		}
		setOutput("正在加入当前设备...")
		result, err := a.onboardingManager().JoinOfficialHubDevice(context.Background(), onboarding.OfficialHubJoinDeviceRequest{
			HubAPIURL:  strings.TrimSpace(hubURL.Text()),
			Token:      currentInvite.Token,
			Code:       currentInvite.Code,
			DeviceName: strings.TrimSpace(deviceName.Text()),
		})
		if err != nil {
			setOutput("加入当前设备失败：\r\n" + err.Error())
			return
		}
		setOutput("当前设备已加入官方网络。\r\n\r\n" + formatOfficialHubDevice(result.Device) + "\r\n\r\n" + formatOfficialHubState(result.State))
	}
	heartbeat := func() {
		setOutput("正在发送 heartbeat...")
		result, err := a.onboardingManager().HeartbeatOfficialHubDevice(context.Background(), onboarding.OfficialHubHeartbeatRequest{
			HubAPIURL: strings.TrimSpace(hubURL.Text()),
			Status:    cloudhub.DeviceStatusOnline,
		})
		if err != nil {
			setOutput("发送 heartbeat 失败：\r\n" + err.Error())
			return
		}
		setOutput("heartbeat 已发送。\r\n\r\n" + formatOfficialHubDevice(result.Device) + "\r\n\r\n" + formatOfficialHubState(result.State))
	}
	refresh := func() {
		setOutput("正在刷新设备列表...")
		result, err := a.onboardingManager().ListOfficialHubDevices(context.Background(), onboarding.OfficialHubDevicesRequest{
			HubAPIURL: strings.TrimSpace(hubURL.Text()),
		})
		if err != nil {
			setOutput("刷新设备列表失败：\r\n" + err.Error())
			return
		}
		setOutput(formatOfficialHubDevices(result.Devices))
	}
	subscription := func() {
		setOutput("正在读取套餐和额度状态...")
		result, err := a.onboardingManager().OfficialHubSubscriptionExperience(context.Background(), onboarding.OfficialHubSubscriptionExperienceRequest{
			HubAPIURL: strings.TrimSpace(hubURL.Text()),
		})
		if err != nil {
			setOutput("读取套餐和额度状态失败：\r\n" + err.Error())
			return
		}
		setOutput(formatOfficialHubSubscriptionExperience(result))
	}

	if err := (Dialog{
		AssignTo:  &dlg,
		Title:     "官方 Hub MVP/内测",
		MinSize:   Size{Width: 720, Height: 560},
		Size:      Size{Width: 820, Height: 640},
		FixedSize: false,
		Layout:    VBox{Margins: Margins{Left: 14, Top: 14, Right: 14, Bottom: 14}, Spacing: 10},
		Children: []Widget{
			Label{Text: "当前只是官方 Hub 控制面 MVP，不代表 Relay/P2P 已完成。尚未接入真实订阅和 Relay 数据面。"},
			GroupBox{
				Title:  "官方 Hub MVP/内测",
				Layout: Grid{Columns: 4, Margins: Margins{Left: 10, Top: 18, Right: 10, Bottom: 10}, Spacing: 7},
				Children: []Widget{
					Label{Text: "Hub API 地址"},
					LineEdit{AssignTo: &hubURL, Text: defaultHubURL, CueBanner: "http://127.0.0.1:18080", ColumnSpan: 3},
					Label{Text: "账号邮箱"},
					LineEdit{AssignTo: &email, Text: defaultEmail, CueBanner: "owner@example.com", ColumnSpan: 3},
					Label{Text: "账号名称"},
					LineEdit{AssignTo: &displayName, Text: defaultDisplayName, CueBanner: "我的账号", ColumnSpan: 3},
					Label{Text: "网络名称"},
					LineEdit{AssignTo: &networkName, Text: defaultNetworkName, ColumnSpan: 3},
					Label{Text: "本机设备名称"},
					LineEdit{AssignTo: &deviceName, Text: defaultDeviceName, ColumnSpan: 3},
					PushButton{Text: "创建测试账号", OnClicked: account, ColumnSpan: 2},
					PushButton{Text: "创建官方网络", OnClicked: network, ColumnSpan: 2},
					PushButton{Text: "生成官方邀请", OnClicked: invite, ColumnSpan: 2},
					PushButton{Text: "加入当前设备", OnClicked: join, ColumnSpan: 2},
					PushButton{Text: "发送 heartbeat", OnClicked: heartbeat, ColumnSpan: 2},
					PushButton{Text: "刷新设备列表", OnClicked: refresh, ColumnSpan: 2},
					PushButton{Text: "查看套餐/了解升级", OnClicked: subscription, ColumnSpan: 2},
				},
			},
			GroupBox{
				Title:  "账号 / 网络 / 设备状态",
				Layout: VBox{Margins: Margins{Left: 10, Top: 18, Right: 10, Bottom: 10}, Spacing: 7},
				Children: []Widget{
					TextEdit{AssignTo: &output, ReadOnly: true, Font: monoFont(), VScroll: true, MinSize: Size{Width: 0, Height: 220}},
				},
			},
			Composite{
				Layout: HBox{MarginsZero: true, Spacing: 8},
				Children: []Widget{
					HSpacer{},
					PushButton{AssignTo: &closeButton, Text: "关闭", OnClicked: func() { dlg.Accept() }},
				},
			},
		},
		DefaultButton: &closeButton,
		CancelButton:  &closeButton,
	}).Create(a.mw); err != nil {
		a.fail("打开官方 Hub MVP/内测失败", err)
		return
	}
	output.SetText(formatOfficialHubState(state))
	dlg.Run()
}

func (a *desktopApp) createHubOnboarding() {
	if a.connecting {
		return
	}
	if strings.TrimSpace(a.inviteServer.Text()) == "" {
		a.fail("启动服务器失败", fmt.Errorf("请填写其他设备能够访问的域名或公网地址"))
		return
	}
	port, err := strconv.Atoi(strings.TrimSpace(a.listenPort.Text()))
	if err != nil || port <= 0 || port > 65535 {
		a.fail("启动服务器失败", fmt.Errorf("监听端口无效"))
		return
	}
	manager, err := onboarding.SelectRole(appBaseDir(), "hub")
	if err != nil {
		a.fail("启动服务器失败", err)
		return
	}
	req := onboarding.StartServerRequest{
		ServerAddress: strings.TrimSpace(a.inviteServer.Text()),
		ListenPort:    port,
		LongLived:     true,
		MaxUses:       a.inviteMaxUsesValue(),
	}
	name := a.currentServiceName()
	a.connecting = true
	a.mw.SetEnabled(false)
	a.onboardingState.SetText("状态：正在启动服务器…")
	go func() {
		result, err := startDesktopServer(manager, req, name)
		a.mw.Synchronize(func() {
			a.connecting = false
			a.mw.SetEnabled(true)
			if result.ConfigPath != "" {
				a.setCurrentConfigPath(result.ConfigPath)
				a.showInvite(result.Invite)
			}
			a.loadMeshStatus()
			if err != nil {
				a.fail("启动服务器失败", err)
				return
			}
			a.inviteServer.SetText(result.Invite.Server)
			a.onboardingState.SetText("状态：服务器运行中")
			a.info("服务器已启动。\r\n本机地址：" + result.VirtualIP + "\r\n监听地址：" + result.Listen + "\r\n\r\n" + formatInviteForDesktop(result.Invite))
		})
	}()
}

func startDesktopServer(manager onboarding.Manager, req onboarding.StartServerRequest, name string) (onboarding.StartServerResult, error) {
	previous, err := winservice.Status(name)
	if err != nil {
		return onboarding.StartServerResult{}, err
	}
	wasRunning := previous.Installed && shouldStopServiceBeforeStart(previous.State)
	if wasRunning {
		if err := winservice.Stop(name); err != nil && !isServiceNotRunningError(err) {
			return onboarding.StartServerResult{}, err
		}
	}
	result, err := manager.StartServerMode(req)
	if err != nil {
		if wasRunning {
			_ = winservice.Start(name)
		}
		return result, err
	}
	return result, installAndStartAgent(name, result.ConfigPath)
}

func (a *desktopApp) createInviteOnboarding() {
	server, err := a.desktopInviteServer()
	if err != nil {
		a.fail("重新生成接入码失败", err)
		return
	}
	invite, err := a.onboardingManager().CreateServerInvite(onboarding.CreateInviteRequest{
		Server:          server,
		Protocol:        "tcp_tls_v1",
		LongLived:       true,
		MaxUses:         a.inviteMaxUsesValue(),
		ReplaceExisting: true,
	})
	if err != nil {
		a.fail("重新生成接入码失败", err)
		return
	}
	a.showInvite(invite)
	a.onboardingState.SetText("状态：接入码已更新")
	a.info(formatInviteForDesktop(invite) + "\r\n\r\n旧接入链接已失效，已连接的设备继续使用原身份。")
}

func (a *desktopApp) showInvite(invite onboarding.CreateInviteResult) {
	if a.inviteOutput != nil {
		a.inviteOutput.SetText(formatInviteForDesktop(invite))
	}
}

func formatInviteForDesktop(invite onboarding.CreateInviteResult) string {
	return formatInviteForDesktopAt(invite, time.Now())
}

func formatInviteForDesktopAt(invite onboarding.CreateInviteResult, now time.Time) string {
	expiry := formatTime(invite.ExpiresAt)
	if invite.LongLived {
		expiry = "长期有效"
	} else if !invite.ExpiresAt.IsZero() && now.After(invite.ExpiresAt) {
		expiry += "（已过期，请重新生成）"
	}
	code := strings.TrimSpace(invite.Code)
	if code == "" {
		code = "未保存，请重新生成接入码"
	}
	return "服务器地址：" + invite.Server +
		"\r\n接入码：" + code +
		"\r\n有效期：" + expiry +
		formatInviteLimitForDesktop(invite) +
		"\r\n接入链接：\r\n" + invite.Link
}

func formatInviteLimitForDesktop(invite onboarding.CreateInviteResult) string {
	if !invite.LongLived {
		return ""
	}
	limit := invite.MaxUses
	if limit == -1 {
		return "\r\n可供多台设备连接；重新生成后旧接入码失效。"
	}
	if limit <= 0 {
		limit = 3
	}
	return "\r\n设备数限制：" + strconv.Itoa(limit) + " 台" +
		"\r\n长期接入码风险较高，请仅发给可信设备；达到设备数限制后会拒绝新设备加入。"
}

func formatOfficialHubState(state onboarding.OfficialHubState) string {
	var b strings.Builder
	b.WriteString("Hub API 地址：")
	b.WriteString(fallbackText(state.HubAPIURL, state.SuggestedHubAPIURL, onboarding.DefaultOfficialHubAPIURL))
	b.WriteString("\r\n账号：")
	b.WriteString(fallbackText(state.AccountEmail, state.AccountID, "-"))
	if state.AccountName != "" {
		b.WriteString("（")
		b.WriteString(state.AccountName)
		b.WriteString("）")
	}
	b.WriteString("\r\n网络：")
	b.WriteString(fallbackText(state.NetworkName, state.NetworkID, "-"))
	b.WriteString("\r\n当前设备：")
	b.WriteString(fallbackText(state.LocalDeviceName, state.DeviceID, "-"))
	if state.LastInvite != nil {
		b.WriteString("\r\n最近邀请：")
		b.WriteString(fallbackText(state.LastInvite.ID, "-"))
		if !state.LastInvite.ExpiresAt.IsZero() {
			b.WriteString("，有效期至 ")
			b.WriteString(formatTime(state.LastInvite.ExpiresAt))
		}
		if state.LastInvite.MaxUses > 0 {
			b.WriteString("，设备数限制 ")
			b.WriteString(strconv.Itoa(state.LastInvite.MaxUses))
			b.WriteString(" 台")
		}
	}
	if !state.UpdatedAt.IsZero() {
		b.WriteString("\r\n更新时间：")
		b.WriteString(formatTime(state.UpdatedAt))
	}
	return b.String()
}

func formatOfficialHubInvite(invite cloudhub.InviteResult) string {
	var b strings.Builder
	b.WriteString("官方邀请接入码：")
	b.WriteString(fallbackText(invite.Code, "------"))
	b.WriteString("\r\n有效期：")
	b.WriteString(formatTime(invite.ExpiresAt))
	if invite.MaxUses > 0 {
		b.WriteString("\r\n设备数限制：")
		b.WriteString(strconv.Itoa(invite.MaxUses))
		b.WriteString(" 台")
	}
	b.WriteString("\r\n请在本窗口继续点击“加入当前设备”。")
	return b.String()
}

func formatOfficialHubDevice(device cloudhub.Device) string {
	var b strings.Builder
	b.WriteString("设备名称：")
	b.WriteString(fallbackText(device.Name, "-"))
	b.WriteString("\r\n设备状态：")
	b.WriteString(string(device.Status))
	b.WriteString("\r\n设备 ID：")
	b.WriteString(fallbackText(device.ID, "-"))
	if device.LastSeen != nil {
		b.WriteString("\r\n最近在线：")
		b.WriteString(formatTime(*device.LastSeen))
	}
	return b.String()
}

func formatOfficialHubDevices(devices []cloudhub.Device) string {
	if len(devices) == 0 {
		return "官方设备列表：暂无设备"
	}
	var b strings.Builder
	b.WriteString("官方设备列表：")
	for i, device := range devices {
		b.WriteString("\r\n")
		b.WriteString(strconv.Itoa(i + 1))
		b.WriteString(". ")
		b.WriteString(fallbackText(device.Name, device.ID, "未命名设备"))
		b.WriteString("  ")
		b.WriteString(string(device.Status))
		if device.LastSeen != nil {
			b.WriteString("  最近在线 ")
			b.WriteString(formatTime(*device.LastSeen))
		}
	}
	return b.String()
}

func formatOfficialHubSubscriptionExperience(experience onboarding.OfficialHubSubscriptionExperience) string {
	var b strings.Builder
	b.WriteString("当前套餐：")
	b.WriteString(fallbackText(experience.CurrentPlan.DisplayName, string(experience.CurrentPlan.ID), "Free"))
	b.WriteString("\r\n订阅状态：")
	b.WriteString(fallbackText(experience.Subscription.Label, "-"))
	if experience.Subscription.Message != "" {
		b.WriteString("\r\n说明：")
		b.WriteString(experience.Subscription.Message)
	}
	b.WriteString("\r\n\r\n关键额度：")
	for _, quota := range experience.Quotas {
		b.WriteString("\r\n- ")
		b.WriteString(quota.Label)
		b.WriteString("：已用 ")
		b.WriteString(quota.UsedText)
		b.WriteString(" / 上限 ")
		b.WriteString(quota.LimitText)
		b.WriteString(" / 剩余 ")
		b.WriteString(quota.RemainingText)
	}
	for _, issue := range experience.QuotaIssues {
		b.WriteString("\r\n\r\n额度提醒：")
		b.WriteString(issue.Label)
		b.WriteString(" 已受限。")
		if issue.Impact != "" {
			b.WriteString("\r\n影响：")
			b.WriteString(issue.Impact)
		}
	}
	if experience.RelayUsageReminder != nil {
		b.WriteString("\r\n\r\n")
		b.WriteString(experience.RelayUsageReminder.Title)
		b.WriteString("：")
		b.WriteString(experience.RelayUsageReminder.Message)
	}
	b.WriteString("\r\n\r\n查看套餐/了解升级：本窗口仅展示本地套餐比较，不跳转外部页面，也不承诺购买结果。")
	b.WriteString("\r\n")
	b.WriteString(fallbackText(experience.SelfHostedContinuity, "免费自建服务器、自建 Relay 和基础设备互联可继续使用。"))
	return b.String()
}

type simpleModeSnapshot struct {
	Mode       string
	NodeName   string
	Server     string
	ListenPort string
	InviteText string
}

func loadSimpleModeSnapshot(baseDir, configPath string, now time.Time) (simpleModeSnapshot, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return simpleModeSnapshot{}, err
	}
	snapshot := simpleModeSnapshot{Mode: cfg.Mode, NodeName: fallbackText(cfg.DisplayName, cfg.NodeID)}
	switch cfg.Mode {
	case "hub":
		if _, port, err := net.SplitHostPort(cfg.Listen); err == nil {
			snapshot.ListenPort = port
		}
	case "spoke":
		snapshot.Server = cfg.Connect
	}
	invite, ok, err := onboarding.ManagerForConfig(baseDir, configPath).LatestInvite()
	if err != nil {
		return snapshot, err
	}
	if ok {
		if invite.Server != "" {
			snapshot.Server = invite.Server
		}
		snapshot.InviteText = formatInviteForDesktopAt(invite, now)
	}
	return snapshot, nil
}

func (a *desktopApp) restoreSimpleModeState() {
	baseDir := appBaseDir()
	configPath := a.currentConfigPath()
	a.setCurrentConfigPath(configPath)
	snapshot, err := loadSimpleModeSnapshot(baseDir, configPath, time.Now())
	if err != nil {
		return
	}
	switch snapshot.Mode {
	case "hub":
		if a.inviteServer != nil && snapshot.Server != "" {
			a.inviteServer.SetText(snapshot.Server)
		}
		if a.listenPort != nil && snapshot.ListenPort != "" {
			a.listenPort.SetText(snapshot.ListenPort)
		}
		if a.inviteOutput != nil && snapshot.InviteText != "" {
			a.inviteOutput.SetText(snapshot.InviteText)
		}
		if a.onboardingState != nil {
			a.onboardingState.SetText("状态：已读取当前服务器配置")
		}
	case "spoke":
		if a.spokeNodeName != nil {
			a.spokeNodeName.SetText(snapshot.NodeName)
		}
		if a.onboardingState != nil {
			a.onboardingState.SetText("状态：已读取当前客户端配置")
		}
	}
}

func (a *desktopApp) desktopInviteServer() (string, error) {
	server := strings.TrimSpace(a.inviteServer.Text())
	if server == "" {
		return "", fmt.Errorf("域名或公网地址为空")
	}
	if strings.Contains(server, "://") || strings.Contains(server, ":") {
		return server, nil
	}
	port := strings.TrimSpace(a.listenPort.Text())
	if port == "" {
		port = "8443"
	}
	return server + ":" + port, nil
}

func (a *desktopApp) inviteMaxUsesValue() int {
	if a.inviteMaxUses == nil {
		return -1
	}
	maxUses, err := strconv.Atoi(strings.TrimSpace(a.inviteMaxUses.Text()))
	if err != nil || maxUses <= 0 {
		return 3
	}
	return maxUses
}

func (a *desktopApp) connectNetwork() {
	if a.connecting {
		return
	}
	if a.onboardingManager().IsOwnServerInvite(strings.TrimSpace(a.inviteLink.Text())) {
		invite, _ := onboarding.ParseInviteLink(strings.TrimSpace(a.inviteLink.Text()))
		a.inviteServer.SetText(invite.Server)
		a.createHubOnboarding()
		return
	}
	manager, err := onboarding.SelectRole(appBaseDir(), "spoke")
	if err != nil {
		a.fail("连接失败", err)
		return
	}
	name := a.currentServiceName()
	req := onboarding.JoinSpokeRequest{
		InviteLink: strings.TrimSpace(a.inviteLink.Text()),
		Code:       strings.TrimSpace(a.inviteCode.Text()),
		NodeName:   strings.TrimSpace(a.spokeNodeName.Text()),
	}
	a.connecting = true
	a.mw.SetEnabled(false)
	a.onboardingState.SetText("状态：正在连接…")
	go func() {
		installed, _ := winservice.Status(name)
		result, err := connectSpoke(manager, req, func(path string) error { return installAndStartAgent(name, path) }, installed.ConfigPath)
		a.mw.Synchronize(func() {
			a.connecting = false
			a.mw.SetEnabled(true)
			if result.ConfigPath != "" {
				a.setCurrentConfigPath(result.ConfigPath)
				if cfg, loadErr := config.Load(result.ConfigPath); loadErr == nil {
					a.spokeNodeName.SetText(fallbackText(cfg.DisplayName, cfg.NodeID))
				}
			}
			a.loadMeshStatus()
			if err != nil {
				a.onboardingState.SetText("状态：未连接")
				a.fail("连接失败", err)
				return
			}
			a.onboardingState.SetText("状态：已连接")
			a.info("已连接服务器。\r\n本机地址：" + result.VirtualIP + "\r\n服务器：" + result.Server)
		})
	}()
}

// Enrollment (or identity reuse) must precede service installation, including
// when an unrelated Meshlink service is still registered on this machine.
func connectSpoke(manager onboarding.Manager, req onboarding.JoinSpokeRequest, start func(string) error, installedConfig ...string) (onboarding.JoinSpokeResult, error) {
	if len(installedConfig) > 0 {
		imported, err := manager.ImportInstalledSpoke(installedConfig[0], req.InviteLink)
		if err != nil {
			return onboarding.JoinSpokeResult{}, err
		}
		if imported {
			req.NodeName = ""
		}
	}
	result, err := manager.JoinSpoke(req)
	if err != nil {
		return result, err
	}
	return result, start(result.ConfigPath)
}

func (a *desktopApp) installAndStartAgent(configPath string) error {
	if strings.TrimSpace(configPath) == "" {
		configPath = a.currentConfigPath()
	}
	absPath, err := filepath.Abs(configPath)
	if err != nil {
		return err
	}
	a.setCurrentConfigPath(absPath)
	return installAndStartAgent(a.currentServiceName(), absPath)
}

func installAndStartAgent(name, absPath string) error {
	if _, err := config.Load(absPath); err != nil {
		return fmt.Errorf("客户端配置不可用：%w", err)
	}
	if status, err := winservice.Status(name); err == nil && status.Installed && shouldStopServiceBeforeStart(status.State) {
		if err := winservice.Stop(name); err != nil && !isServiceNotRunningError(err) {
			return fmt.Errorf("停止旧服务失败：%w", err)
		}
	}
	if err := installAgentService(name, absPath); err != nil {
		return err
	}
	if err := os.Remove(runner.StatusPath(absPath, name)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("清理旧连接状态失败：%w", err)
	}
	startedAt := time.Now()
	if err := winservice.Start(name); err != nil && !isServiceAlreadyRunningError(err) {
		return err
	}
	return waitForAgentRunningAfterStart(absPath, name, startedAt, 20*time.Second)
}

func (a *desktopApp) disconnectNetwork() {
	if err := winservice.Stop(a.currentServiceName()); err != nil && !isServiceNotRunningError(err) {
		a.fail("断开连接失败", err)
		return
	}
	a.onboardingState.SetText("状态：已断开")
	a.loadMeshStatus()
}

func (a *desktopApp) exitNetwork() {
	message := "确定退出当前网络？\r\n\r\n退出后需要重新使用邀请码才能加入。"
	if walk.MsgBox(a.mw, "退出网络", message, walk.MsgBoxYesNo|walk.MsgBoxIconWarning) != walk.DlgCmdYes {
		return
	}
	settings := loadDesktopSettings(appBaseDir())
	if err := completeDesktopLeave(
		settings,
		func(settings desktopSettings) error { return saveDesktopSettings(appBaseDir(), settings) },
		func() error { return networklifecycle.Leave(a.onboardingManager(), a.currentServiceName()) },
	); err != nil {
		a.fail("退出网络失败", err)
		return
	}
	a.clearCurrentNetworkUI()
	a.loadMeshStatus()
}

func completeDesktopLeave(settings desktopSettings, persist func(desktopSettings) error, leave func() error) error {
	settings.LastConfigPath = ""
	if err := persist(settings); err != nil {
		return fmt.Errorf("清理网络记录: %w", err)
	}
	return leave()
}

func (a *desktopApp) clearCurrentNetworkUI() {
	a.coordinatorState = "disconnected"
	a.p2pListen = ""
	if a.configPath != nil {
		a.configPath.SetText(defaultConfigPath(appBaseDir()))
	}
	if a.meshList != nil {
		_ = a.meshList.SetCurrentIndex(-1)
	}
	if a.meshModel != nil {
		a.meshModel.SetItems(nil)
	}
	a.meshSelected = ""
	if a.meshDetail != nil {
		a.meshDetail.SetText("当前没有可显示的节点。")
	}
	if a.meshSummary != nil {
		a.meshSummary.SetText("未加入 · 在线 0 台，离线 0 台")
	}
	if a.onboardingState != nil {
		a.onboardingState.SetText("状态：未加入")
	}
	if a.quickState != nil {
		a.quickState.SetText("未加入")
	}
	a.updateConnectivityOverview(nil, "")
}

func shouldStopServiceBeforeStart(state string) bool {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "", "stopped":
		return false
	default:
		return true
	}
}

func isServiceAlreadyRunningError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "already") || strings.Contains(text, "已启动") || strings.Contains(text, "正在运行")
}

func isServiceNotRunningError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "not been started") ||
		strings.Contains(text, "not started") ||
		strings.Contains(text, "service is not active") ||
		strings.Contains(text, "未启动") ||
		strings.Contains(text, "没有启动")
}

func waitForAgentRunningAfterStart(configPath, serviceName string, startedAt time.Time, timeout time.Duration) error {
	if serviceName == "" {
		serviceName = winservice.DefaultName
	}
	statusPath := runner.StatusPath(configPath, serviceName)
	cfg, _ := config.Load(configPath)
	deadline := time.Now().Add(timeout)
	var lastErr error
	var sawStopped bool
	for {
		status, err := readRuntimeStatusFile(statusPath)
		if err != nil {
			lastErr = err
		} else if statusIsFromThisStart(status.UpdatedAt, startedAt) {
			lastErr = nil
			switch strings.ToLower(status.State) {
			case "running":
				if status.NetworkState == networkstate.Connected && (status.Self.Mode != "spoke" || status.CoordinatorState == networkstate.Connected) {
					if cfg == nil || cfg.ServerNodeConfig == "" {
						return nil
					}
					host, hostErr := readRuntimeStatusFile(statusPath + ".server-node.json")
					if hostErr == nil && statusIsFromThisStart(host.UpdatedAt, startedAt) && host.State == "running" && host.CoordinatorState == networkstate.Connected && host.NetworkState == networkstate.Connected {
						return nil
					}
				}
			case "stopped":
				sawStopped = true
			}
		}
		if time.Now().After(deadline) {
			if sawStopped {
				return fmt.Errorf("服务启动后立即停止：%s", agentLogTail(configPath, serviceName))
			}
			if lastErr != nil {
				return fmt.Errorf("等待服务运行状态超时，无法读取状态文件 %s：%w", statusPath, lastErr)
			}
			return fmt.Errorf("未连接服务器：%s", agentLogTail(configPath, serviceName))
		}
		time.Sleep(300 * time.Millisecond)
	}
}

func readRuntimeStatusFile(path string) (meshagent.RuntimeStatus, error) {
	var status meshagent.RuntimeStatus
	b, err := os.ReadFile(path)
	if err != nil {
		return status, err
	}
	if err := json.Unmarshal(b, &status); err != nil {
		return status, err
	}
	return status, nil
}

func statusIsFromThisStart(updatedAt, startedAt time.Time) bool {
	return !updatedAt.IsZero() && !updatedAt.Before(startedAt)
}

func agentLogTail(configPath, serviceName string) string {
	if serviceName == "" {
		serviceName = winservice.DefaultName
	}
	logPath := filepath.Join(filepath.Dir(configPath), "logs", serviceName+".log")
	text, err := tail(logPath, 8192)
	if err != nil || strings.TrimSpace(text) == "" {
		return "请查看日志：" + logPath
	}
	lines := strings.Split(strings.TrimSpace(text), "\n")
	// The final failure is useful; old startup records from other roles are not.
	for i := len(lines) - 1; i >= 0; i-- {
		lower := strings.ToLower(lines[i])
		if strings.Contains(lower, "error") || strings.Contains(lower, "failed") {
			return strings.TrimSpace(lines[i])
		}
	}
	if len(lines) > 3 {
		lines = lines[len(lines)-3:]
	}
	return strings.Join(lines, "\n")
}

func (a *desktopApp) serviceAction(action string) {
	name := a.currentServiceName()
	configPath := a.currentConfigPath()
	var err error
	switch action {
	case "install":
		err = installAgentService(name, configPath)
	case "start":
		err = a.installAndStartAgent(configPath)
	case "stop":
		err = winservice.Stop(name)
	case "uninstall":
		err = winservice.Uninstall(name)
	}
	if err != nil {
		a.fail("服务操作失败", err)
		return
	}
	a.info("服务操作完成：" + actionCN(action))
	a.serviceStatus()
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
	return "", fmt.Errorf("找不到 mesh-agent.exe，请确认它和 mesh-desktop.exe 在同一目录，或位于当前目录的 bin 目录下")
}

func (a *desktopApp) serviceStatus() {
	text := a.refreshServiceStatusLabels()
	a.info(text)
}

func (a *desktopApp) refreshServiceStatusLabels() string {
	status, err := winservice.Status(a.currentServiceName())
	if err != nil {
		return "服务状态：读取失败"
	}
	if status.ConfigPath != "" {
		a.setCurrentConfigPath(status.ConfigPath)
	}
	text := "服务状态：未安装"
	if status.Installed {
		text = "服务状态：" + serviceStateCN(status.State)
	}
	if a.serviceState != nil {
		a.serviceState.SetText(text)
	}
	return text
}

func (a *desktopApp) createCA() {
	if a.certOut == nil || a.caName == nil {
		a.fail("创建 CA 失败", fmt.Errorf("普通模式不支持手动创建 CA"))
		return
	}
	result, err := certutil.InitCA(certutil.CAOptions{
		OutDir: strings.TrimSpace(a.certOut.Text()),
		Name:   strings.TrimSpace(a.caName.Text()),
		Days:   3650,
	})
	if err != nil {
		a.fail("创建 CA 失败", err)
		return
	}
	a.info("CA 已创建：\r\n" + strings.Join(result.Files, "\r\n"))
}

func (a *desktopApp) issueCert() {
	if a.certOut == nil || a.nodeName == nil || a.dnsSANs == nil || a.ipSANs == nil || a.certDays == nil {
		a.fail("签发证书失败", fmt.Errorf("普通模式不支持手动签发节点证书"))
		return
	}
	days, _ := strconv.Atoi(strings.TrimSpace(a.certDays.Text()))
	out := strings.TrimSpace(a.certOut.Text())
	result, err := certutil.Issue(certutil.IssueOptions{
		OutDir:    out,
		Name:      strings.TrimSpace(a.nodeName.Text()),
		CAPath:    filepath.Join(out, "ca.pem"),
		CAKeyPath: filepath.Join(out, "ca-key.pem"),
		DNSNames:  certutil.SplitCSV(a.dnsSANs.Text()),
		IPAddrs:   certutil.SplitCSV(a.ipSANs.Text()),
		Days:      days,
	})
	if err != nil {
		a.fail("签发证书失败", err)
		return
	}
	a.info("节点证书已签发：\r\n" + strings.Join(result.Files, "\r\n"))
}

func (a *desktopApp) runDiagnostics() {
	report := diagnose.Run(a.currentConfigPath(), a.currentServiceName())
	a.info(formatReport(report))
	if hasFail(report.Checks) {
		a.quickState.SetText("需要处理")
		return
	}
	a.quickState.SetText("诊断正常")
}

func (a *desktopApp) runOneClickDiagnostics() {
	node, ok := a.currentMeshNode()
	req := diagnose.OneClickRequest{
		ConfigPath:   a.currentConfigPath(),
		ServiceName:  a.currentServiceName(),
		IncludeRDP:   ok && node.Kind != "self" && strings.TrimSpace(node.VirtualIP) != "",
		NetworkState: string(a.networkState),
	}
	if ok {
		req.Target = strings.TrimSpace(node.VirtualIP)
		req.TargetDevice = fallbackText(node.DisplayName, node.NodeID)
		req.TargetStatus = node.State
		req.ConnectionStatus = node.ConnectionStatus
	}
	report := diagnose.RunOneClick(req)
	a.info(formatOneClickReport(report))
	if report.Status == diagnose.Fail {
		a.quickState.SetText("需要处理")
		return
	}
	a.quickState.SetText("诊断完成")
}

func (a *desktopApp) loadLogs() {
	configPath := a.currentConfigPath()
	serviceName := a.currentServiceName()
	if serviceName == "" {
		serviceName = winservice.DefaultName
	}
	logPath := filepath.Join(filepath.Dir(configPath), "logs", serviceName+".log")
	text, err := tail(logPath, 128*1024)
	if err != nil {
		a.fail("读取日志失败", err)
		return
	}
	if text == "" {
		text = "日志为空：" + logPath
	}
	a.info(text)
}

func (a *desktopApp) loadMeshStatus() {
	if a.meshModel == nil || a.meshList == nil || a.meshDetail == nil || a.meshSummary == nil {
		return
	}
	configPath := a.currentConfigPath()
	serviceName := a.currentServiceName()
	statusPath := runner.StatusPath(configPath, serviceName)
	selectedKey := a.currentMeshNodeKey()
	devices, devicesErr := a.onboardingManager().Devices(serviceName)
	if devicesErr == nil {
		service, err := winservice.Status(serviceName)
		devices = onboarding.WithServiceRunning(devices, err == nil && service.Installed && service.State == "running")
		a.networkState = devices.NetworkState
		a.coordinatorState = devices.CoordinatorState
		a.p2pListen = devices.P2PListen
		nodes := buildMeshNodesFromDevices(devices, statusPath)
		a.meshModel.SetItems(nodes)
		a.updateConnectivityOverview(nodes, selectedKey)
		networkLabel := desktopNetworkStateText(devices.NetworkState)
		if devices.CoordinatorState == "serving" {
			networkLabel = "服务器运行中"
		}
		a.meshSummary.SetText(networkLabel + " · " + meshSummaryText(nodes, devices.UpdatedAt))
		if a.quickState != nil {
			a.quickState.SetText(networkLabel)
		}
		if a.onboardingState != nil && !a.connecting {
			a.onboardingState.SetText("状态：" + networkLabel)
		}
		index := findMeshNodeIndex(nodes, selectedKey)
		if index < 0 && len(nodes) > 0 {
			index = 0
		}
		if index >= 0 {
			_ = a.meshList.SetCurrentIndex(index)
			a.showSelectedMeshNode()
		} else {
			_ = a.meshList.SetCurrentIndex(-1)
			a.meshSelected = ""
			a.meshDetail.SetText("当前没有可显示的节点。")
		}
		return
	}
	networkState, nodes := conservativeDesktopDeviceSnapshot(a.meshModel.items)
	a.networkState = networkState
	a.coordinatorState = "disconnected"
	a.p2pListen = ""
	a.meshModel.SetItems(nodes)
	a.updateConnectivityOverview(nodes, selectedKey)
	networkLabel := desktopNetworkStateText(a.networkState)
	a.meshSummary.SetText(networkLabel + " · " + meshSummaryText(nodes, time.Time{}))
	if a.quickState != nil {
		a.quickState.SetText(networkLabel)
	}
	index := findMeshNodeIndex(nodes, selectedKey)
	if index < 0 && len(nodes) > 0 {
		index = 0
	}
	if index >= 0 {
		_ = a.meshList.SetCurrentIndex(index)
		a.showSelectedMeshNode()
	} else {
		_ = a.meshList.SetCurrentIndex(-1)
		a.meshSelected = ""
		a.meshDetail.SetText("设备状态刷新失败，当前按已断开处理：\r\n" + devicesErr.Error())
	}
}

func (a *desktopApp) showSelectedMeshNode() {
	if a.meshList == nil || a.meshModel == nil || a.meshDetail == nil {
		return
	}
	index := a.meshList.CurrentIndex()
	if index < 0 || index >= len(a.meshModel.items) {
		a.meshDetail.SetText("请选择左侧节点。")
		return
	}
	node := a.meshModel.items[index]
	a.meshDetail.SetText(formatMeshNodeDetail(node))
	a.updateConnectivityOverview(a.meshModel.items, node.Key)
	a.fillRDPTargetFromMeshNode(node)
}

func (a *desktopApp) updateConnectivityOverview(nodes []meshNode, selectedKey string) {
	if a.coordinatorSummary != nil {
		a.coordinatorSummary.SetText(desktopCoordinatorStateText(a.coordinatorState))
	}
	if a.p2pPathSummary != nil {
		a.p2pPathSummary.SetText(desktopPeerPathSummary(nodes, selectedKey))
	}
}

func desktopCoordinatorStateText(state string) string {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "serving":
		return "本机运行中"
	case "connected":
		return "已连接"
	case "connecting", "reconnecting":
		return "重连中"
	default:
		return "已断开"
	}
}

func desktopNetworkStateText(state networkstate.State) string {
	if state == networkstate.Connected {
		return "已连接"
	}
	return "未连接"
}

func desktopPeerPathSummary(nodes []meshNode, selectedKey string) string {
	for _, node := range nodes {
		if node.Kind == "peer" && node.Key == selectedKey {
			return meshNodeConnectionSummary(node)
		}
	}
	for _, node := range nodes {
		if node.Kind == "peer" {
			return meshNodeConnectionSummary(node)
		}
	}
	return "离线或未知"
}

func (a *desktopApp) currentMeshNodeKey() string {
	if a.meshList == nil || a.meshModel == nil {
		return ""
	}
	index := a.meshList.CurrentIndex()
	if index < 0 || index >= len(a.meshModel.items) {
		return ""
	}
	return a.meshModel.items[index].Key
}

func (a *desktopApp) fillRDPTargetFromMeshNode(node meshNode) {
	if node.Key == a.meshSelected {
		return
	}
	a.meshSelected = node.Key
	if a.rdpTarget == nil || node.Kind == "self" {
		return
	}
	target := strings.TrimSpace(node.VirtualIP)
	if target == "" {
		return
	}
	a.rdpTarget.SetText(target)
}

func (a *desktopApp) startMeshStatusAutoRefresh() chan struct{} {
	stop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if a.mw == nil {
					continue
				}
				a.mw.Synchronize(func() {
					a.loadMeshStatus()
				})
			case <-stop:
				return
			}
		}
	}()
	return stop
}

func (a *desktopApp) checkRDP() {
	if a.rdpTarget == nil {
		a.fail("检查 RDP 失败", fmt.Errorf("请先在节点列表选择远程节点"))
		return
	}
	node, _ := a.currentMeshNode()
	check := diagnose.CheckRDPTarget(diagnose.RDPCheckRequest{
		Target:       strings.TrimSpace(a.rdpTarget.Text()),
		TargetDevice: node.NodeID,
		NetworkState: string(a.networkState),
		TargetStatus: node.State,
	})
	a.info(formatChecks([]diagnose.Check{check}))
}

func (a *desktopApp) renameSelectedDevice() {
	node, err := a.currentAdminMeshNode()
	if err != nil {
		a.fail("重命名设备失败", err)
		return
	}
	var dlg *walk.Dialog
	var nameEdit *walk.LineEdit
	var okButton *walk.PushButton
	var cancelButton *walk.PushButton
	currentName := fallbackText(node.DisplayName, node.NodeID)
	if err := (Dialog{
		AssignTo:  &dlg,
		Title:     "重命名设备",
		MinSize:   Size{Width: 420, Height: 170},
		Size:      Size{Width: 460, Height: 190},
		FixedSize: true,
		Layout:    VBox{Margins: Margins{Left: 14, Top: 14, Right: 14, Bottom: 14}, Spacing: 10},
		Children: []Widget{
			Label{Text: "设备显示名称"},
			LineEdit{AssignTo: &nameEdit, Text: currentName},
			Composite{
				Layout: HBox{MarginsZero: true, Spacing: 8},
				Children: []Widget{
					HSpacer{},
					PushButton{AssignTo: &okButton, Text: "保存", OnClicked: func() { dlg.Accept() }},
					PushButton{AssignTo: &cancelButton, Text: "取消", OnClicked: func() { dlg.Cancel() }},
				},
			},
		},
		DefaultButton: &okButton,
		CancelButton:  &cancelButton,
	}).Create(a.mw); err != nil {
		a.fail("重命名设备失败", err)
		return
	}
	if dlg.Run() != walk.DlgCmdOK {
		return
	}
	renamed, err := a.onboardingManager().RenameDevice(node.NodeID, strings.TrimSpace(nameEdit.Text()))
	if err != nil {
		a.fail("重命名设备失败", err)
		return
	}
	a.loadMeshStatus()
	a.info("设备已重命名：\r\n" + renamed.NodeID + " -> " + renamed.DisplayName)
}

func (a *desktopApp) disableSelectedDevice() {
	node, err := a.currentAdminMeshNode()
	if err != nil {
		a.fail("禁用设备失败", err)
		return
	}
	if walk.MsgBox(a.mw, "禁用设备", "禁用设备 "+nodeTitle(node)+"？\r\n\r\n该设备将不能重新连接。", walk.MsgBoxOKCancel|walk.MsgBoxIconInformation) != walk.DlgCmdOK {
		return
	}
	disabled, err := a.onboardingManager().DisableDevice(node.NodeID)
	if err != nil {
		a.fail("禁用设备失败", err)
		return
	}
	a.loadMeshStatus()
	a.info("设备已禁用：\r\n" + fallbackText(disabled.DisplayName, disabled.NodeID))
}

func (a *desktopApp) removeSelectedDevice() {
	node, err := a.currentAdminMeshNode()
	if err != nil {
		a.fail("移除设备失败", err)
		return
	}
	if walk.MsgBox(a.mw, "移除设备", "移除设备 "+nodeTitle(node)+"？\r\n\r\n它将不再作为正常在线设备展示，并会被拒绝重新连接。", walk.MsgBoxOKCancel|walk.MsgBoxIconInformation) != walk.DlgCmdOK {
		return
	}
	removed, err := a.onboardingManager().RemoveDevice(node.NodeID)
	if err != nil {
		a.fail("移除设备失败", err)
		return
	}
	a.loadMeshStatus()
	a.info("设备已移除：\r\n" + fallbackText(removed.DisplayName, removed.NodeID))
}

func (a *desktopApp) currentAdminMeshNode() (meshNode, error) {
	node, ok := a.currentMeshNode()
	if !ok || node.Kind != "peer" || strings.TrimSpace(node.NodeID) == "" {
		return meshNode{}, fmt.Errorf("请先在节点列表选择远端设备")
	}
	return node, nil
}

func (a *desktopApp) openRDP() {
	if node, ok := a.currentMeshNode(); ok && strings.EqualFold(node.State, "disabled") {
		a.fail("打开远程桌面失败", fmt.Errorf("设备已禁用，不能继续连接"))
		return
	}
	target := a.currentRDPTarget()
	if target == "" {
		a.fail("打开远程桌面失败", fmt.Errorf("请先在节点列表选择远程节点"))
		return
	}
	if err := rdp.Open(target); err != nil {
		a.fail("打开远程桌面失败", err)
		return
	}
	a.info("已打开远程桌面：" + target)
}

func (a *desktopApp) currentRDPTarget() string {
	if a.rdpTarget != nil {
		if target := strings.TrimSpace(a.rdpTarget.Text()); target != "" {
			return target
		}
	}
	if a.meshList == nil || a.meshModel == nil {
		return ""
	}
	index := a.meshList.CurrentIndex()
	if index < 0 || index >= len(a.meshModel.items) {
		return ""
	}
	node := a.meshModel.items[index]
	if node.Kind == "self" {
		return ""
	}
	return strings.TrimSpace(node.VirtualIP)
}

func (a *desktopApp) currentMeshNode() (meshNode, bool) {
	if a.meshList == nil || a.meshModel == nil {
		return meshNode{}, false
	}
	index := a.meshList.CurrentIndex()
	if index < 0 || index >= len(a.meshModel.items) {
		return meshNode{}, false
	}
	return a.meshModel.items[index], true
}

func (a *desktopApp) showAboutDialog() {
	host, port := splitUpdateBaseURL(rememberedUpdateURL(appBaseDir()))
	var dlg *walk.Dialog
	var closeButton *walk.PushButton
	if err := (Dialog{
		AssignTo:  &dlg,
		Title:     "关于 Meshlink",
		MinSize:   Size{Width: 520, Height: 430},
		Size:      Size{Width: 560, Height: 460},
		FixedSize: false,
		Layout:    VBox{Margins: Margins{Left: 16, Top: 16, Right: 16, Bottom: 16}, Spacing: 12},
		Children: []Widget{
			Label{
				Text:      "Meshlink 远程桌面组网",
				Font:      Font{Family: "Microsoft YaHei UI", PointSize: 14, Bold: true},
				TextColor: walk.RGB(24, 36, 52),
			},
			Label{Text: "版本：" + version.Display()},
			Label{Text: "自托管远程桌面组网；仅使用 IPv4 虚拟网段，不做全局代理和公网出口转发。"},
			GroupBox{
				Title:  "远程升级",
				Layout: Grid{Columns: 4, Margins: Margins{Left: 12, Top: 18, Right: 12, Bottom: 12}, Spacing: 8},
				Children: []Widget{
					Label{Text: "更新地址"},
					LineEdit{AssignTo: &a.updateHost, Text: host, ColumnSpan: 3},
					Label{Text: "更新端口"},
					LineEdit{AssignTo: &a.updatePort, Text: port, ColumnSpan: 3},
					PushButton{Text: "检查更新", OnClicked: a.checkUpdate, ColumnSpan: 2},
					PushButton{Text: "立即更新", OnClicked: a.applyUpdate, ColumnSpan: 2},
					Label{AssignTo: &a.updateState, Text: "更新状态：未检查", ColumnSpan: 4},
					TextEdit{AssignTo: &a.updateOutput, ReadOnly: true, MinSize: Size{Width: 0, Height: 120}, ColumnSpan: 4},
				},
			},
			Composite{
				Layout: HBox{MarginsZero: true, Spacing: 8},
				Children: []Widget{
					HSpacer{},
					PushButton{AssignTo: &closeButton, Text: "关闭", OnClicked: func() { dlg.Accept() }},
				},
			},
		},
		DefaultButton: &closeButton,
		CancelButton:  &closeButton,
	}).Create(a.mw); err != nil {
		a.fail("打开关于窗口失败", err)
		return
	}
	dlg.Run()
}

func (a *desktopApp) currentUpdateBaseURL() (string, error) {
	if a.updateHost != nil {
		port := ""
		if a.updatePort != nil {
			port = a.updatePort.Text()
		}
		return updateBaseURLFromHostPort(a.updateHost.Text(), port)
	}
	if a.updateURL != nil {
		return meshupdate.NormalizeBaseURL(a.updateURL.Text())
	}
	return meshupdate.NormalizeBaseURL(rememberedUpdateURL(appBaseDir()))
}

func (a *desktopApp) checkUpdate() {
	if a.updateState == nil {
		a.fail("检查更新失败", fmt.Errorf("普通模式不显示更新设置"))
		return
	}
	normalized, err := a.currentUpdateBaseURL()
	if err != nil {
		a.fail("更新地址无效", err)
		return
	}
	if a.updateURL != nil {
		a.updateURL.SetText(normalized)
	}
	if err := rememberUpdateURL(appBaseDir(), normalized); err != nil {
		a.fail("保存更新地址失败", err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	result, err := meshupdate.Check(ctx, normalized, version.Version)
	if err != nil {
		a.latestUpdate = nil
		a.updateState.SetText("更新状态：检查失败")
		a.fail("检查更新失败", err)
		return
	}
	if result.UpdateAvailable {
		a.latestUpdate = &result.Manifest
		a.updateState.SetText("更新状态：发现新版本 " + result.Manifest.Version)
		a.showUpdateInfo(formatUpdateResult(result))
		return
	}
	a.latestUpdate = nil
	a.updateState.SetText("更新状态：已是最新版本")
	a.showUpdateInfo(formatUpdateResult(result))
}

func (a *desktopApp) applyUpdate() {
	if a.updateState == nil {
		a.fail("立即更新失败", fmt.Errorf("普通模式不显示更新设置"))
		return
	}
	if a.latestUpdate == nil {
		a.checkUpdate()
		if a.latestUpdate == nil {
			return
		}
	}
	manifest := *a.latestUpdate
	if meshupdate.CompareVersions(manifest.Version, version.Version) <= 0 {
		a.info("当前已经是最新版本，无需更新。")
		return
	}
	confirm := walk.MsgBox(
		a.mw,
		"确认更新",
		"将下载并安装 Meshlink "+manifest.Version+"。\r\n\r\n更新会关闭桌面控制台，停止并重启 MeshlinkAgent 服务；配置、证书和日志不会被覆盖。\r\n\r\n建议以管理员身份运行本控制台。",
		walk.MsgBoxOKCancel|walk.MsgBoxIconInformation,
	)
	if confirm != walk.DlgCmdOK {
		return
	}

	baseDir := appBaseDir()
	updateURL, err := a.currentUpdateBaseURL()
	if err != nil {
		a.fail("更新地址无效", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	packagePath, err := meshupdate.DownloadPackage(ctx, updateURL, manifest, filepath.Join(baseDir, "updates", "downloads"))
	if err != nil {
		a.updateState.SetText("更新状态：下载失败")
		a.fail("下载更新失败", err)
		return
	}
	scriptPath, err := meshupdate.WriteApplyScript(meshupdate.ApplyOptions{
		BaseDir:     baseDir,
		PackagePath: packagePath,
		Version:     manifest.Version,
		ServiceName: a.currentServiceName(),
		WaitPID:     os.Getpid(),
	})
	if err != nil {
		a.updateState.SetText("更新状态：准备失败")
		a.fail("准备更新失败", err)
		return
	}
	cmd := exec.Command("powershell.exe", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", scriptPath)
	if err := cmd.Start(); err != nil {
		a.updateState.SetText("更新状态：启动失败")
		a.fail("启动更新脚本失败", err)
		return
	}
	a.info("更新脚本已启动：\r\n" + scriptPath + "\r\n\r\n控制台即将关闭，更新完成后会自动重新打开。")
	a.mw.Close()
}

func (a *desktopApp) showUpdateInfo(text string) {
	if a.updateOutput != nil {
		a.updateOutput.SetText(text)
		return
	}
	a.info(text)
}

func (a *desktopApp) info(text string) {
	if a.output != nil {
		a.output.SetText(text)
		return
	}
	if a.meshDetail != nil {
		a.meshDetail.SetText(text)
	}
}

func (a *desktopApp) fail(title string, err error) {
	msg := title + "：\r\n" + err.Error()
	if a.output != nil {
		a.output.SetText(msg)
	} else if a.meshDetail != nil {
		a.meshDetail.SetText(msg)
	}
	walk.MsgBox(a.mw, title, err.Error(), walk.MsgBoxIconError)
}

func formatReport(report diagnose.Report) string {
	var b strings.Builder
	b.WriteString("节点：")
	b.WriteString(report.Summary.NodeID)
	b.WriteString("\r\n模式：")
	b.WriteString(report.Summary.Mode)
	b.WriteString("\r\n虚拟 IP：")
	b.WriteString(report.Summary.VirtualIP)
	b.WriteString("\r\n配置文件：")
	b.WriteString(report.Summary.ConfigPath)
	b.WriteString("\r\n\r\n")
	b.WriteString(formatChecks(report.Checks))
	return b.String()
}

func formatOneClickReport(report diagnose.OneClickReport) string {
	var b strings.Builder
	b.WriteString("一键诊断报告")
	b.WriteString("\r\n")
	b.WriteString(strings.Repeat("=", len([]rune("一键诊断报告"))))
	b.WriteString("\r\n\r\n")
	if report.Summary.Headline != "" {
		b.WriteString(report.Summary.Headline)
		b.WriteString("\r\n\r\n")
	}
	if len(report.Findings) == 0 {
		b.WriteString("未发现需要处理的问题。\r\n")
		return b.String()
	}
	for i, finding := range report.Findings {
		if i > 0 {
			b.WriteString("\r\n")
		}
		b.WriteString(strconv.Itoa(i + 1))
		b.WriteString(". ")
		b.WriteString(finding.Title)
		b.WriteString(" [")
		b.WriteString(statusCN(finding.Severity))
		b.WriteString("]\r\n")
		writeDetailLine(&b, "发现的问题", finding.Problem)
		writeDetailLine(&b, "影响原因", finding.Impact)
		writeDetailLine(&b, "建议处理", finding.Recommendation)
		writeDetailLine(&b, "下一步动作", finding.Action)
	}
	return b.String()
}

func formatUpdateResult(result meshupdate.CheckResult) string {
	var b strings.Builder
	b.WriteString("当前版本：")
	b.WriteString(result.CurrentVersion)
	b.WriteString("\r\n最新版本：")
	b.WriteString(result.Manifest.Version)
	if result.Manifest.BuildTime != "" {
		b.WriteString("\r\n构建时间：")
		b.WriteString(result.Manifest.BuildTime)
	}
	b.WriteString("\r\n包文件：")
	b.WriteString(result.Manifest.Package.File)
	if result.Manifest.Package.Size > 0 {
		b.WriteString("\r\n包大小：")
		b.WriteString(strconv.FormatInt(result.Manifest.Package.Size, 10))
		b.WriteString(" 字节")
	}
	if result.Manifest.Package.SHA256 != "" {
		b.WriteString("\r\nSHA256：")
		b.WriteString(result.Manifest.Package.SHA256)
	}
	b.WriteString("\r\n更新状态：")
	if result.UpdateAvailable {
		b.WriteString("可更新")
	} else {
		b.WriteString("已是最新")
	}
	if len(result.Manifest.Notes) > 0 {
		b.WriteString("\r\n\r\n更新说明：\r\n")
		for _, note := range result.Manifest.Notes {
			b.WriteString("- ")
			b.WriteString(note)
			b.WriteString("\r\n")
		}
	}
	return b.String()
}

func buildMeshNodes(status meshagent.RuntimeStatus, statusPath string) []meshNode {
	nodes := []meshNode{
		{
			ConnectionStatus: status.Self.ConnectionStatus,
			Key:              "self:" + status.Self.NodeID,
			Kind:             "self",
			Online:           strings.EqualFold(status.State, "running"),
			NodeID:           fallbackText(status.Self.NodeID, "本机"),
			Mode:             status.Self.Mode,
			VirtualIP:        status.Self.VirtualIP,
			Listen:           status.Self.Listen,
			Connect:          status.Self.Connect,
			Routes:           status.Self.Routes,
			Fingerprint:      status.Self.Fingerprint,
			CommonName:       status.Self.CommonName,
			UpdatedAt:        status.UpdatedAt,
			State:            status.State,
			StatusPath:       statusPath,
		},
	}
	for _, peer := range status.Peers {
		online := peer.Status != meshagent.PeerStatusOffline && peer.DisconnectedAt == nil
		nodeID := fallbackText(peer.NodeID, peer.CommonName)
		nodes = append(nodes, meshNode{
			ConnectionStatus: peer.ConnectionStatus,
			Key:              "peer:" + nodeID,
			Kind:             "peer",
			Online:           online,
			NodeID:           fallbackText(nodeID, "未命名节点"),
			VirtualIP:        peer.VirtualIP,
			Routes:           peer.Routes,
			RemoteAddr:       peer.RemoteAddr,
			Fingerprint:      peer.Fingerprint,
			CommonName:       peer.CommonName,
			ConnectedAt:      peer.ConnectedAt,
			LastSeen:         peer.LastSeen,
			DisconnectedAt:   peer.DisconnectedAt,
			UpdatedAt:        status.UpdatedAt,
			State:            fallbackText(peer.Status, meshagent.PeerStatusOnline),
			StatusPath:       statusPath,
		})
	}
	sort.SliceStable(nodes, func(i, j int) bool {
		ri, rj := meshNodeRank(nodes[i]), meshNodeRank(nodes[j])
		if ri != rj {
			return ri < rj
		}
		return strings.ToLower(nodes[i].NodeID) < strings.ToLower(nodes[j].NodeID)
	})
	return nodes
}

func buildMeshNodesFromDevices(devices onboarding.DeviceList, statusPath string) []meshNode {
	nodes := make([]meshNode, 0, len(devices.Nodes))
	for _, device := range devices.Nodes {
		connectionStatus := device.ConnectionStatus
		if strings.TrimSpace(device.ErrorCode) != "" {
			connectionStatus.LastError = strings.TrimSpace(device.ErrorCode)
		}
		connectionStatus = pureP2PDesktopConnectionStatus(connectionStatus)
		nodeID := fallbackText(device.NodeID, device.CommonName)
		kind := device.Kind
		if kind == "" {
			kind = "peer"
		}
		nodes = append(nodes, meshNode{
			ConnectionStatus: connectionStatus,
			Key:              kind + ":" + nodeID,
			Kind:             kind,
			Online:           strings.EqualFold(device.Status, "online"),
			NodeID:           fallbackText(nodeID, "未命名节点"),
			DisplayName:      device.DisplayName,
			VirtualIP:        device.VirtualIP,
			RemoteAddr:       device.RemoteAddr,
			Fingerprint:      device.Fingerprint,
			CommonName:       device.CommonName,
			LastSeen:         device.LastSeen,
			LastHeartbeat:    device.LastHeartbeat,
			UpdatedAt:        devices.UpdatedAt,
			State:            device.Status,
			CoordinatorState: devices.CoordinatorState,
			ErrorCode:        device.ErrorCode,
			StatusPath:       statusPath,
		})
	}
	sort.SliceStable(nodes, func(i, j int) bool {
		ri, rj := meshNodeRank(nodes[i]), meshNodeRank(nodes[j])
		if ri != rj {
			return ri < rj
		}
		return strings.ToLower(nodes[i].NodeID) < strings.ToLower(nodes[j].NodeID)
	})
	return nodes
}

func pureP2PDesktopConnectionStatus(status p2p.ConnectionStatus) p2p.ConnectionStatus {
	if status.PathType != p2p.PathTypeRelay && status.PathState != p2p.PathStateFallbackRelay {
		return status
	}
	status.PathType = ""
	status.PathState = p2p.PathStateFailed
	status.QualityScore = 0
	status.LatencyMS = 0
	status.PacketLossPermille = 0
	status.JitterMS = 0
	status.RelayBytesIn = 0
	status.RelayBytesOut = 0
	status.LastError = "direct_unreachable_no_relay"
	if status.SwitchFromPath == p2p.PathTypeRelay {
		status.SwitchFromPath = ""
	}
	if status.SwitchToPath == p2p.PathTypeRelay {
		status.SwitchToPath = ""
	}
	return status
}

func conservativeDesktopDeviceSnapshot(nodes []meshNode) (networkstate.State, []meshNode) {
	offline := make([]meshNode, len(nodes))
	for i, node := range nodes {
		node.Online = false
		if !strings.EqualFold(node.State, "disabled") && !strings.EqualFold(node.State, "revoked") {
			node.State = meshagent.PeerStatusOffline
		}
		node.ConnectionStatus = p2p.ConnectionStatus{PathState: p2p.PathStateOffline}
		offline[i] = node
	}
	return networkstate.Disconnected, offline
}

func meshNodeRank(node meshNode) int {
	if node.Kind == "self" {
		return 0
	}
	if node.Online {
		return 1
	}
	return 2
}

func meshSummaryText(nodes []meshNode, updatedAt time.Time) string {
	online, offline := 0, 0
	for _, node := range nodes {
		if node.Kind != "peer" {
			continue
		}
		if node.Online {
			online++
		} else {
			offline++
		}
	}
	return fmt.Sprintf("在线 %d 台，离线 %d 台 · 更新时间 %s", online, offline, formatTime(updatedAt))
}

func findMeshNodeIndex(nodes []meshNode, key string) int {
	if key == "" {
		return -1
	}
	for i, node := range nodes {
		if node.Key == key {
			return i
		}
	}
	return -1
}

func formatMeshNodeDetail(node meshNode) string {
	var b strings.Builder
	title := nodeTitle(node)
	b.WriteString(title)
	b.WriteString("\r\n")
	b.WriteString(strings.Repeat("=", len([]rune(title))))
	b.WriteString("\r\n\r\n")
	writeDetailLine(&b, "状态", meshNodeStatusText(node))
	writeDetailLine(&b, "连接方式", meshNodeConnectionSummary(node))
	writeDetailLine(&b, "显示名称", node.DisplayName)
	writeDetailLine(&b, "节点名称", node.NodeID)
	if node.Kind == "self" {
		writeDetailLine(&b, "类型", "本机节点")
		writeDetailLine(&b, "模式", node.Mode)
	} else {
		writeDetailLine(&b, "类型", "远端节点")
		writeDetailLine(&b, "来源 IP", sourceIP(node.RemoteAddr))
	}
	writeDetailLine(&b, "虚拟 IP", node.VirtualIP)
	if node.Kind == "peer" {
		writeDetailLine(&b, "连接时间", formatTime(node.ConnectedAt))
		writeDetailLine(&b, "最近在线", formatTime(node.LastSeen))
		if node.DisconnectedAt != nil {
			writeDetailLine(&b, "离线时间", formatTime(*node.DisconnectedAt))
		}
	}
	writeDetailLine(&b, "状态更新时间", formatTime(node.UpdatedAt))
	return b.String()
}

func meshNodeStatusText(node meshNode) string {
	if node.Kind == "self" {
		if node.Online {
			return "本机在线"
		}
		return "本机离线"
	}
	if strings.EqualFold(node.State, "disabled") {
		return "已禁用"
	}
	if strings.EqualFold(node.State, "revoked") {
		return "已吊销"
	}
	if node.Online {
		return "在线"
	}
	return "离线"
}

func meshNodeConnectionSummary(node meshNode) string {
	label := meshNodeConnectionLabel(node)
	parts := []string{label}
	if label == "局域网直连" || label == "公网直连" {
		if node.LatencyMS > 0 {
			parts = append(parts, fmt.Sprintf("%d ms", node.LatencyMS))
		}
		if node.QualityScore > 0 {
			parts = append(parts, fmt.Sprintf("质量 %d", node.QualityScore))
		}
		if state := strings.ToLower(strings.TrimSpace(node.CoordinatorState)); state != "" && state != "connected" {
			parts = append(parts, "协调服务器离线，当前直连不受影响")
		}
	}
	return strings.Join(parts, " · ")
}

func meshNodeConnectionLabel(node meshNode) string {
	if node.Kind == "self" {
		if node.Online {
			return "本机在线"
		}
		return "本机离线"
	}
	if strings.EqualFold(node.State, "disabled") {
		return "已禁用"
	}
	if strings.EqualFold(node.State, "revoked") {
		return "已吊销"
	}
	pathState := strings.ToLower(strings.TrimSpace(string(node.PathState)))
	pathType := strings.ToLower(strings.TrimSpace(string(node.PathType)))
	switch pathState {
	case "rdp-unreachable":
		return "RDP 不可达"
	case "waiting_coordinator":
		return "等待协调服务器"
	case "connecting", "requesting", "preparing", "punching", "authenticating", "reconnecting", "trying_lan_direct", "trying_public_direct":
		return "正在协商"
	case "failed", "fallback_relay":
		if fallbackText(node.ErrorCode, node.LastError) == "direct_unreachable_no_relay" || pathState == "fallback_relay" {
			return "直连失败 · 本版本未启用中继"
		}
		return "直连失败"
	case "idle":
		if node.Online {
			return "已在线 · 尚未建立直连"
		}
		return "离线或未知"
	case "closed", "offline", "offline_or_unknown":
		return "离线或未知"
	}
	if !node.Online {
		return "离线或未知"
	}
	if pathState == "lan_direct" || pathState == "lan_direct_connected" || pathType == "lan_direct" {
		return "局域网直连"
	}
	if pathState == "public_direct" || pathState == "public_direct_connected" || pathType == "public_direct" {
		return "公网直连"
	}
	if pathType == "relay" {
		return "直连失败 · 本版本未启用中继"
	}
	if pathState == "" {
		return "已在线 · 尚未建立直连"
	}
	return "离线或未知"
}

func meshNodeDotColor(node meshNode) walk.Color {
	switch meshNodeConnectionLabel(node) {
	case "局域网直连", "公网直连", "本机在线":
		return walk.RGB(34, 160, 86)
	case "正在协商", "等待协调服务器", "RDP 不可达", "已在线 · 尚未建立直连":
		return walk.RGB(217, 119, 6)
	case "直连失败", "直连失败 · 本版本未启用中继", "已禁用", "已吊销":
		return walk.RGB(159, 45, 33)
	default:
		return walk.RGB(148, 163, 184)
	}
}

func writeDetailLine(b *strings.Builder, label, value string) {
	if strings.TrimSpace(value) == "" {
		return
	}
	b.WriteString(label)
	b.WriteString("：")
	b.WriteString(value)
	b.WriteString("\r\n")
}

func fallbackText(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func sourceIP(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err == nil {
		return host
	}
	return remoteAddr
}

func formatMeshStatus(status meshagent.RuntimeStatus, statusPath string) string {
	var b strings.Builder
	b.WriteString("状态文件：")
	b.WriteString(statusPath)
	b.WriteString("\r\n更新时间：")
	b.WriteString(formatTime(status.UpdatedAt))
	b.WriteString("\r\n运行状态：")
	b.WriteString(status.State)
	b.WriteString("\r\n\r\n本机节点\r\n")
	writeNodeStatus(&b, status.Self)

	b.WriteString("\r\n已连接节点：")
	b.WriteString(strconv.Itoa(len(status.Peers)))
	b.WriteString("\r\n")
	if len(status.Peers) == 0 {
		b.WriteString("  当前没有其他节点形成隧道。\r\n")
		return b.String()
	}
	for i, peer := range status.Peers {
		b.WriteString("\r\n")
		b.WriteString(strconv.Itoa(i + 1))
		b.WriteString(". ")
		b.WriteString(peer.NodeID)
		if peer.VirtualIP != "" {
			b.WriteString("  ")
			b.WriteString(peer.VirtualIP)
		}
		b.WriteString("\r\n")
		if peer.RemoteAddr != "" {
			b.WriteString("   来源地址：")
			b.WriteString(peer.RemoteAddr)
			b.WriteString("\r\n")
		}
		if peer.CommonName != "" {
			b.WriteString("   证书名称：")
			b.WriteString(peer.CommonName)
			b.WriteString("\r\n")
		}
		if peer.Fingerprint != "" {
			b.WriteString("   证书指纹：")
			b.WriteString(peer.Fingerprint)
			b.WriteString("\r\n")
		}
		if len(peer.Routes) > 0 {
			b.WriteString("   宣告路由：")
			b.WriteString(strings.Join(peer.Routes, ", "))
			b.WriteString("\r\n")
		}
		b.WriteString("   连接时间：")
		b.WriteString(formatTime(peer.ConnectedAt))
		b.WriteString("\r\n")
		b.WriteString("   最近更新：")
		b.WriteString(formatTime(peer.LastSeen))
		b.WriteString("\r\n")
	}
	return b.String()
}

func writeNodeStatus(b *strings.Builder, node meshagent.NodeStatus) {
	b.WriteString("  节点名：")
	b.WriteString(node.NodeID)
	b.WriteString("\r\n  模式：")
	b.WriteString(node.Mode)
	b.WriteString("\r\n  虚拟 IP：")
	b.WriteString(node.VirtualIP)
	if node.Listen != "" {
		b.WriteString("\r\n  监听：")
		b.WriteString(node.Listen)
	}
	if node.Connect != "" {
		b.WriteString("\r\n  连接：")
		b.WriteString(node.Connect)
	}
	if node.CommonName != "" {
		b.WriteString("\r\n  证书名称：")
		b.WriteString(node.CommonName)
	}
	if node.Fingerprint != "" {
		b.WriteString("\r\n  证书指纹：")
		b.WriteString(node.Fingerprint)
	}
	if len(node.Routes) > 0 {
		b.WriteString("\r\n  宣告路由：")
		b.WriteString(strings.Join(node.Routes, ", "))
	}
	b.WriteString("\r\n")
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Local().Format("2006-01-02 15:04:05")
}

func formatChecks(checks []diagnose.Check) string {
	var b strings.Builder
	for _, check := range checks {
		b.WriteString("[")
		b.WriteString(statusCN(check.Status))
		b.WriteString("] ")
		b.WriteString(check.Name)
		if check.Detail != "" {
			b.WriteString("\r\n  ")
			b.WriteString(check.Detail)
		}
		b.WriteString("\r\n\r\n")
	}
	return b.String()
}

func hasFail(checks []diagnose.Check) bool {
	for _, check := range checks {
		if check.Status == diagnose.Fail {
			return true
		}
	}
	return false
}

func statusCN(status diagnose.Status) string {
	switch status {
	case diagnose.OK:
		return "正常"
	case diagnose.Warn:
		return "提醒"
	case diagnose.Fail:
		return "失败"
	default:
		return string(status)
	}
}

func actionCN(action string) string {
	switch action {
	case "install":
		return "安装"
	case "start":
		return "启动"
	case "stop":
		return "停止"
	case "uninstall":
		return "卸载"
	default:
		return action
	}
}

func serviceStateCN(state string) string {
	switch state {
	case "running":
		return "运行中"
	case "stopped":
		return "已停止"
	case "start pending":
		return "正在启动"
	case "stop pending":
		return "正在停止"
	case "paused":
		return "已暂停"
	default:
		return state
	}
}

func tail(path string, maxBytes int64) (string, error) {
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
