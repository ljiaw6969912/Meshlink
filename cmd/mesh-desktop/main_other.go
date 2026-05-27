//go:build !windows

package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "Meshlink 桌面程序目前只支持 Windows。")
	os.Exit(1)
}
