//go:build windows

package runtime

import (
	"os/exec"
	"strconv"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// createBreakawayFromJob：让子进程脱离宿主 Job Object，
// 防止父进程所在 Job 在关闭/终止时连带杀掉推理 server。
const createBreakawayFromJob = 0x01000000

// hideCommandWindow 让子进程无控制台窗口运行。
func hideCommandWindow(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_NO_WINDOW
}

// serverSysProcAttr 返回 server 子进程的 SysProcAttr：
// 无窗口 +（可选）脱离父 Job。若父进程处于不允许 breakaway 的 Job，
// 启动失败后可改用 breakaway=false 重试一次。
func serverSysProcAttr(breakaway bool) *syscall.SysProcAttr {
	flags := uint32(windows.CREATE_NO_WINDOW)
	if breakaway {
		flags |= createBreakawayFromJob
	}
	return &syscall.SysProcAttr{HideWindow: true, CreationFlags: flags}
}

// assignToJobObject 把 pid 加入一个 KILL_ON_JOB_CLOSE 的 Job Object，
// 返回 Job 句柄（以 uintptr 承载）：调用方在停止模型时 closeJobHandle(handle)
// 即会终止整棵进程树。失败返回 0（调用方可继续用 taskkill 兜底）。
func assignToJobObject(pid int) (uintptr, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(job)
		return 0, err
	}
	proc, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		windows.CloseHandle(job)
		return 0, err
	}
	defer windows.CloseHandle(proc)
	if err := windows.AssignProcessToJobObject(job, proc); err != nil {
		windows.CloseHandle(job)
		return 0, err
	}
	return uintptr(job), nil
}

// closeJobHandle 关闭 Job 句柄（触发 KILL_ON_JOB_CLOSE）。
func closeJobHandle(handle uintptr) {
	if handle == 0 {
		return
	}
	windows.CloseHandle(windows.Handle(handle))
}

// killProcessTree 用 taskkill /T /F 终止 pid 的整棵进程树（隐藏窗口执行）。
func killProcessTree(pid int) {
	if pid <= 0 {
		return
	}
	cmd := exec.Command("taskkill", "/pid", strconv.Itoa(pid), "/T", "/F")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	_ = cmd.Run()
}
