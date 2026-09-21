package main

import (
	"fmt"
	"golang.org/x/sys/windows"
	"os"
)

func platformPreflight() error {
	return fmt.Errorf("此命令用于 Linux；Windows 请使用 Meshlink 桌面程序")
}
func lockFile(file *os.File) error {
	return windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &windows.Overlapped{})
}
