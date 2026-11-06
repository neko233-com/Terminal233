//go:build desktope2e && windows

package main

import (
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Observe only the fixture process's HWND. Native caption hit testing checks
// that Windows owns the real minimize, maximize/snap and close targets.
func verifyNativeWorkspaceChrome(t *testing.T, pid int, call func(string, ...map[string]any) map[string]any) {
	t.Helper()
	user32 := windows.NewLazySystemDLL("user32.dll")
	enumWindows := user32.NewProc("EnumWindows")
	getPID := user32.NewProc("GetWindowThreadProcessId")
	findChild := user32.NewProc("FindWindowExW")
	getRect := user32.NewProc("GetWindowRect")
	send := user32.NewProc("SendMessageW")
	isZoomed := user32.NewProc("IsZoomed")
	var hwnd uintptr
	callback := windows.NewCallback(func(window, _ uintptr) uintptr {
		var owner uint32
		getPID.Call(window, uintptr(unsafe.Pointer(&owner)))
		if int(owner) == pid {
			class, _ := windows.UTF16PtrFromString("MyGoCaption")
			child, _, _ := findChild.Call(window, 0, uintptr(unsafe.Pointer(class)), 0)
			if child != 0 {
				hwnd = window
				return 0
			}
		}
		return 1
	})
	enumWindows.Call(callback, 0)
	if hwnd == 0 {
		t.Fatal("native window has no integrated Windows caption")
	}
	class, _ := windows.UTF16PtrFromString("MyGoCaption")
	type rectangle struct{ left, top, right, bottom int32 }
	var caption uintptr
	var bounds rectangle
	for child := uintptr(0); ; {
		next, _, _ := findChild.Call(hwnd, child, uintptr(unsafe.Pointer(class)), 0)
		if next == 0 {
			break
		}
		child = next
		var r rectangle
		getRect.Call(child, uintptr(unsafe.Pointer(&r)))
		if r.bottom-r.top >= 20 && r.right-r.left >= 90 {
			caption, bounds = child, r
			break
		}
	}
	if caption == 0 {
		t.Fatal("native window buttons have no usable caption bounds")
	}
	for i, want := range []uintptr{8, 9, 20} { // HTMINBUTTON, HTMAXBUTTON, HTCLOSE
		x := bounds.left + (bounds.right-bounds.left)*int32(2*i+1)/6
		y := (bounds.top + bounds.bottom) / 2
		point := uintptr(uint32(uint16(x)) | uint32(uint16(y))<<16)
		hit, _, _ := send.Call(caption, 0x0084, 0, point) // WM_NCHITTEST
		if hit != want {
			t.Fatalf("Windows caption target %d: got %d want %d", i, hit, want)
		}
	}
	doubleClickStrip := func() {
		t.Helper()
		for _, item := range call("tree")["result"].(map[string]any)["nodes"].([]any) {
			node := item.(map[string]any)
			if node["key"] == "workspace-session-tabs" {
				r := node["bounds"].(map[string]any)
				call("input", map[string]any{"events": []any{map[string]any{"type": "click", "clicks": 2, "x": r["x"].(float64) + r["width"].(float64)/2, "y": r["y"].(float64) + r["height"].(float64)/2}}})
				return
			}
		}
		t.Fatal("native draggable strip unavailable")
	}
	for _, maximized := range []bool{true, false} {
		doubleClickStrip()
		deadline := time.Now().Add(3 * time.Second)
		for {
			zoomed, _, _ := isZoomed.Call(hwnd)
			if (zoomed != 0) == maximized {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("native title strip failed to change maximized state to %t", maximized)
			}
			time.Sleep(30 * time.Millisecond)
		}
		call("idle")
	}
}
