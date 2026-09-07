//go:build !windows

package runtime

import "syscall"

// processRunning 报告 pid 进程是否仍在运行（Unix 信号 0 探测）。
func processRunning(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}
