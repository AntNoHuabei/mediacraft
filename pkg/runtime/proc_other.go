//go:build !windows

package runtime

import (
	"os/exec"
	"syscall"
)

// hideCommandWindow Unix 无操作。
func hideCommandWindow(_ *exec.Cmd) {}

// serverSysProcAttr Unix 返回 nil（无特殊属性）。
func serverSysProcAttr(_ bool) *syscall.SysProcAttr {
	return nil
}

// assignToJobObject Unix 无 Job Object，返回 0 句柄表示不适用。
func assignToJobObject(_ int) (uintptr, error) {
	return 0, nil
}

// closeJobHandle Unix 无操作。
func closeJobHandle(_ uintptr) {}

// killProcessTree 终止 pid 进程组（-pid 组信号；失败则杀单进程）。
func killProcessTree(pid int) {
	if pid <= 0 {
		return
	}
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}
