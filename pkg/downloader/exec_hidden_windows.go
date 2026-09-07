//go:build windows

package downloader

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

func hideCommandWindow(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_NO_WINDOW
}
