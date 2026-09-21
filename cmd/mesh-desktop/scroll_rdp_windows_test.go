//go:build windows

package main

import (
	"fmt"
	"runtime"
	"testing"

	"github.com/lxn/walk"
	"github.com/lxn/win"
)

func TestDeviceRefreshKeepsScrolledViewportAndSelection(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	a := &desktopApp{}
	if err := a.createWindow(); err != nil {
		t.Fatal(err)
	}
	defer a.mw.Dispose()
	if err := a.meshList.SetSize(walk.Size{Width: 450, Height: 198}); err != nil {
		t.Fatal(err)
	}
	nodes := make([]meshNode, 25)
	for i := range nodes {
		nodes[i] = meshNode{Key: fmt.Sprint(i), NodeID: fmt.Sprint(i), Kind: "peer", VirtualIP: fmt.Sprintf("10.77.0.%d", i+2)}
	}
	a.meshModel.SetItems(nodes)
	a.meshList.SetCurrentIndex(0)
	a.meshList.SendMessage(win.LB_SETTOPINDEX, 8, 0)
	if got := int(a.meshList.SendMessage(win.LB_GETTOPINDEX, 0, 0)); got != 8 {
		t.Fatalf("test viewport: %d", got)
	}
	a.rdpPort.SetText("3391")
	resets := 0
	a.meshModel.ItemsReset().Attach(func() { resets++ })
	refresh := func(next []meshNode) {
		restore := a.preserveMeshViewport()
		defer restore()
		selected := a.currentMeshNodeKey()
		a.meshModel.SetItems(next)
		a.meshList.SetCurrentIndex(findMeshNodeIndex(next, selected))
		a.showSelectedMeshNode()
	}
	for i := 0; i < 3; i++ {
		next := append([]meshNode(nil), a.meshModel.items...)
		next[0].LatencyMS++
		if i == 1 {
			next[0].DisplayName = "新设备名称"
		}
		refresh(next)
		if got := int(a.meshList.SendMessage(win.LB_GETTOPINDEX, 0, 0)); got != 8 {
			t.Fatalf("refresh moved viewport to %d", got)
		}
		if a.currentMeshNodeKey() != "0" {
			t.Fatal("refresh changed selection")
		}
		if a.rdpPort.Text() != "3391" {
			t.Fatal("refresh overwrote edited RDP port")
		}
	}
	if resets != 0 {
		t.Fatalf("ordinary status changes rebuilt list %d times", resets)
	}
	next := append([]meshNode{{Key: "new", Kind: "peer", NodeID: "new"}}, a.meshModel.items...)
	refresh(next)
	top := int(a.meshList.SendMessage(win.LB_GETTOPINDEX, 0, 0))
	if top != 9 || a.meshModel.items[top].Key != "8" || a.currentMeshNodeKey() != "0" {
		t.Fatalf("membership change lost viewport/selection: top=%d selection=%q", top, a.currentMeshNodeKey())
	}
	if target, err := a.currentRDPAddress(); err != nil || target != "10.77.0.2:3391" {
		t.Fatalf("custom target: %s, %v", target, err)
	}
	refresh(nil)
	if a.meshList.CurrentIndex() != -1 {
		t.Fatal("empty list retained invalid selection")
	}
}

func TestRDPPortDefaultValidationAndPersistence(t *testing.T) {
	for _, tc := range []struct{ port, want string }{{"", "3389"}, {"3389", "3389"}, {" 3391 ", "3391"}, {"1", "1"}, {"65535", "65535"}} {
		got, err := rdpAddress("10.77.0.2", tc.port)
		if err != nil || got != "10.77.0.2:"+tc.want {
			t.Fatalf("port %q: %q, %v", tc.port, got, err)
		}
	}
	for _, port := range []string{"0", "65536", "-1", "abc", "3389.5"} {
		if _, err := rdpAddress("10.77.0.2", port); err == nil {
			t.Fatalf("accepted invalid port %q", port)
		}
	}
	if _, err := rdpAddress("", "3389"); err == nil {
		t.Fatal("accepted missing device")
	}
	dir := t.TempDir()
	if got := rememberedRDPPort(dir); got != "3389" {
		t.Fatalf("default port: %s", got)
	}
	want := desktopSettings{LastConfigPath: "existing-config", LastUpdateURL: "existing-update"}
	if err := saveDesktopSettings(dir, want); err != nil {
		t.Fatal(err)
	}
	if err := rememberRDPPort(dir, "3391"); err != nil {
		t.Fatal(err)
	}
	want.LastRDPPort = 3391
	if got := loadDesktopSettings(dir); got != want {
		t.Fatalf("port save replaced other settings: %+v", got)
	}
	if got := rememberedRDPPort(dir); got != "3391" {
		t.Fatalf("reopened port: %s", got)
	}
	if err := rememberRDPPort(dir, "bad"); err == nil {
		t.Fatal("saved invalid port")
	}
	if got := loadDesktopSettings(dir); got != want {
		t.Fatal("invalid input changed saved settings")
	}
}
