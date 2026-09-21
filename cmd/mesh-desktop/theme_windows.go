//go:build windows

package main

import (
	"fmt"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"
	"github.com/lxn/win"
	"golang.org/x/sys/windows"
)

var (
	themeCanvas       = walk.RGB(243, 246, 251)
	themeSidebar      = walk.RGB(18, 30, 51)
	themeSidebarPanel = walk.RGB(27, 42, 65)
	themeWhite        = walk.RGB(255, 255, 255)
	themeInk          = walk.RGB(29, 43, 65)
	themeMuted        = walk.RGB(115, 129, 151)
	themeAccent       = walk.RGB(51, 105, 235)
	themeLine         = walk.RGB(229, 235, 244)
)

type buttonTreatment uint8

const (
	buttonSecondary buttonTreatment = iota
	buttonPrimary
	buttonSidebar
	buttonSidebarPrimary
)

// Keep real Windows buttons, including their accessibility, keyboard handling,
// capture and click semantics. Only their painting is replaced.
type styledButton struct {
	PushButton
	Treatment buttonTreatment
}

func (b styledButton) Create(builder *Builder) error {
	var button *walk.PushButton
	original := b.PushButton.AssignTo
	b.PushButton.AssignTo = &button
	if b.MinSize.Height == 0 {
		b.MinSize.Height = 34
	}
	if b.MaxSize.Height == 0 {
		b.MaxSize.Height = b.MinSize.Height
	}
	if err := b.PushButton.Create(builder); err != nil {
		return err
	}
	if original != nil {
		*original = button
	}
	state := &buttonPaintState{button: button, treatment: b.Treatment}
	buttonPaintStates.Store(button.Handle(), state)
	ok, _, err := setButtonSubclass.Call(uintptr(button.Handle()), buttonPaintCallback, 1, 0)
	if ok == 0 {
		buttonPaintStates.Delete(button.Handle())
		return fmt.Errorf("style desktop button: %w", err)
	}
	// Walk restores a button's original WndProc during WM_DESTROY, before
	// WM_NCDESTROY can reach our subclass. Release our state before that point.
	handle := button.Handle()
	button.Disposing().Attach(func() {
		buttonPaintStates.Delete(handle)
		removeButtonSubclass.Call(uintptr(handle), buttonPaintCallback, 1)
	})
	return nil
}

type buttonPaintState struct {
	button    *walk.PushButton
	treatment buttonTreatment
	hovered   bool
	selected  bool
}

var (
	buttonPaintStates     sync.Map
	buttonControls        = windows.NewLazySystemDLL("comctl32.dll")
	setButtonSubclass     = buttonControls.NewProc("SetWindowSubclass")
	removeButtonSubclass  = buttonControls.NewProc("RemoveWindowSubclass")
	defaultButtonSubclass = buttonControls.NewProc("DefSubclassProc")
	buttonPaintCallback   uintptr
)

func init() { buttonPaintCallback = syscall.NewCallback(themedButtonWndProc) }

func setButtonSelected(button *walk.PushButton, selected bool) {
	if button == nil {
		return
	}
	if value, ok := buttonPaintStates.Load(button.Handle()); ok {
		value.(*buttonPaintState).selected = selected
		_ = button.Invalidate()
	}
}

func themedButtonWndProc(hwnd win.HWND, msg uint32, wp, lp, id, data uintptr) uintptr {
	value, found := buttonPaintStates.Load(hwnd)
	if found {
		state := value.(*buttonPaintState)
		switch msg {
		case win.WM_PAINT, win.WM_PRINTCLIENT:
			var ps win.PAINTSTRUCT
			dc := win.HDC(wp)
			if dc == 0 {
				dc = win.BeginPaint(hwnd, &ps)
				defer win.EndPaint(hwnd, &ps)
			}
			if dc != 0 {
				paintThemedButton(dc, state)
			}
			return 0
		case win.WM_ERASEBKGND:
			return 1
		case win.WM_MOUSEMOVE:
			if !state.hovered {
				state.hovered = true
				tme := win.TRACKMOUSEEVENT{CbSize: uint32(unsafe.Sizeof(win.TRACKMOUSEEVENT{})), DwFlags: win.TME_LEAVE, HwndTrack: hwnd}
				win.TrackMouseEvent(&tme)
				_ = state.button.Invalidate()
			}
		case win.WM_MOUSELEAVE:
			state.hovered = false
			_ = state.button.Invalidate()
		case win.WM_ENABLE, win.WM_SETFOCUS, win.WM_KILLFOCUS, win.WM_LBUTTONDOWN, win.WM_LBUTTONUP, win.WM_KEYDOWN, win.WM_KEYUP, win.BM_SETSTATE:
			_ = state.button.Invalidate()
		case win.WM_NCDESTROY:
			buttonPaintStates.Delete(hwnd)
			removeButtonSubclass.Call(uintptr(hwnd), buttonPaintCallback, id)
		}
	}
	result, _, _ := defaultButtonSubclass.Call(uintptr(hwnd), uintptr(msg), wp, lp)
	return result
}

func paintThemedButton(dc win.HDC, state *buttonPaintState) {
	button := state.button
	var rect win.RECT
	win.GetClientRect(button.Handle(), &rect)
	background, fill, foreground, border := themeWhite, walk.RGB(245, 248, 253), themeInk, themeLine
	if state.treatment == buttonSidebar || state.treatment == buttonSidebarPrimary {
		background, fill, foreground, border = themeSidebar, themeSidebarPanel, walk.RGB(202, 214, 234), themeSidebarPanel
	}
	primary := state.treatment == buttonPrimary || state.treatment == buttonSidebarPrimary || state.selected
	if primary {
		fill, foreground, border = themeAccent, themeWhite, themeAccent
	}
	if state.hovered {
		if primary {
			fill, border = walk.RGB(65, 121, 250), walk.RGB(65, 121, 250)
		} else if state.treatment == buttonSidebar {
			fill, border = walk.RGB(38, 55, 80), walk.RGB(38, 55, 80)
		} else {
			fill = walk.RGB(233, 240, 254)
		}
	}
	pressed := win.SendMessage(button.Handle(), win.BM_GETSTATE, 0, 0)&win.BST_PUSHED != 0
	if pressed {
		fill, border = walk.RGB(38, 85, 198), walk.RGB(38, 85, 198)
		foreground = themeWhite
	}
	if !button.Enabled() {
		foreground = themeMuted
		fill = themeLine
		border = themeLine
	}
	dpi := button.DPI()
	scale := func(v int) int32 { return int32(walk.IntFrom96DPI(v, dpi)) }
	outer := win.CreateBrushIndirect(&win.LOGBRUSH{LbStyle: win.BS_SOLID, LbColor: win.COLORREF(background)})
	outerBrush := win.SelectObject(dc, win.HGDIOBJ(outer))
	outerPen := win.SelectObject(dc, win.GetStockObject(win.NULL_PEN))
	win.Rectangle_(dc, rect.Left, rect.Top, rect.Right+1, rect.Bottom+1)
	win.SelectObject(dc, outerBrush)
	win.SelectObject(dc, outerPen)
	win.DeleteObject(win.HGDIOBJ(outer))
	brush := win.CreateBrushIndirect(&win.LOGBRUSH{LbStyle: win.BS_SOLID, LbColor: win.COLORREF(fill)})
	pen := win.ExtCreatePen(win.PS_COSMETIC|win.PS_SOLID, 1, &win.LOGBRUSH{LbStyle: win.BS_SOLID, LbColor: win.COLORREF(border)}, 0, nil)
	oldBrush := win.SelectObject(dc, win.HGDIOBJ(brush))
	oldPen := win.SelectObject(dc, win.HGDIOBJ(pen))
	win.RoundRect(dc, 0, 0, rect.Right, rect.Bottom, scale(9), scale(9))
	win.SelectObject(dc, oldBrush)
	win.SelectObject(dc, oldPen)
	win.DeleteObject(win.HGDIOBJ(brush))
	win.DeleteObject(win.HGDIOBJ(pen))
	font := win.HGDIOBJ(win.SendMessage(button.Handle(), win.WM_GETFONT, 0, 0))
	oldFont := win.SelectObject(dc, font)
	defer win.SelectObject(dc, oldFont)
	win.SetBkMode(dc, win.TRANSPARENT)
	win.SetTextColor(dc, win.COLORREF(foreground))
	text, _ := windows.UTF16FromString(button.Text())
	textRect := rect
	textRect.Left += scale(8)
	textRect.Right -= scale(8)
	if pressed {
		textRect.Top += scale(1)
	}
	win.DrawTextEx(dc, &text[0], -1, &textRect, win.DT_CENTER|win.DT_VCENTER|win.DT_SINGLELINE|win.DT_END_ELLIPSIS, nil)
	if button.Focused() {
		focus := rect
		focus.Left += scale(4)
		focus.Top += scale(4)
		focus.Right -= scale(4)
		focus.Bottom -= scale(4)
		win.DrawFocusRect(dc, &focus)
	}
}

func removeControlBorder(widget walk.Widget) {
	hwnd := widget.Handle()
	win.SetWindowLong(hwnd, win.GWL_STYLE, win.GetWindowLong(hwnd, win.GWL_STYLE)&^win.WS_BORDER)
	win.SetWindowLong(hwnd, win.GWL_EXSTYLE, win.GetWindowLong(hwnd, win.GWL_EXSTYLE)&^win.WS_EX_CLIENTEDGE)
	win.SetWindowPos(hwnd, 0, 0, 0, 0, 0, win.SWP_NOMOVE|win.SWP_NOSIZE|win.SWP_NOZORDER|win.SWP_NOACTIVATE|win.SWP_FRAMECHANGED)
}

func formatMeshNodePresentation(node meshNode) string {
	latency := "延迟待测"
	if node.LatencyMS > 0 {
		latency = fmt.Sprintf("%d ms", node.LatencyMS)
	}
	return strings.Join([]string{
		nodeTitle(node) + "   ·   " + fallbackText(node.VirtualIP, "未分配虚拟 IP"),
		meshNodeConnectionSummary(node),
		"连接延迟   " + latency + "     /     设备状态   " + meshNodeStatusText(node),
	}, "\r\n")
}
