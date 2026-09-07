import { useEffect, useMemo, useState } from 'react'
import { App as AntApp, Button, Drawer, Form, Input, Select, Tooltip, message } from 'antd'
import {
  AppstoreOutlined,
  AudioOutlined,
  CloudDownloadOutlined,
  MoonOutlined,
  PictureOutlined,
  ReloadOutlined,
  SettingOutlined,
  StopOutlined,
  SunOutlined,
  VideoCameraOutlined,
} from '@ant-design/icons'
import { ModelService } from '../bindings/github.com/AntNoHuabei/mediacraft/service'
import { Events, Window } from '@wailsio/runtime'
import { useTheme } from './theme'

const APP_VERSION = '0.1.0'

type Model = {
  name: string
  displayName: string
  type: string
  description: string
  capabilities: string[] | null
  tags: string[] | null
  runtimes: string[] | null
  version: string
  installed: boolean
  status: string
}
type Runtime = {
  name: string
  displayName: string
  description: string
  version: string
  installed: boolean
  status: string
  vendor: string
  backend: string
}

const labels: Record<string, string> = { 'image-generation': '图片', tts: 'TTS', asr: 'ASR' }

type InstallInfo = {
  kind: 'runtime' | 'model'
  name: string
  status: string
  stage: string
  progress: number
  message?: string
  bytes_done?: number
  bytes_total?: number
  speed?: number
}

type ModelState = {
  name: string
  engine: string
  run_status: string
  port: number
  health_error?: string
}

const stageLabel = (stage?: string): string => {
  switch (stage) {
    case 'preparing':
      return '准备中'
    case 'downloading':
      return '下载中'
    case 'verifying':
      return '校验中'
    case 'extracting':
      return '解压中'
    case 'installing':
      return '安装中'
    case 'completed':
      return '完成'
    default:
      return stage ?? '安装中'
  }
}

const fmtSpeed = (bytesPerSec?: number): string => {
  const value = Number(bytesPerSec) || 0
  if (value <= 0) return ''
  if (value >= 1048576) return `${(value / 1048576).toFixed(1)} MB/s`
  if (value >= 1024) return `${(value / 1024).toFixed(1)} KB/s`
  return `${Math.round(value)} B/s`
}

function InstallMeter({ info, mini }: { info: InstallInfo; mini?: boolean }) {
  const progress = Math.max(0, Math.min(100, Number(info.progress) || 0))
  return (
    <div className={mini ? 'install-line mini' : 'install-line'}>
      <div className="meter">
        <i style={{ width: `${progress}%` }} />
      </div>
      <span className="meter-caption mono">
        {stageLabel(info.stage)} {Math.round(progress)}%
        {info.speed ? ` · ${fmtSpeed(info.speed)}` : ''}
      </span>
    </div>
  )
}

const typeAccent = (type: string): string => {
  if (type === 'image-generation') return 'image'
  if (type === 'tts' || type === 'asr') return 'audio'
  return 'video'
}

/* ---------------- 通用小组件 ---------------- */

function PageHead({ eyebrow, title, desc, extra }: { eyebrow: string; title: string; desc?: string; extra?: React.ReactNode }) {
  return (
    <header className="page-head">
      <div className="page-head-left">
        <span className="eyebrow">{eyebrow}</span>
        <h2 className="page-title">{title}</h2>
        {desc && <p className="page-desc">{desc}</p>}
      </div>
      {extra}
    </header>
  )
}

/* ---------------- 自定义标题栏（无边框窗口） ---------------- */

const win = (fn: () => Promise<void>) => {
  try {
    fn().catch(() => {
      /* 浏览器预览等无窗口环境下静默 */
    })
  } catch {
    /* ignore */
  }
}

function TitleBar() {
  const [maximized, setMaximized] = useState(false)
  const minimise = () => win(() => Window.Minimise())
  const toggleMax = () =>
    win(() =>
      Window.ToggleMaximise().then(async () => {
        try {
          setMaximized(await Window.IsMaximised())
        } catch {
          /* ignore */
        }
      }),
    )
  const close = () => win(() => Window.Close())

  return (
    <header className="titlebar" onDoubleClick={toggleMax}>
      <div className="titlebar-title mono">
        <span className="lamp on" />
        MediaCraft Studio
      </div>
      <div className="titlebar-controls">
        <button className="win-btn" onClick={minimise} aria-label="最小化" title="最小化">
          <svg width="10" height="10" viewBox="0 0 10 10" aria-hidden="true">
            <path d="M0 4.5h10v1H0z" fill="currentColor" />
          </svg>
        </button>
        <button className="win-btn" onClick={toggleMax} aria-label={maximized ? '还原' : '最大化'} title={maximized ? '还原' : '最大化'}>
          {maximized ? (
            <svg width="10" height="10" viewBox="0 0 10 10" aria-hidden="true">
              <path d="M2.5 2.5V0h7.5v7.5h-2.5" fill="none" stroke="currentColor" />
              <rect x="0" y="2.5" width="7.5" height="7.5" fill="none" stroke="currentColor" />
            </svg>
          ) : (
            <svg width="10" height="10" viewBox="0 0 10 10" aria-hidden="true">
              <rect x="1" y="1" width="8" height="8" fill="none" stroke="currentColor" />
            </svg>
          )}
        </button>
        <button className="win-btn win-close" onClick={close} aria-label="关闭" title="关闭">
          <svg width="10" height="10" viewBox="0 0 10 10" aria-hidden="true">
            <path d="M1 1l8 8M9 1l-8 8" stroke="currentColor" strokeWidth="1.2" />
          </svg>
        </button>
      </div>
    </header>
  )
}

/* ---------------- 模型库：运行时母线 + 模型轨道 ---------------- */

function ModelsPage({
  models,
  runtimes,
  refresh,
  installs,
  sync,
}: {
  models: Model[]
  runtimes: Runtime[]
  refresh: () => void
  installs: Record<string, InstallInfo>
  sync: (kind: 'runtime' | 'model', name: string, installed: boolean) => void
}) {
  const [filter, setFilter] = useState('all')
  const [busy, setBusy] = useState<string | null>(null)
  const shown = filter === 'all' ? models : models.filter((model) => model.type === filter)

  const install = (model: Model) => {
    setBusy(model.name)
    ModelService.InstallModel(model.name)
      .then(() => {
        sync('model', model.name, true)
        message.success(`${model.displayName} 安装完成`)
      })
      .catch((error) => message.error(String(error)))
      .finally(() => {
        setBusy(null)
        refresh()
      })
  }
  const uninstall = (model: Model) => {
    setBusy(model.name)
    ModelService.UninstallModel(model.name)
      .then(() => {
        sync('model', model.name, false)
        message.success(`${model.displayName} 已卸载`)
      })
      .catch((error) => message.error(String(error)))
      .finally(() => {
        setBusy(null)
        refresh()
      })
  }
  const installRuntime = (runtime: Runtime) => {
    setBusy(runtime.name)
    ModelService.InstallRuntime(runtime.name)
      .then(() => {
        sync('runtime', runtime.name, true)
        message.success(`${runtime.displayName} 安装完成`)
      })
      .catch((error) => message.error(String(error)))
      .finally(() => {
        setBusy(null)
        refresh()
      })
  }
  const uninstallRuntime = (runtime: Runtime) => {
    setBusy(runtime.name)
    ModelService.UninstallRuntime(runtime.name)
      .then(() => {
        sync('runtime', runtime.name, false)
        message.success(`${runtime.displayName} 已卸载`)
      })
      .catch((error) => message.error(String(error)))
      .finally(() => {
        setBusy(null)
        refresh()
      })
  }

  return (
    <>
      <PageHead
        eyebrow="MODEL LIBRARY · LOCAL BUSES"
        title="模型库"
        desc="本地推理引擎作为母线挂载，模型作为轨道按需装载。"
        extra={
          <Select
            size="small"
            value={filter}
            onChange={setFilter}
            style={{ width: 130 }}
            options={[{ value: 'all', label: '全部轨道' }, ...Object.entries(labels).map(([value, label]) => ({ value, label }))]}
          />
        }
      />

      {/* 母线：运行时 */}
      <div className="bus-grid">
        {runtimes.map((runtime) => {
          const installed = runtime.installed
          const progress = installs[`runtime:${runtime.name}`]
          return (
            <section className="panel bus-card" key={runtime.name}>
              <div className="bus-head">
                <span className={`lamp ${installed ? 'on' : ''}`} />
                <span className="bus-name">{runtime.name}</span>
                {installed && <span className="tag-mini filled">ONLINE</span>}
              </div>
              <div className="bus-meta">
                <span className="tag-mini mono">{runtime.vendor}</span>
                <span className="tag-mini mono">{runtime.backend ? runtime.backend.toUpperCase() : '-'}</span>
                <span className="tag-mini mono">v{runtime.version || '-'}</span>
              </div>
              <p className="bus-desc">{runtime.description || runtime.displayName}</p>
              {progress && <InstallMeter info={progress} />}
              <div className="bus-foot">
                <span className="row-status mono">{installed ? '已挂载' : '未挂载'}</span>
                <Button
                  size="small"
                  danger={installed}
                  loading={busy === runtime.name}
                  icon={<CloudDownloadOutlined />}
                  onClick={() => (installed ? uninstallRuntime(runtime) : installRuntime(runtime))}
                >
                  {installed ? '卸载引擎' : '安装引擎'}
                </Button>
              </div>
            </section>
          )
        })}
      </div>

      {/* 轨道：模型 */}
      <section className="panel">
        <div className="track-head">
          <span>轨</span>
          <span>模型</span>
          <span>引擎 / 能力</span>
          <span>版本</span>
          <span>状态</span>
          <span style={{ textAlign: 'right' }}>操作</span>
        </div>
        <div className="track-list">
          {shown.map((model, index) => {
            const installed = model.installed
            const running = model.status === 'running'
            const progress = installs[`model:${model.name}`]
            return (
              <div className="track-row" key={model.name}>
                <span className="track-no">{String(index + 1).padStart(2, '0')}</span>
                <div className="track-name">
                  <b>
                    <span className={`accent-dot ${typeAccent(model.type)}`} style={{ marginRight: 8 }} />
                    {model.displayName || model.name}
                  </b>
                  <span>{model.description || '本地推理模型'}</span>
                  {progress && <InstallMeter info={progress} mini />}
                </div>
                <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap' }}>
                  {(model.runtimes ?? []).map((engine) => (
                    <span className="tag-mini" key={engine}>
                      {engine}
                    </span>
                  ))}
                </div>
                <span className="mono" style={{ color: 'var(--text-dim)', fontSize: 12 }}>
                  v{model.version}
                </span>
                <div className="row-status">
                  <span className={`lamp ${installed ? (running ? 'on' : 'warn') : ''}`} />
                  {running ? '运行中' : installed ? '已装载' : '可安装'}
                </div>
                <div style={{ display: 'flex', justifyContent: 'flex-end' }}>
                  <Button
                    size="small"
                    danger={installed}
                    loading={busy === model.name}
                    onClick={() => (installed ? uninstall(model) : install(model))}
                  >
                    {installed ? '卸载' : '安装'}
                  </Button>
                </div>
              </div>
            )
          })}
          {!shown.length && (
            <div className="empty-console">
              <span className="big">
                <AppstoreOutlined />
              </span>
              <span>没有匹配的模型轨道</span>
            </div>
          )}
        </div>
      </section>
    </>
  )
}

/* ---------------- 图片处理 ---------------- */

function ImagePage({ models }: { models: Model[] }) {
  const imageModels = models.filter((m) => m.type === 'image-generation')
  const [image, setImage] = useState('')
  const [loading, setLoading] = useState(false)

  const generate = (values: { model: string; prompt: string; width: number; height: number; steps: number }) => {
    const model = imageModels.find((item) => item.name === values.model)
    if (!model?.installed) {
      message.warning('请先在模型库安装图片模型')
      return
    }
    setLoading(true)
    ModelService.GenerateImage(JSON.stringify(values))
      .then((result) => {
        setImage(result)
        message.success('图片生成完成')
      })
      .catch((error) => message.error(String(error)))
      .finally(() => setLoading(false))
  }

  return (
    <>
      <PageHead eyebrow="IMAGE BUS · sd.cpp" title="图片处理" desc="文生图：结果回到暗房画布回放。" />
      <section className="panel" style={{ padding: 18, maxWidth: 780 }}>
        <Form layout="vertical" onFinish={generate}>
          <Form.Item label="模型" name="model" rules={[{ required: true, message: '请选择图片模型' }]}>
            <Select
              placeholder="选择已装载的图片模型"
              options={imageModels.map((m) => ({
                value: m.name,
                label: `${m.displayName}${m.installed ? '' : '（未安装）'}`,
                disabled: !m.installed,
              }))}
            />
          </Form.Item>
          <Form.Item label="提示词" name="prompt" rules={[{ required: true, message: '请输入提示词' }]}>
            <Input.TextArea rows={5} placeholder="描述你想生成的画面" />
          </Form.Item>
          <div style={{ display: 'flex', gap: 16, flexWrap: 'wrap' }}>
            <Form.Item label="宽度" name="width" initialValue={1024}>
              <Input type="number" style={{ width: 130 }} />
            </Form.Item>
            <Form.Item label="高度" name="height" initialValue={1024}>
              <Input type="number" style={{ width: 130 }} />
            </Form.Item>
            <Form.Item label="步数" name="steps" initialValue={9}>
              <Input type="number" style={{ width: 130 }} />
            </Form.Item>
          </div>
          <Button type="primary" htmlType="submit" loading={loading} icon={<PictureOutlined />}>
            生成图片
          </Button>
        </Form>
      </section>
      {image && (
        <section className="panel" style={{ padding: 16, maxWidth: 900 }}>
          <div className="eyebrow" style={{ marginBottom: 10 }}>
            OUTPUT
          </div>
          <img className="canvas" src={`data:image/png;base64,${image}`} alt="生成结果" />
        </section>
      )}
    </>
  )
}

/* ---------------- 音频处理 ---------------- */

function AudioPage({ models }: { models: Model[] }) {
  const [mode, setMode] = useState<'tts' | 'asr'>('tts')
  const [audio, setAudio] = useState('')
  const [transcript, setTranscript] = useState('')
  const [loading, setLoading] = useState(false)
  const [selectedFile, setSelectedFile] = useState<File | null>(null)
  const ttsModels = models.filter((m) => m.type === 'tts')
  const asrModels = models.filter((m) => m.type === 'asr')

  const synthesize = (values: { model: string; text: string; speaker?: string; speed?: number }) => {
    const model = ttsModels.find((item) => item.name === values.model)
    if (!model?.installed) {
      message.warning('请先在模型库安装 TTS 模型')
      return
    }
    setLoading(true)
    ModelService.Synthesize(JSON.stringify(values))
      .then(setAudio)
      .then(() => message.success('音频合成完成'))
      .catch((error) => message.error(String(error)))
      .finally(() => setLoading(false))
  }

  const transcribe = async (values: { model: string }) => {
    const model = asrModels.find((item) => item.name === values.model)
    if (!model?.installed) {
      message.warning('请先在模型库安装 ASR 模型')
      return
    }
    if (!selectedFile) {
      message.warning('请选择音频文件')
      return
    }
    if (selectedFile.size > 32 * 1024 * 1024) {
      message.error('音频文件不能超过 32 MB')
      return
    }
    setLoading(true)
    const reader = new FileReader()
    reader.onload = () => {
      ModelService.Transcribe(
        JSON.stringify({ model: values.model, audio: reader.result, format: selectedFile.name.split('.').pop() }),
      )
        .then((result) => {
          setTranscript(result.text)
          message.success('识别完成')
        })
        .catch((error) => message.error(String(error)))
        .finally(() => setLoading(false))
    }
    reader.onerror = () => {
      setLoading(false)
      message.error('读取音频文件失败')
    }
    reader.readAsDataURL(selectedFile)
  }

  return (
    <>
      <PageHead eyebrow="AUDIO BUS · audio.cpp" title="音频处理" desc="文本转语音与语音识别共用一条总线。" />
      <div style={{ display: 'flex', gap: 10 }}>
        <Button size="small" type={mode === 'tts' ? 'primary' : 'default'} onClick={() => setMode('tts')}>
          文本转语音
        </Button>
        <Button size="small" type={mode === 'asr' ? 'primary' : 'default'} onClick={() => setMode('asr')}>
          语音识别
        </Button>
      </div>

      {mode === 'tts' ? (
        <section className="panel" style={{ padding: 18, maxWidth: 780 }}>
          <Form layout="vertical" onFinish={synthesize}>
            <Form.Item label="模型" name="model" rules={[{ required: true, message: '请选择 TTS 模型' }]}>
              <Select
                placeholder="选择已装载的 TTS 模型"
                options={ttsModels.map((m) => ({
                  value: m.name,
                  label: `${m.displayName}${m.installed ? '' : '（未安装）'}`,
                  disabled: !m.installed,
                }))}
              />
            </Form.Item>
            <Form.Item label="文本" name="text" rules={[{ required: true, message: '请输入文本' }]}>
              <Input.TextArea rows={6} placeholder="输入要合成的文本" />
            </Form.Item>
            <div style={{ display: 'flex', gap: 16, flexWrap: 'wrap' }}>
              <Form.Item label="音色" name="speaker">
                <Input placeholder="例如：vivian" style={{ width: 220 }} />
              </Form.Item>
              <Form.Item label="语速" name="speed" initialValue={1}>
                <Input type="number" style={{ width: 130 }} />
              </Form.Item>
            </div>
            <Button type="primary" htmlType="submit" loading={loading} icon={<AudioOutlined />}>
              合成音频
            </Button>
          </Form>
          {audio && <audio controls src={audio} style={{ width: '100%', marginTop: 20 }} />}
        </section>
      ) : (
        <section className="panel" style={{ padding: 18, maxWidth: 780 }}>
          <Form layout="vertical" onFinish={transcribe}>
            <Form.Item label="模型" name="model" rules={[{ required: true, message: '请选择 ASR 模型' }]}>
              <Select
                placeholder="选择已装载的 ASR 模型"
                options={asrModels.map((m) => ({
                  value: m.name,
                  label: `${m.displayName}${m.installed ? '' : '（未安装）'}`,
                  disabled: !m.installed,
                }))}
              />
            </Form.Item>
            <Form.Item label="音频文件">
              <input
                type="file"
                accept="audio/*"
                onChange={(event) => setSelectedFile(event.target.files?.[0] ?? null)}
              />
            </Form.Item>
            <Button type="primary" htmlType="submit" loading={loading}>
              开始识别
            </Button>
          </Form>
          {transcript && (
            <div style={{ marginTop: 18 }}>
              <div className="eyebrow">TRANSCRIPT</div>
              <pre style={{ whiteSpace: 'pre-wrap', margin: '8px 0 0', color: 'var(--text-dim)' }}>{transcript}</pre>
            </div>
          )}
        </section>
      )}
    </>
  )
}

/* ---------------- 视频处理（占位） ---------------- */

function VideoPage() {
  return (
    <>
      <PageHead eyebrow="VIDEO BUS · ROADMAP" title="视频处理" desc="视频轨道将接入混音台时间线。" />
      <div className="empty-console">
        <span className="big">
          <VideoCameraOutlined />
        </span>
        <b>视频总线尚未接入</b>
        <span style={{ maxWidth: 440, textAlign: 'center', lineHeight: 1.7 }}>
          规划路线：帧序列 / 文生视频 / 时间线剪辑——沿用"多轨 + 走带"控制台语言，与图像、音频轨道同台混编。
        </span>
      </div>
    </>
  )
}

/* ---------------- 外壳 ---------------- */

type PageKey = 'models' | 'image' | 'audio' | 'video'

function AppContent() {
  const { theme, toggle } = useTheme()
  const [models, setModels] = useState<Model[]>([])
  const [runtimes, setRuntimes] = useState<Runtime[]>([])
  const [installs, setInstalls] = useState<Record<string, InstallInfo>>({})
  const [active, setActive] = useState<PageKey>('models')
  const [settingsOpen, setSettingsOpen] = useState(false)

  const refresh = () => {
    ModelService.ListModels('all')
      .then((items) => setModels(items ?? []))
      .catch(() => message.error('加载模型目录失败'))
    ModelService.ListRuntimes()
      .then((items) => setRuntimes(items ?? []))
      .catch(() => message.error('加载运行时目录失败'))
  }
  // 安装/卸载成功后立即本地同步（refresh 前的即时反馈，避免刷新失败时状态滞留）
  const syncInstalled = (kind: 'runtime' | 'model', name: string, installed: boolean) => {
    if (kind === 'runtime') {
      setRuntimes((prev) =>
        prev.map((r) => (r.name === name ? { ...r, installed, status: installed ? 'installed' : 'available' } : r)),
      )
    } else {
      setModels((prev) =>
        prev.map((m) =>
          m.name === name
            ? { ...m, installed, status: installed ? (m.status === 'running' ? 'running' : 'installed') : 'available' }
            : m,
        ),
      )
    }
  }

  useEffect(() => {
    refresh()
    const offInstall = Events.On('mc:install', (ev: any) => {
      const p = ev?.data as InstallInfo
      if (!p || typeof p !== 'object' || !p.kind || !p.name) return
      const key = `${p.kind}:${p.name}`
      setInstalls((prev) => {
        const next = { ...prev }
        if (p.status === 'installed' || p.status === 'error') {
          delete next[key]
        } else {
          next[key] = { ...p, progress: Number(p.progress) || 0 }
        }
        return next
      })
      if (p.status === 'installed') refresh()
    })
    const offModel = Events.On('mc:model', (ev: any) => {
      const state = ev?.data as ModelState
      if (!state || typeof state !== 'object' || !state.name) return
      setModels((prev) => prev.map((m) => (m.name === state.name ? { ...m, status: state.run_status } : m)))
    })
    return () => {
      offInstall()
      offModel()
    }
  }, [])

  const nav = useMemo<Array<{ key: PageKey; icon: React.ReactNode; label: string }>>(
    () => [
      { key: 'models', icon: <AppstoreOutlined />, label: '模型库' },
      { key: 'image', icon: <PictureOutlined />, label: '图片' },
      { key: 'audio', icon: <AudioOutlined />, label: '音频' },
      { key: 'video', icon: <VideoCameraOutlined />, label: '视频' },
    ],
    [],
  )

  const runningModels = models.filter((m) => m.status === 'running')
  const stopAllRunning = () => {
    if (!runningModels.length) return
    Promise.all(runningModels.map((m) => ModelService.StopModel(m.name)))
      .then(() => {
        message.success('已停止运行中的模型')
        refresh()
      })
      .catch((error) => message.error(String(error)))
  }

  const QQ_GROUP = '488797113'
  const copyQQGroup = () => {
    navigator.clipboard
      ?.writeText(QQ_GROUP)
      .then(() => message.success(`QQ 群号已复制：${QQ_GROUP}，去 QQ 搜索添加即可`))
      .catch(() => message.warning(`复制失败，请手动添加 QQ 群号：${QQ_GROUP}`))
  }

  const pageTitle: Record<PageKey, string> = { models: '模型库', image: '图片处理', audio: '音频处理', video: '视频处理' }

  return (
    <div className="window-root">
      <TitleBar />
      <div className="console">
      <aside className="console-rail">
        <div className="rail-brand">MC</div>
        <nav className="rail-nav">
          {nav.map((item) => (
            <Tooltip key={item.key} title={item.label} placement="right">
              <button
                className={`rail-btn ${active === item.key ? 'active' : ''}`}
                onClick={() => setActive(item.key)}
                aria-label={item.label}
              >
                {item.icon}
              </button>
            </Tooltip>
          ))}
        </nav>
        <div className="rail-actions">
          <Tooltip title={theme === 'dark' ? '切换浅色主题' : '切换深色主题'} placement="right">
            <button className="rail-btn" onClick={toggle} aria-label="主题">
              {theme === 'dark' ? <SunOutlined /> : <MoonOutlined />}
            </button>
          </Tooltip>
          <Tooltip title="设置" placement="right">
            <button className="rail-btn" onClick={() => setSettingsOpen(true)} aria-label="设置">
              <SettingOutlined />
            </button>
          </Tooltip>
        </div>
      </aside>

      <section className="console-main">
        <header className="console-top">
          <span className="top-title">{pageTitle[active]}</span>
          <span className="transport-spacer" />
          {runtimes.map((runtime) => (
            <span className="chip mono" key={runtime.name}>
              <span className={`lamp ${runtime.installed ? 'on' : ''}`} />
              {runtime.name}
              <span style={{ color: 'var(--text-faint)' }}>
                {runtime.installed ? (runtime.backend ? runtime.backend.toUpperCase() : '') : 'OFFLINE'}
              </span>
            </span>
          ))}
          <button className="icon-btn" onClick={refresh} title="刷新" aria-label="刷新">
            <ReloadOutlined />
          </button>
        </header>

        <main className="stage">
          {active === 'models' && (
            <ModelsPage models={models} runtimes={runtimes} refresh={refresh} installs={installs} sync={syncInstalled} />
          )}
          {active === 'image' && <ImagePage models={models} />}
          {active === 'audio' && <AudioPage models={models} />}
          {active === 'video' && <VideoPage />}
        </main>

        <footer className="transport">
          <div className="transport-group">
            <span className={`lamp ${runningModels.length ? 'on' : ''}`} style={{ width: 12, height: 12 }} />
            <span className="chip mono">BUS · {runningModels.length ? `RUNNING ×${runningModels.length}` : 'IDLE'}</span>
          </div>
          <Button size="small" danger disabled={!runningModels.length} icon={<StopOutlined />} onClick={stopAllRunning}>
            全部停止
          </Button>
          <div className="transport-spacer" />
          <button type="button" className="chip chip-link mono" onClick={copyQQGroup} title="复制群号，去 QQ 添加">
            QQ 交流群 {QQ_GROUP}
          </button>
          <span className="chip mono" style={{ color: 'var(--text-dim)' }}>
            v{APP_VERSION}
          </span>
        </footer>
        </section>
      </div>
      <Drawer title="设置" placement="right" width={320} open={settingsOpen} onClose={() => setSettingsOpen(false)}>
        <div className="setting-group">
          <div className="eyebrow">外观</div>
          <div className="setting-row">
            <span>主题</span>
            <Button size="small" onClick={toggle}>
              {theme === 'dark' ? '切换到浅色' : '切换到深色'}
            </Button>
          </div>
        </div>
        <div className="setting-group">
          <div className="eyebrow">关于</div>
          <div className="setting-row">
            <span>名称</span>
            <span>MediaCraft Studio</span>
          </div>
          <div className="setting-row">
            <span>版本</span>
            <span className="mono">v{APP_VERSION}</span>
          </div>
          <div className="setting-row">
            <span>引擎</span>
            <span className="mono">audio.cpp · sd.cpp</span>
          </div>
          <div className="setting-row">
            <span>数据目录</span>
            <span className="mono">…\mediacraft</span>
          </div>
          <div className="setting-row">
            <span>开发交流群</span>
            <Button size="small" onClick={copyQQGroup}>
              复制群号 {QQ_GROUP}
            </Button>
          </div>
        </div>
      </Drawer>
    </div>
  )
}

export default function App() {
  return (
    <AntApp>
      <AppContent />
    </AntApp>
  )
}
