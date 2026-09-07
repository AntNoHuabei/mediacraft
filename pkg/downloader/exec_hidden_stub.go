//go:build !windows

package downloader

import "os/exec"

func hideCommandWindow(*exec.Cmd) {}
