package runtime

import (
	"os"
	"os/exec"
	goruntime "runtime"
	"strings"
)

// CurrentVendor 探测当前 GPU 厂商：
//  1. 环境变量 MEDIACRAFT_GPU_VENDOR（nvidia/amd/intel/cpu）优先；
//     （旧名 SDCPP_GPU_VENDOR 仍兼容读取）
//  2. Windows 下查询显卡名称前缀；
//  3. 其余返回 cpu。
func CurrentVendor() Vendor {
	value := strings.ToLower(strings.TrimSpace(os.Getenv("MEDIACRAFT_GPU_VENDOR")))
	if value == "" {
		value = strings.ToLower(strings.TrimSpace(os.Getenv("SDCPP_GPU_VENDOR")))
	}
	if value != "" {
		switch value {
		case "nvidia":
			return VendorNvidia
		case "amd", "radeon":
			return VendorAMD
		case "intel":
			return VendorIntel
		case "cpu":
			return VendorCPU
		default:
			return Vendor(value)
		}
	}
	if goruntime.GOOS == "windows" {
		if output, err := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", "(Get-CimInstance Win32_VideoController).Name").Output(); err == nil {
			name := strings.ToLower(string(output))
			switch {
			case strings.Contains(name, "nvidia"):
				return VendorNvidia
			case strings.Contains(name, "amd"), strings.Contains(name, "radeon"):
				return VendorAMD
			case strings.Contains(name, "intel"):
				return VendorIntel
			}
		}
	}
	return VendorCPU
}

// BackendForVendor 返回 audio.cpp server config 的 backend 字段值。
func BackendForVendor(vendor Vendor) string {
	switch vendor {
	case VendorNvidia:
		return "cuda"
	case VendorAMD:
		return "hip"
	default:
		return "cpu"
	}
}

// VendorName 返回 vendor 的字符串形式（"all" 兜底为 cpu）。
func VendorName(vendor Vendor) string {
	if vendor == "" {
		return "cpu"
	}
	return string(vendor)
}
