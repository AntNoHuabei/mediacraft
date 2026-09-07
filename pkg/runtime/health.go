package runtime

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"time"
)

// defaultRuntimeHealthTimeout 健康探测单次超时。
const defaultRuntimeHealthTimeout = 3 * time.Second

// HTTPHealthOK 对 url 做 GET，2xx 即视为健康（其余含网络错误为不健康）。
func HTTPHealthOK(url string, timeout time.Duration) bool {
	if timeout <= 0 {
		timeout = defaultRuntimeHealthTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode < 500
}

// TCPPortOpen 探测 host:port 是否能建立 TCP 连接。
func TCPPortOpen(host string, port int, timeout time.Duration) bool {
	if timeout <= 0 {
		timeout = defaultRuntimeHealthTimeout
	}
	address := net.JoinHostPort(host, fmt.Sprintf("%d", port))
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// runtimeProcessExited 报告 cmd 进程是否已退出。
// 先看本地 ProcessState（Wait 已返回/调用过），再走平台探测。
func runtimeProcessExited(cmd *exec.Cmd) bool {
	if cmd == nil || cmd.Process == nil {
		return true
	}
	if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
		return true
	}
	return !processRunning(cmd.Process.Pid)
}

// markRuntimeInfoHealthy 回写"健康"状态。
func markRuntimeInfoHealthy(info *ModelRuntimeInfo) {
	if info == nil {
		return
	}
	info.RunStatus = RunStatusRunning
	info.LastHealthCheck = time.Now()
	info.HealthError = ""
}

// markRuntimeInfoError 回写"不健康"状态。
func markRuntimeInfoError(info *ModelRuntimeInfo, message string) {
	if info == nil {
		return
	}
	info.RunStatus = RunStatusError
	info.LastHealthCheck = time.Now()
	info.HealthError = message
}
