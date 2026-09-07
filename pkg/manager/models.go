package manager

import (
	"context"
	"fmt"

	"github.com/AntNoHuabei/mediacraft/pkg/runtime"
)

// InstallModel 安装模型到其首选引擎（必要时自动安装引擎运行时）。
// 支持 ctx 取消；安装进度经 callback 上报。
func (s *Supervisor) InstallModel(ctx context.Context, modelName string, callback runtime.InstallCallback) error {
	rt, err := s.resolveRuntimeForModel(modelName)
	if err != nil {
		return err
	}
	manifest := s.manifestByName(modelName)
	if manifest.Name == "" {
		return fmt.Errorf("model %q not found in catalog", modelName)
	}
	if !rt.GetInfo().Installed {
		s.log.Info("installing runtime before model install", "model", modelName, "runtime", rt.Name())
		if err := s.InstallRuntime(ctx, rt.Name(), nil); err != nil {
			return err
		}
	}
	return rt.InstallModel(ctx, manifest, callback)
}

// UninstallModel 卸载模型（先确保其进程已停止）。
func (s *Supervisor) UninstallModel(ctx context.Context, modelName string) error {
	rts := s.engineCandidatesForModel(modelName)
	if len(rts) == 0 {
		return fmt.Errorf("model %q has no supported inference engine", modelName)
	}
	var firstErr error
	for _, rt := range rts {
		if info, err := rt.GetModelInfo(modelName); err == nil && info.RuntimeInfo.RunStatus == runtime.RunStatusRunning {
			if err := rt.StopModel(ctx, modelName); err != nil && firstErr == nil {
				firstErr = err
			}
			ReleasePort(info.RuntimeInfo.Port)
		}
	}
	for _, rt := range rts {
		if err := rt.UninstallModel(ctx, modelName); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// RunningModelInfo 返回某模型当前运行态（任一引擎）。
func (s *Supervisor) RunningModelInfo(modelName string) (runtime.ModelRuntimeInfo, bool) {
	for _, engine := range enginePriority {
		rt, ok := s.Runtime(engine)
		if !ok {
			continue
		}
		info, err := rt.GetModelInfo(modelName)
		if err != nil {
			continue
		}
		if info.RuntimeInfo.RunStatus == runtime.RunStatusRunning && info.RuntimeInfo.Port > 0 {
			return info.RuntimeInfo, true
		}
	}
	return runtime.ModelRuntimeInfo{}, false
}

// ModelInstalled 判断模型是否已安装（任一引擎视角）。
func (s *Supervisor) ModelInstalled(modelName string) bool {
	rts := s.engineCandidatesForModel(modelName)
	for _, rt := range rts {
		if info, err := rt.GetModelInfo(modelName); err == nil && info.Installed {
			return true
		}
	}
	return false
}
