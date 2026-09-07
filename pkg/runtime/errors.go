package runtime

import (
	"errors"
	"fmt"
	"strings"
)

// 稳定错误码：供上层（manager/service/前端）按码判断，不做字符串匹配。
const (
	CodeContextCanceled     = "CONTEXT_CANCELED"
	CodeRuntimeNotInstalled = "RUNTIME_NOT_INSTALLED"
	CodeRuntimePathMissing  = "RUNTIME_PATH_MISSING"
	CodeModelPathMissing    = "MODEL_PATH_MISSING"
	CodeModelFileMissing    = "MODEL_FILE_MISSING"
	CodeModelNotInstalled   = "MODEL_NOT_INSTALLED"
	CodeInvalidOptions      = "INVALID_OPTIONS"
	CodeInvalidModelType    = "INVALID_MODEL_TYPE"
	CodeExecutableMissing   = "EXECUTABLE_MISSING"
	CodeProcessStartFailed  = "PROCESS_START_FAILED"
	CodePortBindFailed      = "PORT_BIND_FAILED"
	CodeProcessExited       = "PROCESS_EXITED"
	CodeStartupTimeout      = "STARTUP_TIMEOUT"
	CodeGPUDriverOutdated   = "GPU_DRIVER_OUTDATED"
	CodeServiceUnavailable  = "SERVICE_UNAVAILABLE"
	CodeDownloadFailed      = "DOWNLOAD_FAILED"
	CodeChecksumMismatch    = "CHECKSUM_MISMATCH"
	CodeExtractFailed       = "EXTRACT_FAILED"
	CodeInstallInProgress   = "INSTALL_IN_PROGRESS"
	CodeRuntimeInUse        = "RUNTIME_IN_USE"
	CodeModelInUse          = "MODEL_IN_USE"
	CodeInternal            = "INTERNAL"
)

// StartModelError 模型启动/健康失败的结构化错误。
type StartModelError struct {
	Code    string
	Message string
	Cause   error
	Detail  map[string]any
}

func NewStartModelError(code, message string) *StartModelError {
	return &StartModelError{Code: code, Message: message, Detail: map[string]any{}}
}

func (e *StartModelError) WithCause(err error) *StartModelError {
	if e != nil && err != nil {
		e.Cause = err
	}
	return e
}

func (e *StartModelError) WithDetail(key string, value any) *StartModelError {
	if e != nil {
		e.Detail[key] = value
	}
	return e
}

func (e *StartModelError) Error() string {
	if e == nil {
		return "<nil>"
	}
	var sb strings.Builder
	sb.WriteString("start_model[")
	sb.WriteString(e.Code)
	sb.WriteString("]: ")
	sb.WriteString(e.Message)
	if e.Cause != nil {
		sb.WriteString(": ")
		sb.WriteString(e.Cause.Error())
	}
	return sb.String()
}

func (e *StartModelError) Unwrap() error { return e.Cause }

// AsStartModelError 从 error 链中提取 *StartModelError。
func AsStartModelError(err error) (*StartModelError, bool) {
	var target *StartModelError
	if errors.As(err, &target) {
		return target, true
	}
	return nil, false
}

// WrapStartModelError 把任意错误包成指定码的 StartModelError（保留原错误链）。
func WrapStartModelError(err error, code, message string) *StartModelError {
	if err == nil {
		return nil
	}
	return NewStartModelError(code, message).WithCause(err)
}

// RuntimeHealthError 运行时健康检查聚合错误：列出所有不健康的模型。
type RuntimeHealthError struct {
	Runtime Name
	Models  []string
	Err     error
}

func (e *RuntimeHealthError) Error() string {
	return fmt.Sprintf("runtime %s health check failed for models %v: %v", e.Runtime, e.Models, e.Err)
}

func (e *RuntimeHealthError) Unwrap() error { return e.Err }

// AsRuntimeHealthError 从 error 链中提取 *RuntimeHealthError。
func AsRuntimeHealthError(err error) (*RuntimeHealthError, bool) {
	var target *RuntimeHealthError
	if errors.As(err, &target) {
		return target, true
	}
	return nil, false
}

// InstallError 安装（运行时/模型）失败的结构化错误。
type InstallError struct {
	Code    string
	Message string
	Cause   error
}

func NewInstallError(code, message string) *InstallError {
	return &InstallError{Code: code, Message: message}
}

func (e *InstallError) WithCause(err error) *InstallError {
	if e != nil && err != nil {
		e.Cause = err
	}
	return e
}

func (e *InstallError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Cause != nil {
		return "install[" + e.Code + "]: " + e.Message + ": " + e.Cause.Error()
	}
	return "install[" + e.Code + "]: " + e.Message
}

func (e *InstallError) Unwrap() error { return e.Cause }
