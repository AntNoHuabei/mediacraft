# AGENTS.md — MediaCraft Studio 工程上下文

给后续接手本仓库的 agent / 开发者看的约定。改动前先读本文件与 `README.md`。

## 1. 项目一句话

**MediaCraft Studio**：本地音图视创作工具箱（Wails3 + Go + React/TS）。
把 [audio.cpp](https://github.com/ggml-org/audio.cpp) 与 [sd.cpp](https://github.com/ggml-org/stable-diffusion.cpp)
的 server 二进制当作"运行时"统一托管（安装/启停/健康/按需起模型进程），
并在此基础上做音频（TTS/ASR）、图像生成，规划覆盖视频。

- Go module：`github.com/AntNoHuabei/mediacraft`（曾用名 sdcpp_desktop，已整体改名）
- License：MIT（仓库根 `LICENSE`，Copyright (c) 2026 AntNoHuabei）
- 开发交流群（QQ）：`488797113`（位置见 §6）
- 远端：`git@github.com:AntNoHuabei/mediacraft.git`；本地主分支 `main`
  （注意：GitHub 远端默认分支目前是 `dev`，尚未推送，涉及推送先和用户确认分支策略）

## 2. 范围与红线

- 仅实现/维护 audio.cpp 与 sd-cpp 两个引擎；herdsman 其它引擎（llama.cpp、NPU、comfyui…）
  一律不引入。
- **命名红线**：代码/注释/文案中不得再出现 `herdsman` / `szStarWave`（早期清理过）。
  允许的例外：标识符 `SDCpp`（= sd.cpp）、`SDCPP_GPU_VENDOR`（旧环境变量兼容读取）、
  `embed/manifest/models/*.json` 里 modelscope 下载 URL 中的 `flowy2025/herdsman_models`
  —— 那是**真实下载数据路径，改会 404，不要动**；如要换源需先镜像数据。
- 错误信息面向中文用户可带中文，但**错误码/事件名保持英文稳定**。

## 3. 架构分层（改代码先认门）

```
embed/manifest/{models,runtimes}/*.json  模型/运行时下载清单（platform/vendor/版本/downloads/commands…）
catalog/                                 清单解析：Manifest/Runtime 结构、MatchPlatform/Select、
                                         ResolveInferenceEngine/MergedParameters
pkg/types/                               跨包共享类型（DownloadProgress 自包含，勿再依赖外部私有模块）
pkg/downloader/                          下载器：多源选择、重试(backoff)、分段并发、SHA256、进度
pkg/runtime/                             运行时托管抽象（核心）
  runtime.go          Name/InstallStatusEnum/RunStatus/InstallState/Runtime 接口/RuntimeInfo
  base_runtime.go     BaseRuntime：运行时与模型的安装/卸载/磁盘校验/本地状态扫描；
                      子类嵌入它（BaseOptions{ RuntimesDir,ModelsDir,Downloader,Logger,Vendor }）
  audio_cpp.go        AudioCppRuntime：audiocpp_server.exe，--config JSON（family/task/mode）
  sd_cpp.go           SDCppRuntime：sd-server.exe，模型子文件转 CLI 参数，TCP 就绪
  health*.go / proc_*.go / errors.go / logbuf.go / archive.go / disk_* / vendor.go  共享原语
pkg/manager/                              编排层 Supervisor：
  注册表（audio/sd）→ Install(可取消)/Uninstall → StartModel（引擎选择/端口分配）→
  StopModel/StopAll → 30s CheckHealth → ListModels 跨引擎聚合 → 回调注入
service/model_service.go                 Wails3 绑定门面（前端契约，勿破坏方法签名）
frontend/                                 React + TS + AntD v6 + 自绘 console UI
```

依赖方向：`catalog ← pkg/runtime ← pkg/manager ← service ← main`。禁止反向引用/循环。

## 4. 后端关键语义（容易踩坑）

- **安装进度回调是 0..1 小数**（下载器 `written/total`，250ms 节流）！
  任何转成百分比的地方必须 `progressPercent(fraction)`（已实现于 base_runtime.go）；
  字节数用 `fraction * total`。不要直接 `int(progress)`。
- 下载前做**磁盘空间校验**：`ensureDiskSpace(dir,total)`，
  余量 = `max(total/4, 512MB)`，不足返回 `INSUFFICIENT_DISK_SPACE`（错误码见 errors.go）。
- 安装支持 ctx 取消；`CancelInstallModel` 只作用于模型安装；manager 层运行时安装可经
  `CancelRuntimeInstall`。安装中重复调用返回 `INSTALL_IN_PROGRESS`。
- 卸载 = 先停模型进程再删目录；卸载后 manager 状态应立即反映（有回归测试覆盖）。
- 模型/运行时"已安装"判定：exe 存在（`ExecutableFor(name)`）+ `<dir>/.installed` 标记文件。
- 目录：`%UserConfigDir%/mediacraft/{runtimes,models,cache/downloads}`（模块改名后数据目录是 mediacraft）。
- 端口由 manager `allocatePort()` 分配并传入 `StartModel`；进程托管见
  `proc_windows.go`（隐藏窗口 + Job Object KILL_ON_JOB_CLOSE + taskkill 兜底）。
- 每模型一个 server 进程；`StartModel` 幂等；健康失败会停掉该模型（Audio HTTP /health 容忍 2 次，SD 纯 TCP）。

## 5. 前后端事件契约

- `mc:install`：运行时/模型安装进度。data：
  `{kind:'runtime'|'model', name, status, stage, progress(0-100), message, bytes_done, bytes_total, speed}`
  - 终态 `status: installed|error` 到达后前端删除该条进度并 `refresh()`。
- `mc:model`：模型运行状态。data：`{name, engine, run_status, port, health_error}`。
- 前端订阅在 `App.tsx` 的 `AppContent` useEffect 里（`Events.On`，返回 unsubscribe）。
- 改 Go service 公开方法签名后必须重生成绑定：
  `wails3 generate bindings -ts -i ./...`（注意必须带 `-ts -i`：接口模式，匹配现有 TS）。

## 6. UI（Mix Console「混音控制台」方向）

- 设计令牌：`frontend/src/theme.tsx`（antd token）+ `frontend/src/styles/console.css`（CSS 变量）。
  **两处色值必须同步维护**（antd 需要具体色值，CSS 走变量）；深浅双主题经 `html[data-theme]`。
- 页面语言：运行时=「母线(bus)」卡片、模型=「轨道(track)」列表、底部=「走带(transport)」条。
- 自绘标题栏：无边框（main.go `Frameless:true` + `Windows.NonClientRegionSupport:true`），
  前端 `.titlebar` 用 `-webkit-app-region: drag`，窗体按钮在 `.titlebar-controls`（no-drag），
  走 `@wailsio/runtime` 的 `Window.Minimise/ToggleMaximise/IsMaximised/Close`。
- 主题切换与设置按钮：位于左侧设备轨底部 `.rail-actions`；设置用右侧 Drawer。
- 版本号与 QQ 群落位：
  - 版本：底部走带条 `MediaCraft Studio · v{APP_VERSION}` + 设置抽屉「关于」；
    `APP_VERSION` 常量在 `App.tsx` 顶部，**需与 `build/config.yml` 的 version 保持一致**（现 0.1.0）。
  - QQ `488797113`：README「交流反馈」+ 底部可点击复制 chip + 设置抽屉「关于」。
- **布局滚动规范**：`html,body,#root { overflow:hidden }`；所有 flex 纵向滚动容器
  （`.console-main`、`.stage`）必须 `min-height:0`，页面滚动只发生在 `.stage`。
  出现最外层滚动条 = 中间层漏了 `min-height:0`。

## 7. 常用命令

```sh
go build . ./catalog ./embed ./service ./pkg/...   # 全量编译（注意：go build ./... 会因 Wails 模板
                                                   # 自带 build/ios 的“空 main 包”报错，属预期，排除即可）
go vet . ./service ./pkg/... && go test ./pkg/...  # 静态检查 + 单测
wails3 dev                                          # 真机运行（热更）
wails3 generate bindings -ts -i ./...               # 改绑定签名后重新生成（-ts -i 勿漏）
cd frontend && npm run build                        # 前端 tsc+vite 校验
```

- 构建产物勿提交：根目录 `mediacraft.exe`、`*.syso`、`frontend/dist`、`frontend/node_modules` 均已 ignore。
- 偶发 `ReplaceFileW EIO`（Windows 文件瞬时占用）：稍等重试同一条写入即可，勿换等价操作硬绕。

## 8. 测试约定

- `pkg/runtime`：参数推断 / audio config 与 sd args 构造 / 健康助手 / 错误码 / 环形日志 /
  进度量纲（0..1→%）、磁盘不足拒绝（注入 `diskFree` 替身）。
- `pkg/manager`：注册、引擎过滤、安装→卸载状态反映（`roundtrip_test.go` 用 httptest 本地源）。
- 真实"下载大文件 + 起真进程 + 推理"的端到端只能在用户机器 `wails3 dev` 验证，单测不触网不启进程。

## 9. 状态与待办（2026-09）

已完成：运行时托管闭环（装/卸/启停/健康/状态）、进度事件与 UI 进度条、卸载即时刷新、
磁盘预检、混音台 UI + 深浅主题、自绘标题栏、MIT/QQ 落位。

未做/待定：真机端到端联调；远端推送与默认分支(main/dev)决策；安装进度异步化与取消按钮；
sd-server stdout 采样进度解析；图片双栏工作台与音频波形；视频时间线骨架；应用图标/打包发布；
README/品牌完善。新增任务前请与用户确认优先级。
