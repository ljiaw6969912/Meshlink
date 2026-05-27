//go:build windows

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"

	meshagent "meshlink/internal/agent"
	"meshlink/internal/certutil"
	"meshlink/internal/config"
	"meshlink/internal/diagnose"
	"meshlink/internal/rdp"
	"meshlink/internal/runner"
	"meshlink/internal/winservice"
)

type desktopApp struct {
	mw *walk.MainWindow

	serviceName *walk.LineEdit
	configPath  *walk.LineEdit
	certOut     *walk.LineEdit
	caName      *walk.LineEdit
	nodeName    *walk.LineEdit
	dnsSANs     *walk.LineEdit
	ipSANs      *walk.LineEdit
	certDays    *walk.LineEdit
	rdpTarget   *walk.LineEdit

	serviceState *walk.Label
	quickState   *walk.Label
	configEdit   *walk.TextEdit
	meshList     *walk.ListBox
	meshDetail   *walk.TextEdit
	meshSummary  *walk.Label
	meshModel    *meshNodeModel
	meshSelected string
	output       *walk.TextEdit
}

type desktopSettings struct {
	LastConfigPath string `json:"last_config_path"`
}

type meshNode struct {
	Key            string
	Kind           string
	Online         bool
	NodeID         string
	Mode           string
	VirtualIP      string
	Listen         string
	Connect        string
	Routes         []string
	RemoteAddr     string
	Fingerprint    string
	CommonName     string
	ConnectedAt    time.Time
	LastSeen       time.Time
	DisconnectedAt *time.Time
	UpdatedAt      time.Time
	State          string
	StatusPath     string
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
	status := "离线"
	if node.Kind == "self" {
		status = "本机"
	} else if node.Online {
		status = "在线"
	}
	text := status + "  " + node.NodeID
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

	dotColor := walk.RGB(148, 163, 184)
	stateText := "离线"
	if node.Kind == "self" {
		if node.Online {
			dotColor = walk.RGB(9, 105, 98)
			stateText = "本机在线"
		} else {
			stateText = "本机离线"
		}
	} else if node.Online {
		dotColor = walk.RGB(34, 160, 86)
		stateText = "在线"
	}
	if brush, err := walk.NewSolidColorBrush(dotColor); err == nil {
		defer brush.Dispose()
		dot := walk.Rectangle{X: bounds.X + s.scale(15), Y: bounds.Y + s.scale(22), Width: s.scale(10), Height: s.scale(10)}
		_ = canvas.FillEllipsePixels(brush, dot)
	}

	textLeft := bounds.X + s.scale(36)
	textWidth := bounds.Width - s.scale(48)
	title := node.NodeID
	if node.VirtualIP != "" {
		title += "    " + node.VirtualIP
	}
	subtitle := stateText
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

func (s *meshListStyler) scale(v int) int {
	dpi := 96
	if s.list != nil && *s.list != nil {
		dpi = (*s.list).DPI()
	}
	return walk.IntFrom96DPI(v, dpi)
}

var app = &desktopApp{}

func main() {
	if err := app.run(); err != nil {
		walk.MsgBox(nil, "启动失败", err.Error(), walk.MsgBoxIconError)
		os.Exit(1)
	}
}

func (a *desktopApp) run() error {
	baseDir := appBaseDir()
	defaultConfig := rememberedConfigPath(baseDir)
	defaultCertOut := filepath.Join(baseDir, "certs")

	bg := SolidColorBrush{Color: walk.RGB(246, 248, 251)}
	header := SolidColorBrush{Color: walk.RGB(20, 33, 48)}
	panel := SolidColorBrush{Color: walk.RGB(255, 255, 255)}
	ink := walk.RGB(24, 36, 52)
	muted := walk.RGB(101, 113, 128)
	accent := walk.RGB(9, 105, 98)
	danger := walk.RGB(160, 45, 33)
	mono := Font{Family: "Consolas", PointSize: 10}
	a.meshModel = &meshNodeModel{}
	meshStyler := &meshListStyler{list: &a.meshList, model: a.meshModel}

	window := MainWindow{
		AssignTo:   &a.mw,
		Title:      "Meshlink 桌面控制台",
		MinSize:    Size{1040, 720},
		Size:       Size{1180, 780},
		Layout:     VBox{MarginsZero: true, SpacingZero: true},
		Background: bg,
		Children: []Widget{
			Composite{
				Background: header,
				Layout:     HBox{Margins: Margins{Left: 22, Top: 18, Right: 22, Bottom: 18}, Spacing: 18},
				Children: []Widget{
					Composite{
						Layout:        VBox{MarginsZero: true, Spacing: 3},
						StretchFactor: 1,
						Background:    header,
						Children: []Widget{
							Label{
								Text:      "Meshlink 桌面控制台",
								TextColor: walk.RGB(255, 255, 255),
								Font:      Font{Family: "Microsoft YaHei UI", PointSize: 18, Bold: true},
							},
							Label{
								Text:      "自托管 TCP/TLS 异地组网 · 证书 · 服务 · 诊断 · 远程桌面",
								TextColor: walk.RGB(198, 210, 222),
							},
						},
					},
					Label{
						AssignTo:      &a.quickState,
						Text:          "未诊断",
						TextColor:     walk.RGB(255, 255, 255),
						MinSize:       Size{130, 28},
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
						MinSize:    Size{380, 0},
						MaxSize:    Size{470, 0},
						Background: bg,
						Layout:     VBox{Margins: Margins{Left: 16, Top: 16, Right: 10, Bottom: 16}, Spacing: 12},
						Children: []Widget{
							GroupBox{
								Title:      "系统服务",
								Background: panel,
								Layout:     Grid{Columns: 3, Margins: Margins{Left: 12, Top: 18, Right: 12, Bottom: 12}, Spacing: 8},
								Children: []Widget{
									Label{Text: "服务名", TextColor: muted},
									LineEdit{AssignTo: &a.serviceName, Text: "MeshlinkAgent", ColumnSpan: 2},
									Label{Text: "配置文件", TextColor: muted},
									LineEdit{AssignTo: &a.configPath, Text: defaultConfig},
									PushButton{Text: "选择文件", OnClicked: a.chooseConfigFile},
									PushButton{Text: "读取配置", OnClicked: a.loadConfig},
									PushButton{Text: "保存配置", OnClicked: a.saveConfig},
									PushButton{Text: "服务状态", OnClicked: a.serviceStatus},
									PushButton{Text: "安装服务", OnClicked: func() { a.serviceAction("install") }},
									PushButton{Text: "启动服务", OnClicked: func() { a.serviceAction("start") }},
									PushButton{Text: "停止服务", OnClicked: func() { a.serviceAction("stop") }},
									PushButton{Text: "卸载服务", OnClicked: func() { a.serviceAction("uninstall") }},
									Label{AssignTo: &a.serviceState, Text: "服务状态：未读取", TextColor: muted, ColumnSpan: 2},
								},
							},
							GroupBox{
								Title:      "证书",
								Background: panel,
								Layout:     Grid{Columns: 4, Margins: Margins{Left: 12, Top: 18, Right: 12, Bottom: 12}, Spacing: 8},
								Children: []Widget{
									Label{Text: "输出目录", TextColor: muted},
									LineEdit{AssignTo: &a.certOut, Text: defaultCertOut, ColumnSpan: 3},
									Label{Text: "CA 名称", TextColor: muted},
									LineEdit{AssignTo: &a.caName, Text: "my-mesh", ColumnSpan: 3},
									PushButton{Text: "创建 CA", OnClicked: a.createCA, ColumnSpan: 4},
									Label{Text: "节点名", TextColor: muted},
									LineEdit{AssignTo: &a.nodeName, Text: "home-hub", ColumnSpan: 3},
									Label{Text: "DNS", TextColor: muted},
									LineEdit{AssignTo: &a.dnsSANs, CueBanner: "home.example.com", ColumnSpan: 3},
									Label{Text: "IP", TextColor: muted},
									LineEdit{AssignTo: &a.ipSANs, CueBanner: "192.0.2.10", ColumnSpan: 3},
									Label{Text: "天数", TextColor: muted},
									LineEdit{AssignTo: &a.certDays, Text: "825", ColumnSpan: 3},
									PushButton{Text: "签发节点证书", OnClicked: a.issueCert, ColumnSpan: 4},
								},
							},
							GroupBox{
								Title:      "远程桌面",
								Background: panel,
								Layout:     Grid{Columns: 3, Margins: Margins{Left: 12, Top: 18, Right: 12, Bottom: 12}, Spacing: 8},
								Children: []Widget{
									Label{Text: "目标地址", TextColor: muted},
									LineEdit{AssignTo: &a.rdpTarget, Text: "192.168.1.50", ColumnSpan: 2},
									PushButton{Text: "检查 RDP", OnClicked: a.checkRDP},
									PushButton{Text: "打开远程桌面", OnClicked: a.openRDP, ColumnSpan: 2},
								},
							},
							Composite{
								Background: bg,
								Layout:     HBox{MarginsZero: true, Spacing: 8},
								Children: []Widget{
									PushButton{Text: "开始诊断", OnClicked: a.runDiagnostics, StretchFactor: 1},
									PushButton{Text: "读取日志", OnClicked: a.loadLogs, StretchFactor: 1},
								},
							},
						},
					},
					TabWidget{
						StretchFactor:  1,
						ContentMargins: Margins{Left: 14, Top: 14, Right: 14, Bottom: 14},
						Pages: []TabPage{
							{
								Title:  "配置编辑",
								Layout: VBox{MarginsZero: true, Spacing: 10},
								Children: []Widget{
									Label{Text: "JSON 配置", TextColor: ink, Font: Font{Family: "Microsoft YaHei UI", PointSize: 11, Bold: true}},
									TextEdit{AssignTo: &a.configEdit, Font: mono, VScroll: true, HScroll: true, StretchFactor: 1},
								},
							},
							{
								Title:  "组网机群",
								Layout: VBox{MarginsZero: true, Spacing: 10},
								Children: []Widget{
									Composite{
										Layout: HBox{MarginsZero: true, Spacing: 8},
										Children: []Widget{
											Label{Text: "节点列表", TextColor: ink, Font: Font{Family: "Microsoft YaHei UI", PointSize: 11, Bold: true}},
											Label{AssignTo: &a.meshSummary, Text: "等待刷新", TextColor: muted, StretchFactor: 1},
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
												MinSize:               Size{280, 0},
												MaxSize:               Size{360, 0},
												OnCurrentIndexChanged: a.showSelectedMeshNode,
											},
											TextEdit{
												AssignTo:      &a.meshDetail,
												ReadOnly:      true,
												Font:          mono,
												VScroll:       true,
												HScroll:       true,
												Text:          "左侧会显示当前组网节点；点击一个节点查看来源 IP、证书指纹和连接时间。",
												StretchFactor: 1,
											},
										},
									},
								},
							},
							{
								Title:  "诊断和日志",
								Layout: VBox{MarginsZero: true, Spacing: 10},
								Children: []Widget{
									Label{Text: "运行结果", TextColor: ink, Font: Font{Family: "Microsoft YaHei UI", PointSize: 11, Bold: true}},
									TextEdit{AssignTo: &a.output, ReadOnly: true, Font: mono, VScroll: true, HScroll: true, Text: "就绪。", StretchFactor: 1},
									Label{Text: "提示：真实 Wintun、路由、NAT 和 Windows 服务操作需要管理员权限。", TextColor: danger},
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
	a.loadConfigSilently()
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

func defaultConfigPath(baseDir string) string {
	return filepath.Join(baseDir, "configs", "spoke.example.json")
}

func settingsPath(baseDir string) string {
	return filepath.Join(baseDir, "configs", "desktop-state.json")
}

func rememberedConfigPath(baseDir string) string {
	defaultPath := defaultConfigPath(baseDir)
	b, err := os.ReadFile(settingsPath(baseDir))
	if err != nil {
		return defaultPath
	}
	var settings desktopSettings
	if err := json.Unmarshal(b, &settings); err != nil {
		return defaultPath
	}
	path := strings.TrimSpace(settings.LastConfigPath)
	if path == "" {
		return defaultPath
	}
	if info, err := os.Stat(path); err == nil && !info.IsDir() {
		return path
	}
	return defaultPath
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
	settings := desktopSettings{LastConfigPath: absPath}
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

func formatJSON(b []byte) (string, error) {
	var out bytes.Buffer
	if err := json.Indent(&out, b, "", "  "); err != nil {
		return "", err
	}
	return out.String(), nil
}

func (a *desktopApp) chooseConfigFile() {
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
	path := strings.TrimSpace(a.configPath.Text())
	if err := a.loadConfigPath(path); err != nil {
		a.fail("读取配置失败", err)
	}
}

func (a *desktopApp) loadConfigSilently() {
	path := strings.TrimSpace(a.configPath.Text())
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
	a.configPath.SetText(absPath)
	a.configEdit.SetText(text)
	if err := rememberConfigPath(appBaseDir(), absPath); err != nil {
		return err
	}
	a.info("配置已读取：\r\n" + absPath)
	return nil
}

func (a *desktopApp) saveConfig() {
	path := strings.TrimSpace(a.configPath.Text())
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
	a.configPath.SetText(absPath)
	a.configEdit.SetText(string(pretty))
	if err := rememberConfigPath(appBaseDir(), absPath); err != nil {
		a.fail("记住配置文件失败", err)
		return
	}
	a.info("配置已保存：\r\n" + absPath)
}

func (a *desktopApp) serviceAction(action string) {
	name := strings.TrimSpace(a.serviceName.Text())
	configPath := strings.TrimSpace(a.configPath.Text())
	var err error
	switch action {
	case "install":
		err = installAgentService(name, configPath)
	case "start":
		err = winservice.Start(name)
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
	status, err := winservice.Status(strings.TrimSpace(a.serviceName.Text()))
	if err != nil {
		a.fail("读取服务状态失败", err)
		return
	}
	text := "服务状态：未安装"
	if status.Installed {
		text = "服务状态：" + serviceStateCN(status.State)
	}
	a.serviceState.SetText(text)
	a.quickState.SetText(strings.TrimPrefix(text, "服务状态："))
	a.info(text)
}

func (a *desktopApp) createCA() {
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
	report := diagnose.Run(strings.TrimSpace(a.configPath.Text()), strings.TrimSpace(a.serviceName.Text()))
	a.info(formatReport(report))
	if hasFail(report.Checks) {
		a.quickState.SetText("需要处理")
		return
	}
	a.quickState.SetText("诊断正常")
}

func (a *desktopApp) loadLogs() {
	configPath := strings.TrimSpace(a.configPath.Text())
	serviceName := strings.TrimSpace(a.serviceName.Text())
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
	configPath := strings.TrimSpace(a.configPath.Text())
	serviceName := strings.TrimSpace(a.serviceName.Text())
	statusPath := runner.StatusPath(configPath, serviceName)
	selectedKey := a.currentMeshNodeKey()
	b, err := os.ReadFile(statusPath)
	if err != nil {
		a.meshModel.SetItems(nil)
		a.meshSummary.SetText("未读取到状态文件")
		a.meshDetail.SetText("读取组网状态失败：\r\n" + statusPath + "\r\n\r\n" + err.Error() + "\r\n\r\n请确认服务已经启动。")
		return
	}
	var status meshagent.RuntimeStatus
	if err := json.Unmarshal(b, &status); err != nil {
		a.meshModel.SetItems(nil)
		a.meshSummary.SetText("状态文件格式无效")
		a.meshDetail.SetText("组网状态 JSON 无效：\r\n" + err.Error())
		return
	}
	nodes := buildMeshNodes(status, statusPath)
	a.meshModel.SetItems(nodes)
	a.meshSummary.SetText(meshSummaryText(nodes, status.UpdatedAt))
	index := findMeshNodeIndex(nodes, selectedKey)
	if index < 0 && len(nodes) > 0 {
		index = 0
	}
	if index >= 0 {
		_ = a.meshList.SetCurrentIndex(index)
		a.showSelectedMeshNode()
	} else {
		a.meshDetail.SetText("当前没有可显示的节点。")
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
	a.fillRDPTargetFromMeshNode(node)
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
	check := diagnose.CheckRDP(strings.TrimSpace(a.rdpTarget.Text()))
	a.info(formatChecks([]diagnose.Check{check}))
}

func (a *desktopApp) openRDP() {
	target := strings.TrimSpace(a.rdpTarget.Text())
	if err := rdp.Open(target); err != nil {
		a.fail("打开远程桌面失败", err)
		return
	}
	a.info("已打开远程桌面：" + target)
}

func (a *desktopApp) info(text string) {
	a.output.SetText(text)
}

func (a *desktopApp) fail(title string, err error) {
	msg := title + "：\r\n" + err.Error()
	a.output.SetText(msg)
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

func buildMeshNodes(status meshagent.RuntimeStatus, statusPath string) []meshNode {
	nodes := []meshNode{
		{
			Key:         "self:" + status.Self.NodeID,
			Kind:        "self",
			Online:      strings.EqualFold(status.State, "running"),
			NodeID:      fallbackText(status.Self.NodeID, "本机"),
			Mode:        status.Self.Mode,
			VirtualIP:   status.Self.VirtualIP,
			Listen:      status.Self.Listen,
			Connect:     status.Self.Connect,
			Routes:      status.Self.Routes,
			Fingerprint: status.Self.Fingerprint,
			CommonName:  status.Self.CommonName,
			UpdatedAt:   status.UpdatedAt,
			State:       status.State,
			StatusPath:  statusPath,
		},
	}
	for _, peer := range status.Peers {
		online := peer.Status != meshagent.PeerStatusOffline && peer.DisconnectedAt == nil
		nodeID := fallbackText(peer.NodeID, peer.CommonName)
		nodes = append(nodes, meshNode{
			Key:            "peer:" + nodeID,
			Kind:           "peer",
			Online:         online,
			NodeID:         fallbackText(nodeID, "未命名节点"),
			VirtualIP:      peer.VirtualIP,
			Routes:         peer.Routes,
			RemoteAddr:     peer.RemoteAddr,
			Fingerprint:    peer.Fingerprint,
			CommonName:     peer.CommonName,
			ConnectedAt:    peer.ConnectedAt,
			LastSeen:       peer.LastSeen,
			DisconnectedAt: peer.DisconnectedAt,
			UpdatedAt:      status.UpdatedAt,
			State:          status.State,
			StatusPath:     statusPath,
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
	b.WriteString(node.NodeID)
	b.WriteString("\r\n")
	b.WriteString(strings.Repeat("=", len([]rune(node.NodeID))))
	b.WriteString("\r\n\r\n")
	writeDetailLine(&b, "状态", meshNodeStatusText(node))
	if node.Kind == "self" {
		writeDetailLine(&b, "类型", "本机节点")
		writeDetailLine(&b, "模式", node.Mode)
		writeDetailLine(&b, "服务状态", node.State)
		writeDetailLine(&b, "监听地址", node.Listen)
		writeDetailLine(&b, "连接地址", node.Connect)
	} else {
		writeDetailLine(&b, "类型", "远端节点")
		writeDetailLine(&b, "来源 IP", sourceIP(node.RemoteAddr))
		writeDetailLine(&b, "来源地址", node.RemoteAddr)
	}
	writeDetailLine(&b, "虚拟 IP", node.VirtualIP)
	writeDetailLine(&b, "证书名称", node.CommonName)
	writeDetailLine(&b, "证书指纹", node.Fingerprint)
	if len(node.Routes) > 0 {
		writeDetailLine(&b, "宣告路由", strings.Join(node.Routes, ", "))
	}
	if node.Kind == "peer" {
		writeDetailLine(&b, "连接时间", formatTime(node.ConnectedAt))
		writeDetailLine(&b, "最近在线", formatTime(node.LastSeen))
		if node.DisconnectedAt != nil {
			writeDetailLine(&b, "离线时间", formatTime(*node.DisconnectedAt))
		}
	}
	writeDetailLine(&b, "状态更新时间", formatTime(node.UpdatedAt))
	writeDetailLine(&b, "状态文件", node.StatusPath)
	return b.String()
}

func meshNodeStatusText(node meshNode) string {
	if node.Kind == "self" {
		if node.Online {
			return "本机在线"
		}
		return "本机离线"
	}
	if node.Online {
		return "在线"
	}
	return "离线"
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
