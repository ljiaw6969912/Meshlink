package main

import (
	"fmt"
	"golang.org/x/sys/unix"
	"os"
	"os/exec"
)

func platformPreflight() error {
	if _, err := exec.LookPath("ip"); err != nil {
		return fmt.Errorf("需要安装 iproute2（缺少 ip 命令）")
	}
	f, err := os.OpenFile("/dev/net/tun", os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("无法打开 /dev/net/tun：请启用 TUN，并使用 root 或具备 CAP_NET_ADMIN 的账户运行：%w", err)
	}
	return f.Close()
}

func lockFile(file *os.File) error { return unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB) }
