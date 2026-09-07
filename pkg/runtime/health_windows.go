//go:build windows

package runtime

import "golang.org/x/sys/windows"

// processRunning 报告 pid 进程是否仍在运行。
// Windows 上用 OpenProcess + GetExitCodeProcess==STILL_ACTIVE 判断，
// 避免依赖平台外工具。
func processRunning(pid int) bool {
	if pid <= 0 {
		return false
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(handle)
	var code uint32
	if err := windows.GetExitCodeProcess(handle, &code); err != nil {
		return false
	}
	// STILL_ACTIVE = 259（x/sys/windows 未导出该常量）。
	return code == 259
}
