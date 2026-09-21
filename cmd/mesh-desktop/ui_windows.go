//go:build windows

package main

import (
	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"
	"meshlink/internal/productflags"
)

func (a *desktopApp) createWindow() error {
	bg := SolidColorBrush{Color: themeCanvas}
	sidebar := SolidColorBrush{Color: themeSidebar}
	panel := SolidColorBrush{Color: themeWhite}
	muted := walk.RGB(162, 180, 207)
	light := walk.RGB(234, 240, 250)
	font := Font{Family: "Microsoft YaHei UI", PointSize: 9}
	heading := Font{Family: "Microsoft YaHei UI", PointSize: 12, Bold: true}
	a.meshModel = &meshNodeModel{}
	meshStyler := &meshListStyler{list: &a.meshList, model: a.meshModel}
	deviceMenu := []MenuItem{
		Action{Text: "连接详情", OnTriggered: a.showSelectedConnectionDetails},
		Separator{},
		Action{Text: "重命名设备", OnTriggered: a.renameSelectedDevice},
		Action{Text: "禁用设备", OnTriggered: a.disableSelectedDevice},
		Action{Text: "移除设备", OnTriggered: a.removeSelectedDevice},
	}
	window := MainWindow{
		AssignTo:   &a.mw,
		Title:      "Meshlink 远程桌面组网",
		MinSize:    Size{Width: 1020, Height: 660},
		Size:       Size{Width: 1120, Height: 690},
		Layout:     HBox{MarginsZero: true, SpacingZero: true, Alignment: AlignHNearVNear},
		Background: bg,
		Font:       font,
		MenuItems: []MenuItem{
			Menu{Text: "设备", Items: deviceMenu},
			Menu{Text: "诊断", Items: []MenuItem{
				Action{Text: "一键诊断", OnTriggered: a.runOneClickDiagnostics},
				Action{Text: "诊断远程桌面", OnTriggered: a.checkRDP},
			}},
			Menu{Text: "帮助", Items: []MenuItem{Action{Text: "关于 / 检查更新", OnTriggered: a.showAboutDialog}}},
		},
		Children: []Widget{
			ScrollView{
				HorizontalFixed: true,
				MinSize:         Size{Width: 308}, MaxSize: Size{Width: 308},
				Background: sidebar,
				Layout:     VBox{Margins: Margins{Left: 22, Top: 20, Right: 22, Bottom: 16}, Spacing: 9, Alignment: AlignHNearVNear},
				Children: []Widget{
					Composite{
						Background: sidebar,
						Layout:     HBox{MarginsZero: true, Spacing: 10, Alignment: AlignHNearVCenter},
						Children: []Widget{
							CustomWidget{Background: sidebar, MinSize: Size{Width: 35, Height: 38}, MaxSize: Size{Width: 35, Height: 38}, PaintPixels: paintMeshlinkMark},
							Label{Text: "Meshlink", TextColor: themeWhite, Font: Font{Family: "Segoe UI", PointSize: 23, Bold: true}},
							HSpacer{},
						},
					},
					Label{Text: "让每一台设备，触手可及", TextColor: muted},
					VSpacer{Size: 3},
					Label{Text: "连接工作区", TextColor: muted, Font: Font{Family: "Microsoft YaHei UI", PointSize: 8}},
					Composite{
						Background: sidebar,
						Layout:     HBox{MarginsZero: true, Spacing: 8},
						Children: []Widget{
							styledButton{PushButton: PushButton{AssignTo: &a.serverModeButton, Text: "创建服务器", ToolTipText: "我有公网 IP，创建服务器", OnClicked: a.focusCreateServer}, Treatment: buttonSidebar},
							styledButton{PushButton: PushButton{AssignTo: &a.clientModeButton, Text: "加入已有网络", OnClicked: a.focusJoinNetwork}, Treatment: buttonSidebar},
						},
					},
					PushButton{Text: "我没有公网 IP，使用官方 Hub", OnClicked: a.showOfficialHubMVPDialog, Visible: productflags.OfficialHubMVPEnabled()},
					Label{AssignTo: &a.modeState, Text: "选择创建或加入网络", TextColor: light, Font: heading},
					Composite{
						AssignTo: &a.serverPanel, Visible: false, Background: sidebar,
						Layout: Grid{Columns: 4, MarginsZero: true, Spacing: 8},
						Children: []Widget{
							Label{Text: "域名或公网地址", TextColor: muted, ColumnSpan: 4},
							LineEdit{AssignTo: &a.inviteServer, CueBanner: "example.com:8443", MinSize: Size{Height: 29}, ColumnSpan: 4},
							Label{Text: "监听端口", TextColor: muted, ColumnSpan: 2},
							LineEdit{AssignTo: &a.listenPort, Text: "8443", MinSize: Size{Height: 28}, ColumnSpan: 2},
							styledButton{PushButton: PushButton{Text: "启动服务器", OnClicked: a.createHubOnboarding, ColumnSpan: 2}, Treatment: buttonSidebarPrimary},
							styledButton{PushButton: PushButton{Text: "停止服务器", OnClicked: func() { a.serviceAction("stop") }, ColumnSpan: 2}, Treatment: buttonSidebar},
							Label{Text: "邀请信息", TextColor: muted, ColumnSpan: 4},
							TextEdit{AssignTo: &a.inviteOutput, ReadOnly: true, VScroll: true, CompactHeight: true, Background: SolidColorBrush{Color: themeSidebarPanel}, TextColor: light, MinSize: Size{Height: 68}, MaxSize: Size{Height: 68}, ColumnSpan: 4, Text: "启动服务器后，在这里获取邀请信息。"},
							styledButton{PushButton: PushButton{Text: "重新生成接入码", OnClicked: a.createInviteOnboarding, ColumnSpan: 4}, Treatment: buttonSidebar},
							TextLabel{AssignTo: &a.serverInfo, TextColor: muted, MinSize: Size{Width: 244, Height: 52}, ColumnSpan: 4, Text: "启动后显示本机名称、虚拟 IP 和监听信息。"},
						},
					},
					Composite{
						AssignTo: &a.clientPanel, Background: sidebar,
						Layout: Grid{Columns: 4, MarginsZero: true, Spacing: 8},
						Children: []Widget{
							Label{Text: "邀请链接", TextColor: muted, ColumnSpan: 4},
							LineEdit{AssignTo: &a.inviteLink, CueBanner: "meshlink://join?...", MinSize: Size{Height: 29}, ColumnSpan: 4},
							Label{Text: "验证码", TextColor: muted, ColumnSpan: 4},
							LineEdit{AssignTo: &a.inviteCode, CueBanner: "输入接入验证码", MinSize: Size{Height: 29}, ColumnSpan: 4},
							Label{Text: "本机名称", TextColor: muted, ColumnSpan: 4},
							LineEdit{AssignTo: &a.spokeNodeName, Text: defaultNodeName("spoke"), MinSize: Size{Height: 29}, ColumnSpan: 4},
							styledButton{PushButton: PushButton{Text: "连接", OnClicked: a.connectNetwork, ColumnSpan: 4}, Treatment: buttonSidebarPrimary},
							styledButton{PushButton: PushButton{Text: "断开连接", OnClicked: a.disconnectNetwork, ColumnSpan: 2}, Treatment: buttonSidebar},
							styledButton{PushButton: PushButton{Text: "退出网络", OnClicked: a.exitNetwork, ColumnSpan: 2}, Treatment: buttonSidebar},
							TextLabel{AssignTo: &a.clientInfo, TextColor: muted, MinSize: Size{Width: 244, Height: 52}, ColumnSpan: 4, Text: "使用服务器提供的邀请信息加入网络。\r\n连接成功后，即可访问其他设备。"},
						},
					},
					VSpacer{},
					Composite{
						Background: SolidColorBrush{Color: themeSidebarPanel},
						Layout:     VBox{Margins: Margins{Left: 12, Top: 12, Right: 12, Bottom: 12}, Spacing: 6},
						Children: []Widget{
							Composite{Background: SolidColorBrush{Color: themeSidebarPanel}, Layout: HBox{MarginsZero: true}, Children: []Widget{Label{Text: "后台守护", TextColor: light, Font: Font{Family: "Microsoft YaHei UI", PointSize: 9, Bold: true}}, HSpacer{}}},
							Label{AssignTo: &a.onboardingState, Text: "状态：未加入网络", TextColor: muted},
							Label{AssignTo: &a.serviceState, Text: "服务状态：读取中", TextColor: muted},
							Label{Text: "关闭窗口持续运行 · 开机自动恢复", TextColor: muted, Font: Font{Family: "Microsoft YaHei UI", PointSize: 8}},
						},
					},
				},
			},
			Composite{
				Background: bg, StretchFactor: 1000,
				Layout: VBox{Margins: Margins{Left: 28, Top: 20, Right: 28, Bottom: 16}, Spacing: 14},
				Children: []Widget{
					Composite{
						Background: bg, Layout: HBox{MarginsZero: true, Spacing: 16, Alignment: AlignHNearVCenter},
						Children: []Widget{
							Composite{Background: bg, Layout: VBox{MarginsZero: true, Spacing: 6}, Children: []Widget{
								Label{Text: "网络总览", TextColor: themeInk, Font: Font{Family: "Microsoft YaHei UI", PointSize: 22, Bold: true}},
								Label{Text: "连接你的设备，随时开启远程协作。", TextColor: themeMuted},
							}},
							HSpacer{},
							Composite{Background: SolidColorBrush{Color: walk.RGB(231, 238, 255)}, MinSize: Size{Width: 122}, MaxSize: Size{Width: 146}, Layout: VBox{Margins: Margins{Left: 12, Top: 8, Right: 12, Bottom: 8}}, Children: []Widget{
								Label{AssignTo: &a.quickState, Text: "就绪", TextColor: themeAccent, TextAlignment: AlignCenter},
							}},
						},
					},
					Composite{
						Background: bg, Layout: HBox{MarginsZero: true, Spacing: 16},
						Children: []Widget{
							Composite{Background: panel, StretchFactor: 1, Layout: VBox{Margins: Margins{Left: 20, Top: 14, Right: 20, Bottom: 14}, Spacing: 8}, Children: []Widget{
								Composite{Layout: HBox{MarginsZero: true}, Children: []Widget{Label{Text: "协调服务器", TextColor: themeMuted}, HSpacer{}}},
								Label{AssignTo: &a.coordinatorSummary, Text: "已断开", TextColor: themeInk, Font: heading},
							}},
							Composite{Background: panel, StretchFactor: 1, Layout: VBox{Margins: Margins{Left: 20, Top: 14, Right: 20, Bottom: 14}, Spacing: 8}, Children: []Widget{
								Composite{Layout: HBox{MarginsZero: true}, Children: []Widget{Label{Text: "对端连接路径", TextColor: themeMuted}, HSpacer{}}},
								Label{AssignTo: &a.p2pPathSummary, Text: "离线或未知", TextColor: themeInk, Font: heading},
							}},
						},
					},
					Composite{
						Background: panel, StretchFactor: 1,
						Layout: VBox{Margins: Margins{Left: 20, Top: 16, Right: 20, Bottom: 14}, Spacing: 10},
						Children: []Widget{
							Composite{Background: panel, Layout: HBox{MarginsZero: true, Spacing: 10}, Children: []Widget{
								Label{Text: "网络设备", TextColor: themeInk, Font: heading},
								HSpacer{},
								styledButton{PushButton: PushButton{Text: "刷新列表", OnClicked: a.loadMeshStatus, MinSize: Size{Width: 82}}},
								styledButton{PushButton: PushButton{Text: "打开远程桌面", OnClicked: a.openRDP, MinSize: Size{Width: 120}}, Treatment: buttonPrimary},
							}},
							Label{AssignTo: &a.meshSummary, Text: "等待设备连接", TextColor: themeMuted},
							ListBox{AssignTo: &a.meshList, Model: a.meshModel, ItemStyler: meshStyler, Background: panel, Font: font, MinSize: Size{Height: 198}, StretchFactor: 1000, OnCurrentIndexChanged: a.showSelectedMeshNode,
								ContextMenuItems: []MenuItem{
									Action{Text: "连接详情", OnTriggered: a.showSelectedConnectionDetails},
									Action{Text: "重命名设备", OnTriggered: a.renameSelectedDevice},
									Action{Text: "禁用设备", OnTriggered: a.disableSelectedDevice},
									Action{Text: "移除设备", OnTriggered: a.removeSelectedDevice},
								},
							},
							Composite{Background: SolidColorBrush{Color: themeCanvas}, Layout: VBox{Margins: Margins{Left: 14, Top: 12, Right: 14, Bottom: 12}, Spacing: 6}, Children: []Widget{
								Composite{Background: bg, Layout: HBox{MarginsZero: true, Spacing: 8}, Children: []Widget{
									Label{Text: "设备概况", TextColor: themeMuted},
									HSpacer{},
									styledButton{PushButton: PushButton{Text: "连接详情", OnClicked: a.showSelectedConnectionDetails, MinSize: Size{Height: 26, Width: 78}}},
								}},
								TextEdit{AssignTo: &a.meshDetail, ReadOnly: true, CompactHeight: true, VScroll: true, Background: bg, TextColor: themeInk, Font: font, MinSize: Size{Height: 50}, MaxSize: Size{Height: 50}, Text: "设备就绪后，将显示在上方列表。\r\n选择设备即可查看连接情况，或开启远程桌面。"},
							}},
						},
					},
					Composite{Background: bg, Layout: HBox{MarginsZero: true}, Children: []Widget{
						Label{Text: "MESHLINK  /  私有设备网络", TextColor: themeMuted, Font: Font{Family: "Microsoft YaHei UI", PointSize: 8}},
						HSpacer{},
						Label{Text: "状态自动同步", TextColor: themeMuted, Font: Font{Family: "Microsoft YaHei UI", PointSize: 8}},
					}},
				},
			},
		},
	}
	if err := window.Create(); err != nil {
		return err
	}
	removeControlBorder(a.meshList)
	removeControlBorder(a.meshDetail)
	removeControlBorder(a.inviteOutput)
	a.showConnectionMode("spoke")
	_ = a.mw.SetSize(walk.Size{Width: 1120, Height: 690})
	return nil
}

func paintMeshlinkMark(canvas *walk.Canvas, bounds walk.Rectangle) error {
	scale := func(v int) int { return walk.IntFrom96DPI(v, canvas.DPI()) }
	brush, err := walk.NewSolidColorBrush(themeAccent)
	if err != nil {
		return err
	}
	defer brush.Dispose()
	_ = canvas.FillRoundedRectanglePixels(brush, walk.Rectangle{X: 0, Y: scale(1), Width: scale(34), Height: scale(34)}, walk.Size{Width: scale(10), Height: scale(10)})
	white, err := walk.NewSolidColorBrush(themeWhite)
	if err != nil {
		return err
	}
	defer white.Dispose()
	pen, err := walk.NewGeometricPen(walk.PenSolid, scale(2), white)
	if err != nil {
		return err
	}
	defer pen.Dispose()
	_ = canvas.DrawLinePixels(pen, walk.Point{X: scale(9), Y: scale(23)}, walk.Point{X: scale(9), Y: scale(12)})
	_ = canvas.DrawLinePixels(pen, walk.Point{X: scale(9), Y: scale(12)}, walk.Point{X: scale(17), Y: scale(20)})
	_ = canvas.DrawLinePixels(pen, walk.Point{X: scale(17), Y: scale(20)}, walk.Point{X: scale(25), Y: scale(12)})
	_ = canvas.DrawLinePixels(pen, walk.Point{X: scale(25), Y: scale(12)}, walk.Point{X: scale(25), Y: scale(23)})
	return nil
}

func (a *desktopApp) showSelectedConnectionDetails() {
	node, ok := a.currentMeshNode()
	if !ok {
		return
	}
	var dlg *walk.Dialog
	var closeButton *walk.PushButton
	_, err := (Dialog{
		AssignTo: &dlg, Title: "连接详情 · " + nodeTitle(node), MinSize: Size{Width: 640, Height: 480},
		Font: Font{Family: "Microsoft YaHei UI", PointSize: 9}, Background: SolidColorBrush{Color: themeCanvas},
		Layout: VBox{Margins: Margins{Left: 20, Top: 20, Right: 20, Bottom: 16}, Spacing: 14}, CancelButton: &closeButton,
		Children: []Widget{
			Label{Text: nodeTitle(node), Font: Font{Family: "Microsoft YaHei UI", PointSize: 16, Bold: true}, TextColor: themeInk},
			TextEdit{ReadOnly: true, VScroll: true, Text: formatMeshNodeDetail(node), StretchFactor: 1, Background: SolidColorBrush{Color: themeWhite}},
			Composite{Layout: HBox{MarginsZero: true}, Children: []Widget{HSpacer{}, styledButton{PushButton: PushButton{AssignTo: &closeButton, Text: "关闭", OnClicked: func() { dlg.Cancel() }}, Treatment: buttonPrimary}}},
		},
	}).Run(a.mw)
	if err != nil {
		a.fail("无法打开连接详情", err)
	}
}
