//go:build windows

package main

import (
	"time"

	"golang.org/x/sys/windows"
)

var getDoubleClickTime = windows.NewLazySystemDLL("user32.dll").NewProc("GetDoubleClickTime")

func hostInspectorDelay() time.Duration {
	milliseconds, _, _ := getDoubleClickTime.Call()
	if milliseconds == 0 || milliseconds > 5000 {
		milliseconds = 500
	}
	return time.Duration(milliseconds)*time.Millisecond + 25*time.Millisecond
}
