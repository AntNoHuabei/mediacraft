import { useEffect, useMemo, useState } from 'react'
import { App as AntApp, Button, Card, Empty, Form, Input, Layout, Menu, Select, Space, Tag, Typography, message } from 'antd'
import { AudioOutlined, CloudDownloadOutlined, PictureOutlined, SettingOutlined, VideoCameraOutlined } from '@ant-design/icons'
import { ModelService } from '../bindings/github.com/AntNoHuabei/mediacraft/service'

type Model = { name: string; displayName: string; type: string; description: string; capabilities: string[] | null; tags: string[] | null; runtimes: string[] | null; version: string; installed: boolean; status: string }
type Runtime = { name: string; displayName: string; description: string; version: string; installed: boolean; status: string; vendor: string; backend: string }
const labels: Record<string, string> = { 'image-generation': '图片生成', tts: '文本转语音', asr: '语音识别' }

function ModelsPage({ models, runtimes, refresh }: { models: Model[]; runtimes: Runtime[]; refresh: () => void }) {
  const [filter, setFilter] = useState('all')
  const [busy, setBusy] = useState<string | null>(null)
  const shown = filter === 'all' ? models : models.filter((model) => model.type === filter)
  const install = (model: Model) => { setBusy(model.name); ModelService.InstallModel(model.name).then(() => message.success(`${model.displayName} 安装完成`)).catch((error) => message.error(String(error))).finally(() => { setBusy(null); refresh() }) }
  const uninstall = (model: Model) => { setBusy(model.name); ModelService.UninstallModel(model.name).then(() => message.success(`${model.displayName} 已卸载`)).catch((error) => message.error(String(error))).finally(() => { setBusy(null); refresh() }) }
  const installRuntime = (runtime: Runtime) => { setBusy(runtime.name); ModelService.InstallRuntime(runtime.name).then(() => message.success(`${runtime.displayName} 安装完成`)).catch((error) => message.error(String(error))).finally(() => { setBusy(null); refresh() }) }
  const uninstallRuntime = (runtime: Runtime) => { setBusy(runtime.name); ModelService.UninstallRuntime(runtime.name).then(() => message.success(`${runtime.displayName} 已卸载`)).catch((error) => message.error(String(error))).finally(() => { setBusy(null); refresh() }) }
  return <Space direction="vertical" size="large" style={{ width: '100%' }}>
    <Space align="center" wrap><Typography.Title level={2} style={{ margin: 0 }}>模型管理</Typography.Title><Button onClick={refresh}>刷新</Button><Select value={filter} onChange={setFilter} options={[{ value: 'all', label: '全部模型' }, ...Object.entries(labels).map(([value, label]) => ({ value, label }))]} /></Space>
    <Typography.Text type="secondary">模型和本地推理引擎均可按需安装。</Typography.Text>
    <div className="runtime-grid">{runtimes.map((runtime) => <Card key={runtime.name} size="small" title={runtime.displayName} extra={<Tag color={runtime.installed ? 'success' : 'default'}>{runtime.installed ? '已安装' : '未安装'}</Tag>}><Typography.Text type="secondary">{runtime.description}</Typography.Text><div><Tag>{runtime.vendor}</Tag><Tag>{runtime.backend}</Tag></div><div className="model-card-footer"><Typography.Text type="secondary">v{runtime.version || '-'}</Typography.Text><Button size="small" loading={busy === runtime.name} onClick={() => runtime.installed ? uninstallRuntime(runtime) : installRuntime(runtime)}>{runtime.installed ? '卸载引擎' : '安装引擎'}</Button></div></Card>)}</div>
    <div className="model-grid">{shown.map((model) => <Card key={model.name} title={model.displayName} extra={<Tag color={model.type === 'image-generation' ? 'blue' : 'purple'}>{labels[model.type] ?? model.type}</Tag>}><Typography.Paragraph type="secondary" ellipsis={{ rows: 2 }}>{model.description || '本地推理模型'}</Typography.Paragraph><Space wrap>{(model.runtimes ?? []).map((runtime) => <Tag key={runtime}>{runtime}</Tag>)}{(model.capabilities ?? []).map((capability) => <Tag key={capability} bordered={false}>{capability}</Tag>)}</Space><div className="model-card-footer"><Space><Typography.Text type="secondary">v{model.version}</Typography.Text><Tag color={model.installed ? 'success' : 'default'}>{model.installed ? '已安装' : '未安装'}</Tag></Space><Button loading={busy === model.name} icon={<CloudDownloadOutlined />} onClick={() => model.installed ? uninstall(model) : install(model)}>{model.installed ? '卸载' : '安装'}</Button></div></Card>)}</div>
    {!shown.length && <Empty description="没有匹配的模型" />}
  </Space>
}

function ImagePage({ models }: { models: Model[] }) {
  const imageModels = models.filter((m) => m.type === 'image-generation')
  const [image, setImage] = useState('')
  const [loading, setLoading] = useState(false)
  const generate = (values: { model: string; prompt: string; width: number; height: number; steps: number }) => {
    const model = imageModels.find((item) => item.name === values.model)
    if (!model?.installed) { message.warning('请先在模型管理中安装图片模型'); return }
    setLoading(true)
    ModelService.GenerateImage(JSON.stringify(values)).then((result) => { setImage(result); message.success('图片生成完成') }).catch((error) => message.error(String(error))).finally(() => setLoading(false))
  }
  return <Space direction="vertical" size="large" style={{ width: '100%' }}><Card title="图片处理" extra={<Tag color="blue">sd.cpp</Tag>}><Form layout="vertical" onFinish={generate}><Form.Item label="模型" name="model" rules={[{ required: true, message: '请选择图片模型' }]}><Select placeholder="选择已安装的图片模型" options={imageModels.map((m) => ({ value: m.name, label: `${m.displayName}${m.installed ? '' : '（未安装）'}`, disabled: !m.installed }))} /></Form.Item><Form.Item label="提示词" name="prompt" rules={[{ required: true, message: '请输入提示词' }]}><Input.TextArea rows={5} placeholder="描述你想生成的图片" /></Form.Item><Space><Form.Item label="宽度" name="width" initialValue={1024}><Input type="number" /></Form.Item><Form.Item label="高度" name="height" initialValue={1024}><Input type="number" /></Form.Item><Form.Item label="步数" name="steps" initialValue={9}><Input type="number" /></Form.Item></Space><br /><Button type="primary" htmlType="submit" loading={loading} icon={<PictureOutlined />}>生成图片</Button></Form></Card>{image && <Card title="生成结果"><img className="generated-image" src={`data:image/png;base64,${image}`} alt="生成结果" /></Card>}</Space>
}
function AudioPage({ models }: { models: Model[] }) {
  const audioModels = models.filter((m) => m.type === 'tts' || m.type === 'asr')
  const [mode, setMode] = useState<'tts' | 'asr'>('tts')
  const [audio, setAudio] = useState('')
  const [transcript, setTranscript] = useState('')
  const [loading, setLoading] = useState(false)
  const [selectedFile, setSelectedFile] = useState<File | null>(null)
  const ttsModels = audioModels.filter((m) => m.type === 'tts')
  const asrModels = audioModels.filter((m) => m.type === 'asr')
  const synthesize = (values: { model: string; text: string; speaker?: string; speed?: number }) => {
    const model = ttsModels.find((item) => item.name === values.model)
    if (!model?.installed) { message.warning('请先在模型管理中安装音频模型'); return }
    setLoading(true); ModelService.Synthesize(JSON.stringify(values)).then(setAudio).then(() => message.success('音频合成完成')).catch((error) => message.error(String(error))).finally(() => setLoading(false))
  }
  const transcribe = async (values: { model: string }) => {
    const model = asrModels.find((item) => item.name === values.model)
    if (!model?.installed) { message.warning('请先在模型管理中安装 ASR 模型'); return }
    if (!selectedFile) { message.warning('请选择音频文件'); return }
    if (selectedFile.size > 32 * 1024 * 1024) { message.error('音频文件不能超过 32 MB'); return }
    setLoading(true)
    const reader = new FileReader(); reader.onload = () => { ModelService.Transcribe(JSON.stringify({ model: values.model, audio: reader.result, format: selectedFile.name.split('.').pop() })).then((result) => { setTranscript(result.text); message.success('识别完成') }).catch((error) => message.error(String(error))).finally(() => setLoading(false)) }; reader.onerror = () => { setLoading(false); message.error('读取音频文件失败') }; reader.readAsDataURL(selectedFile)
  }
  return <Space direction="vertical" size="large" style={{ width: '100%' }}><Space><Typography.Title level={2} style={{ margin: 0 }}>音频处理</Typography.Title><Select value={mode} onChange={setMode} options={[{ value: 'tts', label: '文本转语音' }, { value: 'asr', label: '语音识别' }]} /></Space>{mode === 'tts' ? <Card title="文本转语音" extra={<Tag color="purple">audio.cpp</Tag>}><Form layout="vertical" onFinish={synthesize}><Form.Item label="模型" name="model" rules={[{ required: true, message: '请选择 TTS 模型' }]}><Select placeholder="选择已安装的 TTS 模型" options={ttsModels.map((m) => ({ value: m.name, label: `${m.displayName}${m.installed ? '' : '（未安装）'}`, disabled: !m.installed }))} /></Form.Item><Form.Item label="文本" name="text" rules={[{ required: true, message: '请输入文本' }]}><Input.TextArea rows={7} placeholder="输入要合成的文本" /></Form.Item><Space><Form.Item label="音色" name="speaker"><Input placeholder="例如：vivian" /></Form.Item><Form.Item label="语速" name="speed" initialValue={1}><Input type="number" /></Form.Item></Space><br /><Button type="primary" htmlType="submit" loading={loading} icon={<AudioOutlined />}>合成音频</Button></Form>{audio && <audio controls src={audio} style={{ width: '100%', marginTop: 24 }} />}</Card> : <Card title="语音识别" extra={<Tag color="purple">audio.cpp</Tag>}><Form layout="vertical" onFinish={transcribe}><Form.Item label="模型" name="model" rules={[{ required: true, message: '请选择 ASR 模型' }]}><Select placeholder="选择已安装的 ASR 模型" options={asrModels.map((m) => ({ value: m.name, label: `${m.displayName}${m.installed ? '' : '（未安装）'}`, disabled: !m.installed }))} /></Form.Item><Form.Item label="音频文件"><input type="file" accept="audio/*" onChange={(event) => setSelectedFile(event.target.files?.[0] ?? null)} /></Form.Item><Button type="primary" htmlType="submit" loading={loading}>开始识别</Button></Form>{transcript && <Card type="inner" title="识别结果" style={{ marginTop: 24 }}>{transcript}</Card>}</Card>}</Space>
}
function VideoPage() { return <Card title="视频处理"><Empty image={<VideoCameraOutlined style={{ fontSize: 48 }} />} description={<><Typography.Title level={4}>首版暂不支持视频处理</Typography.Title><Typography.Text type="secondary">当前先完成图片生成、语音合成和模型管理。</Typography.Text></>} /></Card> }

function AppContent() { const [models, setModels] = useState<Model[]>([]); const [runtimes, setRuntimes] = useState<Runtime[]>([]); const [active, setActive] = useState('models'); const refresh = () => { ModelService.ListModels('all').then((items) => setModels(items ?? [])).catch(() => message.error('加载模型目录失败')); ModelService.ListRuntimes().then((items) => setRuntimes(items ?? [])).catch(() => message.error('加载运行时目录失败')); }; useEffect(() => { refresh(); }, []); const items = useMemo(() => [{ key: 'models', icon: <SettingOutlined />, label: '模型管理' }, { key: 'image', icon: <PictureOutlined />, label: '图片处理' }, { key: 'audio', icon: <AudioOutlined />, label: '音频处理' }, { key: 'video', icon: <VideoCameraOutlined />, label: '视频处理' }], []); return <Layout className="app-layout"><Layout.Sider theme="light" width={220} className="app-sider"><div className="app-brand"><CloudDownloadOutlined /> MediaCraft Studio</div><Menu mode="inline" selectedKeys={[active]} items={items} onClick={({ key }) => setActive(key)} /></Layout.Sider><Layout.Content className="app-content">{active === 'models' && <ModelsPage models={models} runtimes={runtimes} refresh={refresh} />}{active === 'image' && <ImagePage models={models} />}{active === 'audio' && <AudioPage models={models} />}{active === 'video' && <VideoPage />}</Layout.Content></Layout> }
export default function App() { return <AntApp><AppContent /></AntApp> }
