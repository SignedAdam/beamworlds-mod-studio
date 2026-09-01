import { useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { Events } from '@wailsio/runtime'
import { AppService as API } from '../bindings/github.com/SignedAdam/beamng-mod-studio/index.js'
import type { AgentActivity, AgentModelOption, NewModRequest, RuntimeReport, WorkspaceDetail, WorkspaceRecord } from '../bindings/github.com/SignedAdam/beamng-mod-studio/models.js'
import type { WorkspaceChange } from '../bindings/github.com/SignedAdam/beamworlds-modkit/models.js'
import { CodeEditor, canFormatSource, formatSource } from './CodeEditor'
import { FileTree, type TreeSelection } from './FileTree'
import { BeamWorldsMark, Icon } from './icons'
import { Badge, Button, EmptyState, HelpTip, Spinner, formatBytes, formatDate, issueTone, kindIcon, kindLabel, shortID } from './ui'

type MakerTab = 'files' | 'diff' | 'agent' | 'review' | 'knowledge' | 'runtime'
type OpenMode = 'editor' | 'virgil' | null

interface EditorDocument {
  path: string
  content: string
  savedContent: string
  restored: boolean
}

interface ActivityLine {
  id: string
  type: string
  at: string
  message: string
  delta: string
  toolName: string
  isError: boolean
  data: Record<string, unknown>
}

const editableExtensions = new Set(['.cfg', '.cs', '.css', '.html', '.ini', '.jbeam', '.js', '.json', '.json5', '.lua', '.md', '.mis', '.pc', '.scss', '.ts', '.txt', '.xml', '.yaml', '.yml'])
function isEditableSource(path: string) { const dot = path.toLowerCase().lastIndexOf('.'); return dot >= 0 && editableExtensions.has(path.toLowerCase().slice(dot)) }

interface ModMakerProps {
  workspaces: WorkspaceRecord[]
  detail: WorkspaceDetail | null
  selectedID: string
  loading: boolean
  defaultAuthor: string
  showFileSizes: boolean
  openMode: OpenMode
  onOpenModeHandled: () => void
  onSelect: (workspaceID: string) => void
  onReload: () => Promise<void>
  onCreateMod: (request: NewModRequest, virgilPrompt?: string, modelOverride?: string) => Promise<void>
  onAgentStarted: () => void
  onEditorStatus: (status: { path: string; dirty: boolean; sizeBytes: number }) => void
  onNotify: (message: string, tone?: 'success' | 'error' | 'info') => void
  onError: (error: unknown) => void
}

export function ModMaker({ workspaces, detail, selectedID, loading, defaultAuthor, showFileSizes, openMode, onOpenModeHandled, onSelect, onReload, onCreateMod, onAgentStarted, onEditorStatus, onNotify, onError }: ModMakerProps) {
  const [tab, setTab] = useState<MakerTab>('files')
  const [documents, setDocuments] = useState<EditorDocument[]>([])
  const [activePath, setActivePath] = useState('')
  const [treeSelection, setTreeSelection] = useState<TreeSelection | null>(null)
  const [fileLoading, setFileLoading] = useState(false)
  const [fileQuery, setFileQuery] = useState('')
  const [contentQuery, setContentQuery] = useState('')
  const [searchResults, setSearchResults] = useState<string[]>([])
  const [changes, setChanges] = useState<WorkspaceChange[]>([])
  const [diffLoading, setDiffLoading] = useState(false)
  const [busy, setBusy] = useState('')
  const [exportLabel, setExportLabel] = useState('')
  const [agentPrompt, setAgentPrompt] = useState('')
  const [activeRunID, setActiveRunID] = useState('')
  const [activities, setActivities] = useState<ActivityLine[]>([])
  const [agentText, setAgentText] = useState('')
  const [runtime, setRuntime] = useState<RuntimeReport | null>(null)
  const [newModOpen, setNewModOpen] = useState(false)
  const initializedWorkspace = useRef('')
  const activitySequence = useRef(0)
  const draftTimer = useRef<number>()
  const workspace = detail?.workspace
  const files = detail?.files ?? []
  const directories = detail?.directories ?? []
  const agentRuns = detail?.agentRuns ?? []
  const exports = detail?.exports ?? []
  const knowledge = detail?.knowledge ?? []
  const validationIssues = detail?.validation.issues ?? []
  const runtimeDiagnostics = runtime?.diagnostics ?? []
  const activeDocument = documents.find(document => document.path === activePath)
  const dirty = documents.some(document => document.content !== document.savedContent)
  const activeSizeBytes = useMemo(() => activeDocument ? new TextEncoder().encode(activeDocument.content).byteLength : 0, [activeDocument?.content])
  const agentTasks = useMemo(() => extractAgentTasks(activities, agentPrompt, Boolean(activeRunID)), [activities, agentPrompt, activeRunID])
  const totalTokens = useMemo(() => activities.filter(line => line.type === 'turn_end').reduce((total, line) => total + tokenCount(line.data), 0), [activities])

  useEffect(() => {
    initializedWorkspace.current = ''
    setDocuments([])
    setActivePath('')
    setTreeSelection(null)
    setChanges([])
    setRuntime(null)
    setActivities([])
    setAgentText('')
    setAgentPrompt('')
  }, [selectedID])

  useEffect(() => {
    if (!workspace || initializedWorkspace.current === workspace.id) return
    initializedWorkspace.current = workspace.id
    const running = agentRuns.find(run => run.status === 'running')
    setActiveRunID(running?.id ?? '')
    if (running) {
      setAgentPrompt(running.prompt)
      setTab('agent')
      void loadAgentRun(running.id, running.finalText)
    }
    const drafts = detail?.drafts ?? []
    if (!drafts.length) return
    void Promise.all(drafts.map(async draft => {
      let savedContent = ''
      try { savedContent = (await API.ReadWorkspaceFile(workspace.id, draft.path)).content } catch { savedContent = '' }
      return { path: draft.path, content: draft.content, savedContent, restored: true } satisfies EditorDocument
    })).then(restored => {
      setDocuments(restored)
      setActivePath(restored[0]?.path ?? '')
      if (restored[0]) setTreeSelection({ path: restored[0].path, kind: 'file' })
      if (restored.length) onNotify(`Restored ${restored.length} unsaved ${restored.length === 1 ? 'draft' : 'drafts'}`, 'info')
    }).catch(onError)
  }, [workspace?.id])

  useEffect(() => {
    if (!openMode || !workspace || workspace.id !== selectedID) return
    setTab(openMode === 'virgil' ? 'agent' : 'files')
    onOpenModeHandled()
  }, [openMode, workspace?.id, selectedID])

  useEffect(() => {
    onEditorStatus({ path: activePath, dirty, sizeBytes: activeSizeBytes })
    return () => onEditorStatus({ path: '', dirty: false, sizeBytes: 0 })
  }, [activePath, dirty, activeSizeBytes, onEditorStatus])

  useEffect(() => {
    window.clearTimeout(draftTimer.current)
    if (!workspace || documents.length === 0) return
    const snapshot = documents.map(document => ({ ...document }))
    draftTimer.current = window.setTimeout(() => {
      void Promise.all(snapshot.map(document => document.content === document.savedContent ? API.DeleteWorkspaceDraft(workspace.id, document.path) : API.SaveWorkspaceDraft(workspace.id, document.path, document.content))).catch(onError)
    }, 450)
    return () => window.clearTimeout(draftTimer.current)
  }, [documents, workspace?.id])

  useEffect(() => {
    const unsubscribe = Events.On('agent:event', event => {
      const activity: AgentActivity = event.data
      if (!workspace || activity.runId !== activeRunID) return
      const line: ActivityLine = { id: `${activity.at}-${activitySequence.current++}`, type: activity.type, at: activity.at, message: activity.message ?? '', delta: activity.delta ?? '', toolName: activity.toolName ?? '', isError: activity.isError ?? false, data: (activity.data ?? {}) as Record<string, unknown> }
      if (activity.delta) setAgentText(value => value + activity.delta)
      if (activity.type !== 'message_update' || activity.delta) setActivities(value => [...value.slice(-1999), line])
      if (activity.type === 'host_tool_end' && ['workspace_write', 'workspace_replace'].includes(activity.toolName ?? '')) void onReload()
      if (activity.type === 'finished') { setActiveRunID(''); void onReload() }
    })
    return unsubscribe
  }, [workspace?.id, activeRunID, onReload])

  useEffect(() => { if (tab === 'diff' && workspace) void refreshDiff() }, [tab, workspace?.id])

  const refreshDiff = async () => {
    if (!workspace) return
    setDiffLoading(true)
    try { setChanges(await API.WorkspaceDiff(workspace.id) ?? []) } catch (error) { onError(error) } finally { setDiffLoading(false) }
  }

  const selectFile = async (path: string, switchToEditor = true) => {
    if (!workspace) return
    setTreeSelection({ path, kind: 'file' })
    if (switchToEditor) setTab('files')
    if (!isEditableSource(path)) { setActivePath(''); return }
    const existing = documents.find(document => document.path === path)
    if (existing) { setActivePath(path); return }
    setFileLoading(true)
    try {
      const file = await API.ReadWorkspaceFile(workspace.id, path)
      const draft = detail?.drafts?.find(item => item.path === path)
      const document = { path: file.path, content: draft?.content ?? file.content, savedContent: file.content, restored: Boolean(draft) }
      setDocuments(current => [...current, document])
      setActivePath(file.path)
    } catch (error) { onError(error) } finally { setFileLoading(false) }
  }

  const updateDocument = (content: string) => setDocuments(current => current.map(document => document.path === activePath ? { ...document, content, restored: false } : document))

  const saveFile = async () => {
    if (!workspace || !activeDocument || activeDocument.content === activeDocument.savedContent) return
    setBusy('save')
    try {
      await API.WriteWorkspaceFile(workspace.id, activeDocument.path, activeDocument.content)
      setDocuments(current => current.map(document => document.path === activeDocument.path ? { ...document, savedContent: document.content, restored: false } : document))
      onNotify(`Saved ${activeDocument.path}`, 'success')
      await onReload()
    } catch (error) { onError(error) } finally { setBusy('') }
  }

  const closeDocument = (path: string) => {
    const document = documents.find(item => item.path === path)
    if (document && document.content !== document.savedContent && workspace) void API.SaveWorkspaceDraft(workspace.id, path, document.content).catch(onError)
    setDocuments(current => current.filter(item => item.path !== path))
    if (treeSelection?.path === path) setTreeSelection(null)
    if (activePath === path) {
      const remaining = documents.filter(item => item.path !== path)
      setActivePath(remaining[remaining.length - 1]?.path ?? '')
    }
  }

  const parentDirectory = () => {
    if (!treeSelection) return ''
    return treeSelection.path.includes('/') ? treeSelection.path.slice(0, treeSelection.path.lastIndexOf('/')) : ''
  }

  const createFile = async () => {
    if (!workspace) return
    const base = parentDirectory()
    const suggested = base ? `${base}/new-file.lua` : 'new-file.lua'
    const path = window.prompt('New project-relative file path', suggested)?.trim()
    if (!path) return
    try { await API.WriteWorkspaceFile(workspace.id, path, ''); await onReload(); await selectFile(path); onNotify(`Created ${path}`, 'success') } catch (error) { onError(error) }
  }

  const createDirectory = async () => {
    if (!workspace) return
    const base = parentDirectory()
    const path = window.prompt('New project-relative folder', base ? `${base}/new-folder` : 'new-folder')?.trim()
    if (!path) return
    try { await API.CreateWorkspaceDirectory(workspace.id, path); await onReload(); onNotify(`Created ${path}`, 'success') } catch (error) { onError(error) }
  }

  const movePath = async (oldPath: string, newPath: string) => {
    if (!workspace || !newPath || oldPath === newPath) return
    try {
      await API.RenameWorkspacePath(workspace.id, oldPath, newPath)
      const migrate = (path: string) => path === oldPath ? newPath : path.startsWith(`${oldPath}/`) ? `${newPath}${path.slice(oldPath.length)}` : path
      setDocuments(current => current.map(document => ({ ...document, path: migrate(document.path) })))
      setActivePath(current => migrate(current))
      setTreeSelection(current => current ? { ...current, path: migrate(current.path) } : null)
      await onReload()
      onNotify(`Moved to ${newPath}`, 'success')
    } catch (error) { onError(error) }
  }

  const renamePath = (selection: TreeSelection) => {
    const nextPath = window.prompt(`Rename project ${selection.kind}`, selection.path)?.trim()
    if (nextPath) void movePath(selection.path, nextPath)
  }

  const deletePath = async (selection: TreeSelection) => {
    if (!workspace || !window.confirm(`Delete ${selection.path} from this project${selection.kind === 'directory' ? ' and every path inside it' : ''}?`)) return
    const deleted = selection.path
    try {
      await API.DeleteWorkspacePath(workspace.id, deleted)
      setDocuments(current => current.filter(document => document.path !== deleted && !document.path.startsWith(`${deleted}/`)))
      if (activePath === deleted || activePath.startsWith(`${deleted}/`)) setActivePath('')
      if (treeSelection?.path === deleted || treeSelection?.path.startsWith(`${deleted}/`)) setTreeSelection(null)
      await onReload()
      onNotify('Project path deleted', 'success')
    } catch (error) { onError(error) }
  }

  const revealPath = (selection: TreeSelection) => {
    if (!workspace) return
    void API.RevealWorkspacePath(workspace.id, selection.path).catch(onError)
  }

  const formatActive = async () => {
    if (!activeDocument) return
    setBusy('format')
    try { updateDocument(await formatSource(activeDocument.path, activeDocument.content)); onNotify(`Formatted ${activeDocument.path}`, 'success') } catch (error) { onError(error) } finally { setBusy('') }
  }

  const searchContent = async () => {
    if (!workspace || !contentQuery.trim()) return
    setBusy('search')
    try { setSearchResults(await API.SearchWorkspace(workspace.id, contentQuery.trim(), 300) ?? []) } catch (error) { onError(error) } finally { setBusy('') }
  }

  const validate = async () => {
    if (!workspace) return
    setBusy('validate')
    try { const result = await API.ValidateWorkspace(workspace.id); await onReload(); onNotify(result.valid ? 'Project validation passed' : `Validation found ${(result.issues ?? []).length} review items`, result.valid ? 'success' : 'error') } catch (error) { onError(error) } finally { setBusy('') }
  }

  const exportWorkspace = async () => {
    if (!workspace) return
    setBusy('export')
    try { const response = await API.ExportWorkspace(workspace.id, exportLabel); await onReload(); onNotify(`Exported ${response.record.path}`, 'success') } catch (error) { onError(error) } finally { setBusy('') }
  }

  const installLatest = async () => {
    if (!workspace || exports.length === 0) return
    setBusy('install')
    try { const installed = await API.InstallExportForTest(workspace.id, exports[0].id); await onReload(); onNotify(`Test-installed ${installed.path}`, 'success') } catch (error) { onError(error) } finally { setBusy('') }
  }

  const uninstallTest = async () => {
    if (!workspace) return
    setBusy('uninstall')
    try { await API.UninstallTest(workspace.id); await onReload(); onNotify('Managed test archive removed', 'success') } catch (error) { onError(error) } finally { setBusy('') }
  }

  const launchGame = async () => {
    if (!workspace) return
    setBusy('launch')
    try { const launch = await API.LaunchBeamNG(workspace.id); onNotify(`BeamNG launched as process ${launch.pid}`, 'success') } catch (error) { onError(error) } finally { setBusy('') }
  }

  const analyzeRuntime = async () => {
    if (!workspace) return
    setBusy('runtime')
    try { setRuntime(await API.AnalyzeRuntime(workspace.id)); setTab('runtime') } catch (error) { onError(error) } finally { setBusy('') }
  }

  const startAgent = async () => {
    if (!workspace || !agentPrompt.trim()) return
    setBusy('agent')
    try {
      const run = await API.StartAgent(workspace.id, agentPrompt.trim(), '')
      setActiveRunID(run.id)
      setActivities([])
      setAgentText('')
      setTab('agent')
      onAgentStarted()
      onNotify('Virgil started with project-scoped tools', 'info')
    } catch (error) { onError(error) } finally { setBusy('') }
  }

  const stopAgent = async () => {
    if (!activeRunID) return
    try { await API.StopAgent(activeRunID); onNotify('Virgil cancellation requested', 'info') } catch (error) { onError(error) }
  }

  async function loadAgentRun(runID: string, finalText: string) {
    try {
      const records = await API.ListAgentEvents(runID) ?? []
      setActivities(records.map(record => ({ id: String(record.id), type: record.type, at: record.at, message: record.message, delta: '', toolName: inferToolName(record.data as Record<string, unknown>), isError: record.type.includes('error') || Boolean((record.data as Record<string, unknown>)?.error), data: (record.data ?? {}) as Record<string, unknown> })))
      setAgentText(finalText)
    } catch (error) { onError(error) }
  }

  const cloneSelectedVariant = async () => {
    if (!workspace || !activePath.toLowerCase().endsWith('.pc')) return
    const baseName = window.prompt('New variant basename (without .pc)')?.trim()
    if (!baseName) return
    const displayName = window.prompt('New variant display name', baseName)?.trim() ?? baseName
    try { const created = await API.CloneVehicleVariant(workspace.id, activePath, baseName, displayName) ?? []; await onReload(); onNotify(`Created ${created.length} variant files`, 'success') } catch (error) { onError(error) }
  }

  if (!selectedID) return <section className="project-browser-view"><ProjectBrowser workspaces={workspaces} onOpen={onSelect} onNew={() => setNewModOpen(true)}/>{newModOpen && <div className="new-mod-overlay" role="dialog" aria-modal="true" aria-label="New mod project"><button className="new-mod-overlay__close" onClick={() => setNewModOpen(false)} aria-label="Close"><Icon name="close" size={17}/></button><NewModStart embedded defaultAuthor={defaultAuthor} onCreate={async (request, prompt, model) => { await onCreateMod(request, prompt, model); setNewModOpen(false) }} onError={onError}/></div>}</section>

  const tabs: [MakerTab, Parameters<typeof Icon>[0]['name'], string][] = [['files', 'files', 'Files'], ['diff', 'diff', 'Change history'], ['agent', 'agent', 'Virgil'], ['review', 'check', 'Build'], ['knowledge', 'book', 'Context'], ['runtime', 'terminal', 'Game test']]
  return <section className="maker-shell">
    <main className="maker-main">
      {loading || !detail ? <div className="center-loader center-loader--full"><Spinner/><span>Loading project</span></div> : <>
        <header className="maker-header">
          <Button className="maker-back" tone="quiet" icon="arrow" onClick={() => onSelect('')}>Back to all projects</Button>
          <div className="maker-title"><Icon name={kindIcon(String(detail.entity.kind))} size={18}/><strong>{detail.entity.displayName}</strong><Badge tone="neutral">{kindLabel(String(detail.entity.kind))}</Badge></div>
          <div className="maker-header__metrics"><span>{files.length.toLocaleString()} files</span><span>{formatBytes(detail.diskBytes)}</span><span className={detail.validation.valid ? 'status-good' : ''}>{detail.validation.valid ? 'Validated' : 'Not validated'}</span>{detail.activeTest && <span className="status-test">Test installed</span>}</div>
          <div className="maker-header__actions"><Button icon="check" disabled={busy !== ''} onClick={validate}>{busy === 'validate' ? 'Validating' : 'Validate'}</Button><Button icon="export" tone="primary" disabled={busy !== ''} onClick={exportWorkspace}>{busy === 'export' ? 'Exporting' : 'Export ZIP'}</Button></div>
        </header>
        <nav className="glass-tabs maker-tabs">{tabs.map(([value, icon, label], index) => <button key={value} style={{ zIndex: tab === value ? tabs.length + 2 : tabs.length - index }} className={tab === value ? 'is-active' : ''} onClick={() => setTab(value)}><Icon name={icon} size={14}/>{label}</button>)}</nav>
        <div className="maker-content">
          {tab === 'files' && <div className="editor-layout">
            <aside className="file-browser">
              <div className="file-browser__toolbar"><label className="search-box"><Icon name="search" size={15}/><input value={fileQuery} onChange={event => setFileQuery(event.target.value)} placeholder="Filter files"/><span>{files.length}</span></label><button className="icon-button" onClick={createFile} title="Create text file"><Icon name="files" size={15}/></button><button className="icon-button" onClick={createDirectory} title="Create folder"><Icon name="folder" size={15}/></button></div>
              <FileTree files={files} directories={directories} query={fileQuery} selected={treeSelection} showSizes={showFileSizes} onSelectFile={path => void selectFile(path)} onMove={(oldPath, newPath) => void movePath(oldPath, newPath)} onRename={renamePath} onDelete={selection => void deletePath(selection)} onReveal={revealPath}/>
              <div className="content-search"><div><input value={contentQuery} onChange={event => setContentQuery(event.target.value)} onKeyDown={event => { if (event.key === 'Enter') void searchContent() }} placeholder="Search file contents"/><button onClick={() => void searchContent()} aria-label="Search file contents"><Icon name="search" size={15}/></button></div>{searchResults.length > 0 && <div className="content-search__results">{searchResults.slice(0, 100).map(result => <button key={result} onClick={() => void selectFile(result.split(':')[0])}>{result}</button>)}</div>}</div>
            </aside>
            <section className={`source-editor ${documents.length > 0 ? 'source-editor--open' : 'source-editor--empty'}`}>
              {documents.length > 0 && <div className="editor-tabs" role="tablist">{documents.map(document => <div key={document.path} className={`editor-tab ${document.path === activePath ? 'is-active' : ''}`}><button className="editor-tab__open" role="tab" aria-selected={document.path === activePath} onClick={() => { setActivePath(document.path); setTreeSelection({ path: document.path, kind: 'file' }) }} title={document.path}><Icon name="files" size={14}/><span>{document.path.split('/').pop()}</span>{document.content !== document.savedContent && <i/>}</button><button className="editor-tab__close" onClick={() => closeDocument(document.path)} aria-label={`Close ${document.path}`}><Icon name="close" size={12}/></button></div>)}</div>}
              {fileLoading ? <div className="center-loader"><Spinner/><span>Opening file</span></div> : activeDocument ? <>
                <header className="source-editor__header"><div><code>{activeDocument.path}</code>{activeDocument.restored && <Badge tone="cyan">Draft restored</Badge>}{activeDocument.content !== activeDocument.savedContent && <Badge tone="warning">Unsaved</Badge>}</div><div>{activePath.toLowerCase().endsWith('.pc') && <Button tone="quiet" icon="copy" onClick={cloneSelectedVariant}>Clone variant</Button>}<Button tone="quiet" icon="code" disabled={!canFormatSource(activeDocument.path) || busy !== ''} onClick={formatActive}>Format</Button><Button tone="primary" icon="save" disabled={activeDocument.content === activeDocument.savedContent || busy === 'save'} onClick={saveFile}>Save</Button></div></header>
                <CodeEditor path={activeDocument.path} value={activeDocument.content} onChange={updateDocument} onSave={() => void saveFile()}/>
                <footer className="source-editor__footer"><span>{activeDocument.content.split('\n').length.toLocaleString()} lines</span><span>{formatBytes(activeSizeBytes)}</span><span>Ctrl+S · diagnostics enabled</span></footer>
              </> : <div className="editor-empty-message">Click a file to open it</div>}
            </section>
          </div>}
          {tab === 'diff' && <div className="maker-pane"><PaneToolbar title="Change history" help="Added, modified, and deleted files are calculated against the immutable source ZIP." action={<Button icon="refresh" onClick={() => void refreshDiff()}>Refresh</Button>}/>{diffLoading ? <div className="center-loader"><Spinner/><span>Computing hashes</span></div> : changes.length === 0 ? <EmptyState icon="diff" title="No source changes" detail="The project currently matches its immutable source."/> : <div className="change-list">{changes.map(change => <article key={change.path} className={`change change--${change.type}`}><Badge tone={change.type === 'added' ? 'success' : change.type === 'deleted' ? 'danger' : 'warning'}>{change.type}</Badge><code>{change.path}</code>{change.textDiff && <pre>{change.textDiff}</pre>}</article>)}</div>}</div>}
          {tab === 'agent' && <div className="virgil-workspace"><aside className="virgil-files"><header><strong>Live project</strong><span>{files.length}</span></header><FileTree files={files} directories={directories} query="" selected={treeSelection} showSizes={showFileSizes} onSelectFile={path => void selectFile(path)} onMove={(oldPath, newPath) => void movePath(oldPath, newPath)} onRename={renamePath} onDelete={selection => void deletePath(selection)} onReveal={revealPath}/></aside><section className="virgil-conversation"><PaneToolbar title="Virgil" help="Virgil sees BeamNG context and only project-scoped host tools. Its built-in task tool is enabled; unrestricted filesystem tools remain disabled."/><div className="agent-goal"><textarea value={agentPrompt} onChange={event => setAgentPrompt(event.target.value)} placeholder="Describe one concrete change and its acceptance behavior" disabled={Boolean(activeRunID)}/><div><span><Icon name="link" size={13}/>Project-scoped tools</span>{activeRunID ? <Button icon="close" tone="danger" onClick={stopAgent}>Cancel</Button> : <Button icon="agent" tone="primary" disabled={!agentPrompt.trim() || busy === 'agent'} onClick={startAgent}>Run Virgil</Button>}</div></div><div className="conversation-feed">{!agentPrompt && activities.length === 0 && !agentText ? <EmptyState icon="agent" title="Start a conversation" detail="Ask for a BeamNG change. Files and task progress remain visible while Virgil works."/> : <><article className="message message--user"><header><strong>You</strong></header><p>{agentPrompt}</p></article>{activities.filter(line => line.type === 'host_tool_end' || line.type === 'tool_execution_end' || line.isError).map(line => <div className={`tool-activity ${line.isError ? 'is-error' : ''}`} key={line.id}><Icon name={line.isError ? 'error' : 'terminal'} size={13}/><span>{line.message || line.toolName || friendlyEvent(line.type)}</span><time>{formatDate(line.at)}</time></div>)}{agentText && <article className="message message--assistant"><header><Icon name="agent" size={14}/><strong>Virgil</strong>{activeRunID && <span className="live-dot is-live"/>}</header><pre>{agentText}</pre></article>}</>}</div></section><aside className="virgil-context"><section className="agent-task-panel"><header><div><strong>Tasks</strong><Badge tone={activeRunID ? 'warning' : 'neutral'}>{agentTasks.length}</Badge></div><p>Task updates may trail live tool activity.</p></header><div>{agentTasks.map((task, index) => <div className={`agent-task agent-task--${task.status}`} key={`${task.text}-${index}`}><span>{task.status === 'complete' ? '✓' : task.status === 'blocked' ? '!' : task.status === 'active' ? '●' : '○'}</span><p>{task.text}</p></div>)}</div></section><div className="token-counter"><span>TOTAL TOKENS</span><strong>{totalTokens.toLocaleString()}</strong><small>{activeRunID ? `Run ${shortID(activeRunID)}` : 'Selected conversation'}</small></div><section className="agent-history"><header>Conversations</header>{agentRuns.length === 0 ? <p>No previous conversations.</p> : agentRuns.map(run => <button key={run.id} className={run.id === activeRunID ? 'is-active' : ''} onClick={() => { setAgentPrompt(run.prompt); void loadAgentRun(run.id, run.finalText) }}><div><Badge tone={run.status === 'complete' ? 'success' : run.status === 'failed' ? 'danger' : 'warning'}>{run.status}</Badge><time>{formatDate(run.startedAt)}</time></div><p>{run.prompt}</p></button>)}</section></aside></div>}
          {tab === 'review' && <div className="maker-pane"><PaneToolbar title="Build" help="Validation checks project structure. Export creates a verified immutable ZIP without modifying the original source."/><section className="build-section"><div className="build-row"><div><Icon name={detail.validation.valid ? 'check' : 'warning'} size={17}/><strong>Validation</strong><span>{detail.validation.valid ? 'Passed' : `${validationIssues.length} review items`}</span></div><Button onClick={validate} disabled={busy !== ''}>Run validation</Button></div>{validationIssues.length > 0 && <div className="review-issues">{validationIssues.map((issue, index) => <div key={`${issue.code}-${index}`}><Badge tone={issueTone(issue.severity)}>{issue.severity}</Badge><strong>{issue.code}</strong><span>{issue.message}</span>{issue.path && <code>{issue.path}</code>}</div>)}</div>}</section><section className="build-section"><div className="build-row"><div><Icon name="export" size={17}/><strong>Verified ZIP</strong><span>Atomic pack · structural verification · SHA-256 registration</span></div><label className="inline-field"><span>Label</span><input value={exportLabel} onChange={event => setExportLabel(event.target.value)} placeholder={detail.entity.displayName}/></label><Button icon="export" tone="primary" onClick={exportWorkspace} disabled={busy !== ''}>Build ZIP</Button></div></section><section className="build-section"><div className="build-row"><div><Icon name="archive" size={17}/><strong>Exports</strong><span>{exports.length} immutable artifacts</span></div>{exports.length > 0 && <Button icon="install" onClick={installLatest} disabled={busy !== ''}>Install latest for test</Button>}</div>{exports.length > 0 && <div className="export-list">{exports.map((item, index) => <div key={item.id}><strong>{item.path}</strong><span>{formatDate(item.createdAt)}</span><code>SHA {item.sha256.slice(0, 12)}</code>{index === 0 && <Badge tone="success">Latest</Badge>}</div>)}</div>}</section></div>}
          {tab === 'knowledge' && <div className="maker-pane"><PaneToolbar title="Context" help="Reference material supplied to Virgil according to context depth in settings."/><div className="knowledge-list">{knowledge.map((document, index) => <details key={document.id} open={index === 0}><summary><span><Icon name="book" size={15}/>{document.title}</span><code>{document.id}</code></summary><pre>{document.content}</pre></details>)}</div></div>}
          {tab === 'runtime' && <div className="maker-pane"><PaneToolbar title="Game test" help="Install an exact export into the managed test location, exercise it in BeamNG, then analyze only fresh log bytes."/><div className="game-test-toolbar"><div className={detail.activeTest ? 'test-state is-active' : 'test-state'}><Icon name="install" size={16}/><div><strong>{detail.activeTest ? 'Test ZIP installed' : 'No test ZIP installed'}</strong><span>{detail.activeTest?.path || 'Build and install an export before launching the game.'}</span></div></div><Button onClick={detail.activeTest ? uninstallTest : installLatest} disabled={(!detail.activeTest && exports.length === 0) || busy !== ''} tone={detail.activeTest ? 'danger' : 'default'}>{detail.activeTest ? 'Uninstall' : 'Install latest'}</Button><Button icon="play" tone="primary" onClick={launchGame} disabled={!detail.activeTest || busy !== ''}>Launch BeamNG</Button><Button icon="terminal" onClick={analyzeRuntime} disabled={!detail.activeTest || busy !== ''}>Analyze log</Button></div>{!runtime ? <EmptyState icon="terminal" title="No game-test report" detail="Install, launch, exercise the mod in BeamNG, then analyze the new log segment."/> : <div className="runtime-report"><header><Badge tone={runtime.errors ? 'danger' : 'success'}>{runtime.errors} errors</Badge><Badge tone={runtime.warnings ? 'warning' : 'success'}>{runtime.warnings} warnings</Badge><Badge tone="neutral">{runtime.relevantHits} mod hits</Badge><span>{formatBytes(runtime.freshBytes)} · {runtime.logPath}</span></header>{runtimeDiagnostics.length === 0 ? <div className="success-line"><Icon name="check" size={15}/>No warnings or errors in the new log segment</div> : <div className="diagnostic-list">{runtimeDiagnostics.map((item, index) => <div key={`${item.line}-${index}`}><Badge tone={item.severity === 'error' ? 'danger' : 'warning'}>{item.severity}</Badge><code>Line {item.line}</code><span>{item.message}</span></div>)}</div>}</div>}</div>}
        </div>
      </>}
    </main>
    {newModOpen && <div className="new-mod-overlay" role="dialog" aria-modal="true" aria-label="New mod project"><button className="new-mod-overlay__close" onClick={() => setNewModOpen(false)} aria-label="Close"><Icon name="close" size={17}/></button><NewModStart embedded defaultAuthor={defaultAuthor} onCreate={async (request, prompt, model) => { await onCreateMod(request, prompt, model); setNewModOpen(false) }} onError={onError}/></div>}
  </section>
}

function ProjectBrowser({ workspaces, onOpen, onNew }: { workspaces: WorkspaceRecord[]; onOpen: (workspaceID: string) => void; onNew: () => void }) {
  const [query, setQuery] = useState('')
  const [filter, setFilter] = useState<'all' | 'active' | 'ai'>('all')
  const visible = useMemo(() => {
    const normalized = query.trim().toLowerCase()
    return workspaces.filter(record => {
      if (filter === 'active' && record.status !== 'active') return false
      if (filter === 'ai' && record.agentStatus !== 'running') return false
      return !normalized || record.displayName.toLowerCase().includes(normalized) || String(record.kind).toLowerCase().includes(normalized)
    })
  }, [workspaces, query, filter])
  const running = workspaces.filter(record => record.agentStatus === 'running').length

  return <section className="project-browser">
    <header className="project-browser__header"><div><span>MODMAKER</span><h1>Projects</h1><p>Open a mod workspace or start something new.</p></div><Button icon="plus" tone="primary" onClick={onNew}>New project</Button></header>
    <div className="project-browser__toolbar">
      <label className="search-box"><Icon name="search" size={16}/><input value={query} onChange={event => setQuery(event.target.value)} placeholder="Find a project"/>{query && <button onClick={() => setQuery('')} aria-label="Clear project search"><Icon name="close" size={13}/></button>}</label>
      <div className="segmented"><button className={filter === 'all' ? 'is-active' : ''} onClick={() => setFilter('all')}>All {workspaces.length}</button><button className={filter === 'active' ? 'is-active' : ''} onClick={() => setFilter('active')}>Active</button><button className={filter === 'ai' ? 'is-active' : ''} onClick={() => setFilter('ai')}>AI working {running}</button></div>
      <span>{visible.length.toLocaleString()} shown</span>
    </div>
    <div className="project-grid">
      {visible.map(record => <button className="project-card" key={record.id} onClick={() => onOpen(record.id)}>
        <header><span className="project-card__mark"><Icon name={kindIcon(String(record.kind))} size={19}/></span><Badge tone={record.agentStatus === 'running' ? 'warning' : record.status === 'active' ? 'success' : 'neutral'}>{record.agentStatus === 'running' ? 'AI working' : record.status}</Badge></header>
        <h2>{record.displayName}</h2>
        <p>{kindLabel(String(record.kind))}</p>
        <dl><div><dt>Last worked</dt><dd>{formatDate(record.updatedAt)}</dd></div><div><dt>Project status</dt><dd>{record.status === 'active' ? 'Active workspace' : record.status}</dd></div><div><dt>AI activity</dt><dd>{record.agentStatus === 'running' ? `Working since ${formatDate(record.agentUpdatedAt)}` : record.agentStatus === 'idle' ? 'Idle' : `${record.agentStatus} · ${formatDate(record.agentUpdatedAt)}`}</dd></div><div><dt>Validation</dt><dd>{record.lastValidation ? 'Recorded' : 'Not run'}</dd></div></dl>
        <footer><span>Open project</span><Icon name="arrow" size={15}/></footer>
      </button>)}
      {visible.length === 0 && <div className="project-browser__empty"><Icon name="workspace" size={30}/><strong>{workspaces.length === 0 ? 'No projects yet' : 'No matching projects'}</strong><span>{workspaces.length === 0 ? 'Create a project to begin working in ModMaker.' : 'Change the search or project filter.'}</span>{workspaces.length === 0 && <Button icon="plus" tone="primary" onClick={onNew}>Create first project</Button>}</div>}
    </div>
  </section>
}

function PaneToolbar({ title, help, action }: { title: string; help: string; action?: ReactNode }) { return <header className="pane-toolbar"><strong>{title}</strong><HelpTip label={`About ${title}`}>{help}</HelpTip><span/>{action}</header> }

function NewModStart({ defaultAuthor, onCreate, onError, embedded = false }: { defaultAuthor: string; onCreate: (request: NewModRequest, prompt?: string, modelOverride?: string) => Promise<void>; onError: (error: unknown) => void; embedded?: boolean }) {
  const [mode, setMode] = useState<'prompt' | 'manual'>('prompt')
  const [prompt, setPrompt] = useState('')
  const [name, setName] = useState('')
  const [modID, setModID] = useState('')
  const [kind, setKind] = useState('vehicle')
  const [author, setAuthor] = useState(defaultAuthor)
  const [version, setVersion] = useState('0.1.0')
  const [description, setDescription] = useState('')
  const [creating, setCreating] = useState(false)
  const [manualID, setManualID] = useState(false)
  const [showModels, setShowModels] = useState(false)
  const [models, setModels] = useState<AgentModelOption[]>([])
  const [model, setModel] = useState('')
  const [modelsLoading, setModelsLoading] = useState(false)
  const changeName = (value: string) => { setName(value); if (!manualID) setModID(slugify(value)) }
  const create = async (request: NewModRequest, virgilPrompt = '') => { setCreating(true); try { await onCreate(request, virgilPrompt, model) } catch (error) { onError(error) } finally { setCreating(false) } }
  const createFromPrompt = () => void create(inferNewMod(prompt, defaultAuthor), prompt.trim())
  const createManual = () => void create({ name: name.trim(), modId: modID.trim(), kind, author: author.trim(), version: version.trim(), description: description.trim() })
  const revealModels = async () => {
    const next = !showModels
    setShowModels(next)
    if (!next || models.length) return
    setModelsLoading(true)
    try { setModels(await API.ListAgentModels() ?? []) } catch (error) { onError(error) } finally { setModelsLoading(false) }
  }
  return <section className={`new-mod-start ${embedded ? 'is-embedded' : 'view'}`}><header className="new-mod-start__header"><BeamWorldsMark size={34}/><div><span>MODMAKER</span><h1>{mode === 'prompt' ? 'What do you want to build?' : 'Set up a project'}</h1><p>{mode === 'prompt' ? 'Virgil creates the correct BeamNG template, opens the project, and continues as a conversation.' : 'Choose the template details yourself. You can ask Virgil for help later.'}</p></div></header>{mode === 'prompt' ? <div className="prompt-create"><div className="virgil-create-bar"><Icon name="agent" size={22}/><textarea value={prompt} onChange={event => setPrompt(event.target.value)} onKeyDown={event => { if ((event.ctrlKey || event.metaKey) && event.key === 'Enter') createFromPrompt() }} placeholder="Describe your BeamNG mod and the behavior it should have" autoFocus/><Button icon="arrow" tone="primary" disabled={!prompt.trim() || creating} onClick={createFromPrompt}>{creating ? 'Creating' : 'Create'}</Button></div><div className="creation-options"><button className="creation-link" onClick={() => setMode('manual')}>Set up manually</button><button className="creation-link" onClick={() => void revealModels()}>{showModels ? 'Hide model options' : 'Choose a model (optional)'}</button>{showModels && <label className="creation-model"><span>Virgil model</span>{modelsLoading ? <Spinner small/> : <select value={model} onChange={event => setModel(event.target.value)}><option value="">Use application settings</option>{models.map(option => <option key={option.selector} value={option.selector}>{option.name || option.id} · {option.provider}</option>)}</select>}</label>}<span>Ctrl+Enter to create</span></div></div> : <div className="manual-create"><button className="creation-link creation-back" onClick={() => setMode('prompt')}>← Back to Virgil</button><div className="form-grid"><label className="field"><span>Mod kind</span><select value={kind} onChange={event => setKind(event.target.value)}><option value="vehicle">Vehicle</option><option value="map">Map</option><option value="ui">UI app</option><option value="script">Game-engine script</option></select></label><label className="field"><span>Name</span><input value={name} onChange={event => changeName(event.target.value)} placeholder="My BeamNG Mod"/></label><label className="field"><span>Mod ID</span><input value={modID} onChange={event => { setManualID(true); setModID(event.target.value) }} placeholder="my_beamng_mod"/></label><label className="field"><span>Author</span><input value={author} onChange={event => setAuthor(event.target.value)} placeholder="Your name"/></label><label className="field"><span>Version</span><input value={version} onChange={event => setVersion(event.target.value)} placeholder="0.1.0"/></label><label className="field field--wide"><span>Description</span><textarea value={description} onChange={event => setDescription(event.target.value)} placeholder="What this mod changes"/></label></div><Button icon="plus" tone="primary" disabled={!name.trim() || !modID.trim() || !version.trim() || creating} onClick={createManual}>{creating ? 'Creating project' : 'Create project'}</Button></div>}</section>
}

function inferNewMod(prompt: string, defaultAuthor: string): NewModRequest { const source = prompt.trim(); const lower = source.toLowerCase(); const kind = /\b(map|level|terrain|road|track)\b/.test(lower) ? 'map' : /\b(ui|interface|dashboard|app|widget)\b/.test(lower) ? 'ui' : /\b(lua|script|extension|game engine)\b/.test(lower) ? 'script' : 'vehicle'; const cleaned = source.replace(/^(please\s+)?(create|make|build|start)\s+(me\s+)?(an?\s+)?/i, '').split(/[.!?\n]/)[0].trim(); const fallback = kind === 'map' ? 'New Map' : kind === 'ui' ? 'New UI App' : kind === 'script' ? 'New Script Mod' : 'New Vehicle Mod'; const name = (cleaned || fallback).slice(0, 64); return { name, modId: slugify(name) || `new_${kind}_mod`, kind, author: defaultAuthor, version: '0.1.0', description: source } }
function slugify(value: string) { return value.toLowerCase().trim().replace(/[^a-z0-9]+/g, '_').replace(/^_+|_+$/g, '').slice(0, 64) }
function friendlyEvent(type: string) { return type.replace(/_/g, ' ') }
function inferToolName(data: Record<string, unknown>) { const direct = data?.toolName; if (typeof direct === 'string') return direct; const nested = data?.toolCall; return nested && typeof nested === 'object' && typeof (nested as Record<string, unknown>).name === 'string' ? String((nested as Record<string, unknown>).name) : '' }

interface AgentTask { text: string; status: 'pending' | 'active' | 'complete' | 'blocked' }
function extractAgentTasks(lines: ActivityLine[], goal: string, running: boolean): AgentTask[] {
  let found: AgentTask[] = []
  const visit = (value: unknown) => {
    if (!value || typeof value !== 'object') return
    if (Array.isArray(value)) { value.forEach(visit); return }
    const record = value as Record<string, unknown>
    if (Array.isArray(record.list)) {
      const candidate: AgentTask[] = []
      for (const phase of record.list) {
        if (!phase || typeof phase !== 'object') continue
        const phaseRecord = phase as Record<string, unknown>
        if (Array.isArray(phaseRecord.items)) for (const item of phaseRecord.items) {
          if (typeof item === 'string') candidate.push({ text: item, status: 'pending' })
          else if (item && typeof item === 'object') candidate.push(taskFromRecord(item as Record<string, unknown>))
        }
      }
      if (candidate.length) found = candidate
    }
    if (Array.isArray(record.tasks)) {
      const candidate = record.tasks.map(item => typeof item === 'string' ? { text: item, status: 'pending' as const } : item && typeof item === 'object' ? taskFromRecord(item as Record<string, unknown>) : null).filter((item): item is AgentTask => Boolean(item))
      if (candidate.length) found = candidate
    }
    Object.values(record).forEach(visit)
  }
  for (const line of lines) visit(line.data)
  if (found.length) return found
  if (!goal.trim()) return []
  return [{ text: goal.trim(), status: running ? 'active' : agentFinished(lines) ? 'complete' : 'pending' }]
}
function taskFromRecord(record: Record<string, unknown>): AgentTask { const text = String(record.task ?? record.text ?? record.content ?? 'Task'); const raw = String(record.status ?? record.state ?? '').toLowerCase(); const status: AgentTask['status'] = raw.includes('complete') || raw === 'done' ? 'complete' : raw.includes('block') ? 'blocked' : raw.includes('progress') || raw.includes('active') || raw.includes('doing') ? 'active' : 'pending'; return { text, status } }
function agentFinished(lines: ActivityLine[]) { return lines.some(line => line.type === 'finished' && line.message === 'complete') }
function tokenCount(value: unknown): number {
  if (!value || typeof value !== 'object') return 0
  if (Array.isArray(value)) {
    let total = 0
    for (const item of value) total += tokenCount(item)
    return total
  }
  const record = value as Record<string, unknown>
  const componentKeys = ['inputTokens', 'outputTokens', 'cacheReadTokens', 'cacheWriteTokens', 'reasoningTokens']
  const components = componentKeys.reduce((total, key) => total + (typeof record[key] === 'number' ? Number(record[key]) : 0), 0)
  if (components > 0) return components
  if (typeof record.totalTokens === 'number') return Number(record.totalTokens)
  let total = 0
  for (const item of Object.values(record)) total += tokenCount(item)
  return total
}
