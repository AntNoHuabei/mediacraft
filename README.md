# MediaCraft Studio

本地音图视创作工具箱（Local audio, image & video creation toolkit）。

MediaCraft Studio 是一个基于 [Wails v3](https://v3.wails.io) 的桌面应用，把
[audio.cpp](https://github.com/ggml-org/audio.cpp) 与 [sd.cpp](https://github.com/ggml-org/stable-diffusion.cpp)
等本地推理 server 作为"运行时"统一托管：安装、启动、健康巡检、按需起停，
并在此基础上提供音频（TTS / ASR）、图像生成等创作功能，规划覆盖视频。

## 交流反馈

- **开发交流群（QQ）：`488797113`** —— 使用问题、功能建议、模型/引擎接入都欢迎来群里聊
- 群内可第一时间获取新版本与内测包

## 特性

- **本地优先**：模型与运行时全部在本地执行，无云端依赖；
- **运行时管理**：audio.cpp / sd-cpp 的安装（带进度、可取消）、卸载、
  每模型一个 server 进程的启动/停止、端口分配与 30s 健康巡检；
- **模型目录驱动**：`embed/manifest/` 下的 JSON 清单决定"模型 → 引擎 → 下载源"，
  新模型只需加清单即可接入；
- **分层可扩展**：`pkg/runtime`（托管抽象）+ `pkg/manager`（编排）+ `service`（Wails 绑定），
  后续可平滑加入新的本地引擎。

## 架构

```
embed/manifest/{models,runtimes}/*.json   ← 模型/运行时下载清单（ModelScope 等）
        │
catalog/                                   ← 清单解析、平台/厂商匹配
        │
pkg/runtime/                               ← 运行时托管抽象
  runtime.go       核心接口 / 状态枚举 / 进度结构
  base_runtime.go  安装、卸载、模型安装的通用基座
  audio_cpp.go     audio.cpp（audiocpp_server）托管
  sd_cpp.go        sd.cpp（sd-server）托管
  health*.go / proc_*.go / errors.go / logbuf.go / archive.go   共享原语
        │
pkg/manager/                               ← supervisor：注册表、端口分配、
                                             可取消安装、健康轮询、状态聚合
        │
service/model_service.go                   ← Wails3 绑定门面（前端契约）
frontend/                                  ← React + TS + Ant Design
```

## 目录

| 路径 | 说明 |
|---|---|
| `embed/manifest/models/` | 模型清单（ASR/TTS/图像模型及下载参数） |
| `embed/manifest/runtimes/` | 运行时清单（audio.cpp / sd-cpp 的按厂商下载包） |
| `pkg/downloader/` | 下载器：多源选择、重试、分段并发、进度/校验 |
| `pkg/runtime/`、`pkg/manager/` | 运行时托管与编排（见上） |
| `service/` | Wails3 服务绑定 |
| `frontend/` | React 前端（Ant Design） |

## 开发

前置：Go ≥ 1.25、Node ≥ 20、[Wails3](https://v3.wails.io) CLI。

```sh
# 前端绑定（改 Go 服务签名后需重新生成）
wails3 generate bindings -ts -i ./...

# 前端依赖与构建
cd frontend && npm install && npm run build

# 运行（开发模式）
wails3 dev

# Go 测试
go test ./pkg/...
```

数据目录：运行时会写入用户配置目录下的 `mediacraft/`（`runtimes/`、`models/`、
`cache/downloads/`）。

## 模型与第三方归属

- 模型/运行库二进制在运行时从其清单指定的上游下载（默认 ModelScope 镜像）。
- audio.cpp、sd.cpp 及其模型文件版权归其各自作者/许可证所有，使用前请遵循
  对应上游许可。
- 本项目代码基于 [MIT](LICENSE) 许可证开源。

## 状态

早期开发阶段（work in progress）。当前后端闭环已完成：安装/启动/停止/
状态/健康巡检均可用并带单元测试；前端为演示级页面。
