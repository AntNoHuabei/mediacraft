import { useCallback, useEffect, useMemo, useState } from 'react'
import { App as AntApp, Button, Drawer, Form, Input, InputNumber, Popconfirm, Popover, Select, Tooltip, message } from 'antd'
import {
  AppstoreOutlined,
  AudioOutlined,
  CloudDownloadOutlined,
  CopyOutlined,
  DeleteOutlined,
  DownloadOutlined,
  HistoryOutlined,
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
  parameters?: Record<string, unknown> | null
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

type ImageSize = { width: number; height: number }

// 常用画幅预设：像素均为 64 的倍数（适配 sd.cpp 分辨率约束）。
const ASPECT_PRESETS: { key: string; label: string; size: ImageSize }[] = [
  { key: '1:1', label: '1:1 方形', size: { width: 1024, height: 1024 } },
  { key: '3:2', label: '3:2 横版', size: { width: 1152, height: 768 } },
  { key: '4:3', label: '4:3 横版', size: { width: 1024, height: 768 } },
  { key: '16:9', label: '16:9 宽屏', size: { width: 1024, height: 576 } },
  { key: '21:9', label: '21:9 超宽', size: { width: 1344, height: 576 } },
  { key: '3:4', label: '3:4 竖版', size: { width: 768, height: 1024 } },
  { key: '2:3', label: '2:3 竖版', size: { width: 768, height: 1152 } },
  { key: '9:16', label: '9:16 长图', size: { width: 576, height: 1024 } },
]

const roundTo64 = (value: number): number => Math.max(64, Math.round(value / 64) * 64)

const aspectKeyOf = (width: number, height: number): string => {
  for (const p of ASPECT_PRESETS) {
    if (p.size.width === width && p.size.height === height) return p.key
  }
  return 'custom'
}

const paramNum = (value: unknown): number | undefined => {
  const n = Number(value)
  return Number.isFinite(n) && n > 0 ? n : undefined
}

// 按真实宽高比画一个小的形状预览，供画幅下拉直接目测比例。
function AspectGlyph({ width, height }: ImageSize) {
  const scale = Math.min(26 / width, 20 / height)
  const w = Math.max(4, Math.round(width * scale))
  const h = Math.max(4, Math.round(height * scale))
  return (
    <span
      aria-hidden
      style={{
        display: 'inline-flex',
        width: 30,
        flex: '0 0 auto',
        justifyContent: 'center',
        marginRight: 8,
        verticalAlign: 'middle',
      }}
    >
      <i
        style={{
          display: 'block',
          width: w,
          height: h,
          borderRadius: 3,
          backgroundColor: 'currentColor',
          opacity: 0.78,
        }}
      />
    </span>
  )
}

type ImageOutput = {
  id: string
  model: string
  prompt: string
  width: number
  height: number
  steps: number
  cfgScale: number
  seed: number
  createdAt: number
  thumb: string
}

type ImageOutputDetail = {
  image: string
  output: ImageOutput
}

type CurrentImage = {
  dataUrl: string
  meta?: ImageOutput
}

const fmtClock = (ms: number): string =>
  new Date(ms).toLocaleString('zh-CN', { hour12: false, month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' })

const thumbSrc = (thumb: string): string => `data:image/jpeg;base64,${thumb}`

function ImagePage({ models }: { models: Model[] }) {
  const imageModels = models.filter((m) => m.type === 'image-generation')
  const [form] = Form.useForm()
  const selectedModel = Form.useWatch('model', form) as string | undefined
  const stepsValue = Form.useWatch('steps', form) as number | undefined
  const seedValue = Form.useWatch('seed', form) as number | undefined
  const [aspect, setAspect] = useState('1:1')
  const [loading, setLoading] = useState(false)
  const [advanced, setAdvanced] = useState(false)
  const [view, setView] = useState<'canvas' | 'recent'>('recent')
  const [tab, setTab] = useState<'create' | 'outputs'>('create')
  const [allMode, setAllMode] = useState(false)
  const [outputs, setOutputs] = useState<ImageOutput[]>([])
  const [current, setCurrent] = useState<CurrentImage | null>(null)

  const loadOutputs = useCallback(async () => {
    try {
      const list = await ModelService.ListImageOutputs()
      setOutputs(Array.isArray(list) ? list : [])
    } catch {
      // 历史加载失败不阻塞工作台
    }
  }, [])
  useEffect(() => {
    void loadOutputs()
  }, [loadOutputs])

  // 选中模型后按清单默认参数（default_width/height/steps）填充表单。
  useEffect(() => {
    if (!selectedModel) return
    const model = imageModels.find((item) => item.name === selectedModel)
    const p = (model?.parameters ?? {}) as Record<string, unknown>
    const width = paramNum(p.default_width)
    const height = paramNum(p.default_height)
    if (width && height) {
      const w = roundTo64(width)
      const h = roundTo64(height)
      form.setFieldsValue({ width: w, height: h })
      setAspect(aspectKeyOf(w, h))
    }
    const steps = paramNum(p.default_steps)
    if (steps) form.setFieldsValue({ steps: Math.round(steps) })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [selectedModel])

  const applySize = (width: number, height: number) => {
    form.setFieldsValue({ width, height })
    setAspect(aspectKeyOf(width, height))
  }

  const pickAspect = (key: string) => {
    setAspect(key)
    if (key === 'custom') return
    const preset = ASPECT_PRESETS.find((p) => p.key === key)
    if (preset) form.setFieldsValue({ width: preset.size.width, height: preset.size.height })
  }

  const generate = async (values: {
    model: string
    prompt: string
    width?: number
    height?: number
    steps?: number
    seed?: number
  }) => {
    const model = imageModels.find((item) => item.name === values.model)
    if (!model?.installed) {
      message.warning('请先在模型库安装图片模型')
      return
    }
    if (!values.prompt || !values.prompt.trim()) {
      message.warning('请输入提示词')
      return
    }
    const width = roundTo64(Number(values.width) || 1024)
    const height = roundTo64(Number(values.height) || 1024)
    const steps = Math.max(1, Math.round(Number(values.steps) || 8))
    const seed = Math.floor(Number(values.seed))
    if (width !== Number(values.width) || height !== Number(values.height)) {
      applySize(width, height)
      message.info(`尺寸已对齐到 64 的倍数：${width} × ${height}`)
    }
    const payload: Record<string, unknown> = { model: values.model, prompt: values.prompt, width, height, steps }
    if (Number.isFinite(seed) && seed > 0) payload.seed = seed
    setView('canvas')
    setLoading(true)
    try {
      const b64 = await ModelService.GenerateImage(JSON.stringify(payload))
      setCurrent({ dataUrl: `data:image/png;base64,${b64}` })
      const list = await ModelService.ListImageOutputs()
      setOutputs(Array.isArray(list) ? list : [])
      const top = list?.[0]
      if (top) setCurrent({ dataUrl: `data:image/png;base64,${b64}`, meta: top })
      message.success('图片生成完成，已存入产物历史')
    } catch (error) {
      message.error(String(error))
    } finally {
      setLoading(false)
    }
  }

  const pickOutput = async (out: ImageOutput) => {
    try {
      const detail: ImageOutputDetail = await ModelService.GetImageOutput(out.id)
      setCurrent({ dataUrl: `data:image/png;base64,${detail.image}`, meta: detail.output })
      setView('canvas')
      setTab('create')
    } catch (error) {
      message.error(String(error))
    }
  }

  const removeOutput = async (out: ImageOutput) => {
    try {
      await ModelService.DeleteImageOutput(out.id)
      if (current?.meta?.id === out.id) {
        setCurrent(null)
        setView('recent')
      }
      setOutputs((prev) => prev.filter((item) => item.id !== out.id))
    } catch (error) {
      message.error(String(error))
    }
  }

  const reproduce = (out: ImageOutput) => {
    const model = imageModels.find((item) => item.name === out.model)
    if (!model?.installed) {
      message.warning('该产物所用模型未安装，无法重现')
      return
    }
    applySize(roundTo64(out.width), roundTo64(out.height))
    form.setFieldsValue({ model: out.model, prompt: out.prompt, steps: out.steps, seed: out.seed })
    setTab('create')
    message.success('参数已填回，可直接再次生成')
  }

  const copyPrompt = (prompt: string) => {
    const clip = navigator.clipboard
    if (clip) {
      clip.writeText(prompt).then(
        () => message.success('提示词已复制'),
        () => message.info(prompt),
      )
    } else {
      message.info(prompt)
    }
  }

  const downloadCurrent = () => {
    if (!current) return
    const a = document.createElement('a')
    a.href = current.dataUrl
    a.download = `mediacraft-${current.meta?.id ?? Date.now()}.png`
    a.click()
  }

  const modelName = (name: string): string => imageModels.find((m) => m.name === name)?.displayName ?? name
  const meta = current?.meta

  const renderThumb = (out: ImageOutput, large: boolean) => {
    const selected = current?.meta?.id === out.id
    return (
      <div className={large ? 'thumb thumb-lg' : `thumb${selected ? ' sel' : ''}`} key={out.id} title={out.prompt}>
        <button type="button" className="thumb-btn" onClick={() => void pickOutput(out)}>
          <img src={thumbSrc(out.thumb)} alt={out.prompt} loading="lazy" />
        </button>
        <div className="thumb-cap">
          {out.width}×{out.height} · {out.steps} 步
        </div>
        <span className="thumb-del">
          <Popconfirm title="删除该产物？" okText="删除" cancelText="取消" onConfirm={() => void removeOutput(out)}>
            <Button type="text" size="small" icon={<DeleteOutlined />} onClick={(e) => e.stopPropagation()} />
          </Popconfirm>
        </span>
      </div>
    )
  }

  const recent = outputs.slice(0, 12)

  const createPane = (
    <div className="pane-fill pane-create">
      {/* 画布 / 近期产物：同时只显示一个 */}
      {view === 'canvas' ? (
      <section className="panel create-canvas">
        <div className="canvas-head">
          <div className="eyebrow">CANVAS · 画布</div>
          {meta && (
            <span className="meta-line mono">
              {modelName(meta.model)} · {meta.width}×{meta.height} · {meta.steps} 步
              {meta.cfgScale > 0 ? ` · cfg ${meta.cfgScale}` : ''}
              {meta.seed > 0 ? ` · seed ${meta.seed}` : ''} · {fmtClock(meta.createdAt)}
            </span>
          )}
          {outputs.length > 0 && (
            <Button size="small" type="text" style={{ marginLeft: 'auto' }} icon={<HistoryOutlined />} onClick={() => setView('recent')}>
              近期产物（{outputs.length}）
            </Button>
          )}
        </div>

        <div className="canvas-stage">
          {loading ? (
            <div className="canvas-empty">
              <span className="canvas-spinner" />
              <p>正在生成…（首次约需十几秒）</p>
            </div>
          ) : current ? (
            <img className="canvas" src={current.dataUrl} alt="生成结果" />
          ) : (
            <div className="canvas-empty">
              <span className="big">
                <PictureOutlined />
              </span>
              <p>在下方输入 prompt，像给 agent 下指令一样创作</p>
              <p className="dim">出图自动存档，可在「产物」页签回看全部</p>
            </div>
          )}
        </div>

        {current && (
          <div className="canvas-actions">
            <Button size="small" icon={<DownloadOutlined />} onClick={downloadCurrent}>
              下载
            </Button>
            {meta ? (
              <>
                <Button size="small" icon={<CopyOutlined />} onClick={() => copyPrompt(meta.prompt)}>
                  复制提示词
                </Button>
                <Button size="small" icon={<ReloadOutlined />} onClick={() => reproduce(meta)}>
                  参数填回
                </Button>
                <Popconfirm title="删除当前产物？" okText="删除" cancelText="取消" onConfirm={() => void removeOutput(meta)}>
                  <Button size="small" danger icon={<DeleteOutlined />}>
                    删除
                  </Button>
                </Popconfirm>
              </>
            ) : (
              <span className="dim" style={{ fontSize: 12 }}>
                历史读取失败，未关联存档
              </span>
            )}
          </div>
        )}
      </section>
      ) : (
      <section className="panel create-recent browse">
        <div className="history-head">
          <span className="eyebrow">近期产物</span>
          <span className="dim" style={{ fontSize: 12 }}>
            {outputs.length ? `共 ${outputs.length} 张 · 生成后自动切到画布` : '还没有产物'}
          </span>
          {outputs.length > 0 && (
            <Button size="small" type="text" style={{ marginLeft: 'auto' }} icon={<HistoryOutlined />} onClick={() => setTab('outputs')}>
              查看全部
            </Button>
          )}
        </div>
        {outputs.length === 0 ? (
          <div className="history-empty dim">
            还没有生成过图片——在下方输入 prompt 开始创作，生成后自动切到画布回放结果。
          </div>
        ) : (
          <div className="out-grid">{outputs.map((out) => renderThumb(out, true))}</div>
        )}
      </section>
      )}

      {/* 底部：类 Codex 输入区 */}
      <section className="panel composer">
        <Form form={form} onFinish={(v) => void generate(v)}>
          <div className="composer-input">
            <Form.Item
              name="prompt"
              style={{ marginBottom: 0, flex: 1 }}
              rules={[{ required: true, message: '请输入提示词' }]}
            >
              <Input.TextArea
                className="composer-textarea"
                placeholder="输入 prompt，描述你想生成的画面…（Enter 生成 · Shift+Enter 换行）"
                autoSize={{ minRows: 5, maxRows: 12 }}
                onPressEnter={(e) => {
                  if (!e.shiftKey) {
                    e.preventDefault()
                    form.submit()
                  }
                }}
              />
            </Form.Item>
          </div>

          {/* 下面一排选项 */}
          <div className="composer-options">
            <span className="opt-chip">
              <span className="opt-label">模型</span>
              <Form.Item
                name="model"
                style={{ marginBottom: 0 }}
                rules={[{ required: true, message: '请选择图片模型' }]}
              >
                <Select
                  size="small"
                  style={{ minWidth: 180 }}
                  placeholder="选择模型"
                  options={imageModels.map((m) => ({
                    value: m.name,
                    label: `${m.displayName}${m.installed ? '' : '（未安装）'}`,
                    disabled: !m.installed,
                  }))}
                />
              </Form.Item>
            </span>

            <span className="opt-chip">
              <span className="opt-label">画幅</span>
              <Select
                size="small"
                style={{ width: 230 }}
                value={aspect}
                onChange={pickAspect}
                options={[
                  ...ASPECT_PRESETS.map((p) => ({
                    value: p.key,
                    label: (
                      <span style={{ display: 'inline-flex', alignItems: 'center' }}>
                        <AspectGlyph width={p.size.width} height={p.size.height} />
                        <span>
                          {p.label} · {p.size.width} × {p.size.height}
                        </span>
                      </span>
                    ),
                  })),
                  { value: 'custom', label: '自定义尺寸' },
                ]}
              />
            </span>

            {aspect === 'custom' ? (
              <span className="opt-chip">
                <span className="opt-label">宽</span>
                <Form.Item name="width" initialValue={1024} style={{ marginBottom: 0 }}>
                  <InputNumber size="small" min={64} max={2048} step={64} style={{ width: 92 }} />
                </Form.Item>
                <span className="opt-label" style={{ marginLeft: 6 }}>
                  高
                </span>
                <Form.Item name="height" initialValue={1024} style={{ marginBottom: 0 }}>
                  <InputNumber size="small" min={64} max={2048} step={64} style={{ width: 92 }} />
                </Form.Item>
              </span>
            ) : null}

            <Popover
              placement="top"
              trigger="click"
              open={advanced}
              onOpenChange={setAdvanced}
              content={
                <div className="adv-pop">
                  <span className="opt-label">步数</span>
                  <InputNumber
                    size="small"
                    min={1}
                    max={100}
                    value={stepsValue ?? 8}
                    onChange={(v) => form.setFieldsValue({ steps: v ?? 8 })}
                    style={{ width: 110 }}
                  />
                  <span className="opt-label" style={{ marginLeft: 10 }}>
                    种子
                  </span>
                  <InputNumber
                    size="small"
                    min={1}
                    max={9007199254740991}
                    precision={0}
                    placeholder="留空=随机"
                    value={seedValue}
                    onChange={(v) => form.setFieldsValue({ seed: v ?? undefined })}
                    style={{ width: 170 }}
                  />
                  <div className="dim" style={{ fontSize: 11, marginTop: 4, width: '100%' }}>
                    固定 seed 可精确重现同一张图
                  </div>
                </div>
              }
            >
              <Button className="opt-toggle" size="small" type={advanced ? 'primary' : 'default'}>
                高级参数 {advanced ? '▴' : '▾'}
              </Button>
            </Popover>
            <span style={{ flex: 1 }} />
            <Button type='primary' htmlType='submit' loading={loading} icon={<PictureOutlined />}>
              生成
            </Button>
          </div>


        </Form>
      </section>
    </div>
  )


  const outputsPane = (
    <div className="pane-fill pane-outputs">
      <section className="panel outputs-area">
        <div className="history-head">
          <span className="eyebrow">{allMode ? '全部产物' : '最近产物'}</span>
          <span className="dim" style={{ fontSize: 12 }}>
            共 {outputs.length} 张 · 点击回看并切回创作
          </span>
          <div style={{ marginLeft: 'auto', display: 'flex', gap: 8 }}>
            <Button size="small" type="text" onClick={() => setAllMode((v) => !v)}>
              {allMode ? '只看最近 ▴' : '查看全部 ▾'}
            </Button>
            <Button size="small" type="text" icon={<PictureOutlined />} onClick={() => setTab('create')}>
              回到创作
            </Button>
          </div>
        </div>

        {outputs.length === 0 ? (
          <div className="history-empty dim">
            暂无产物——在「创作」页生成图片后会自动出现在这里，重启应用也不丢失。
          </div>
        ) : allMode ? (
          <div className="out-grid">{outputs.map((out) => renderThumb(out, true))}</div>
        ) : (
          <div className="filmstrip">{recent.map((out) => renderThumb(out, false))}</div>
        )}
      </section>
    </div>
  )

  return (
    <div className="img-page">
      <PageHead eyebrow="IMAGE BUS · sd.cpp" title="图片工作台" desc="创作与产物分页：像给 agent 下指令一样输入 prompt，画布实时出图。" />
      <div className="image-workspace">
        <div className="img-tabs">
          <div className="img-tabbar">
            <button type="button" className={tab === 'create' ? 'img-tab on' : 'img-tab'} onClick={() => setTab('create')}>
              创作
            </button>
            <button
              type="button"
              className={tab === 'outputs' ? 'img-tab on' : 'img-tab'}
              onClick={() => setTab('outputs')}
            >
              产物{outputs.length > 0 ? `（${outputs.length}）` : ''}
            </button>
            <span className="dim" style={{ fontSize: 11, marginLeft: 'auto' }}>
              Enter 生成 · Shift+Enter 换行
            </span>
          </div>
          <div className="img-panels">{tab === 'create' ? createPane : outputsPane}</div>
        </div>
      </div>
    </div>
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
                <InputNumber min={0.1} max={3} step={0.1} style={{ width: 130 }} />
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
