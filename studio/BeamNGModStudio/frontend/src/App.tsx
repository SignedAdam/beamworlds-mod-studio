import { useCallback, useEffect, useRef, useState } from 'react'
import { Events } from '@wailsio/runtime'
import { AppService as API } from '../bindings/github.com/SignedAdam/beamng-mod-studio/index.js'
import type { AIUsage, AppConfig, AppSettings, Dashboard, EntityDetail, LibraryItem, NewModRequest, OrganizationState, ProfileProgress, ScanProgress, SettingsUpdate, WorkspaceDetail, WorkspaceRecord } from '../bindings/github.com/SignedAdam/beamng-mod-studio/models.js'
import { ActivityView } from './ActivityView'
import { Inspector } from './Inspector'
import { LibraryView } from './LibraryView'
import { ModMaker } from './ModMaker'
import { ProfilesView } from './ProfilesView'
import { Icon } from './icons'
import { Button } from './ui'

type View = 'library' | 'workspaces' | 'profiles' | 'activity'
type ToastTone = 'success' | 'error' | 'info'

interface ToastState {
  message: string
  tone: ToastTone
  visible: boolean
}

interface EditorStatus {
  path: string
  dirty: boolean
}

function App() {
  const [view, setView] = useState<View>('library')
  const [config, setConfig] = useState<AppConfig | null>(null)
  const [settings, setSettings] = useState<AppSettings | null>(null)
  const [usage, setUsage] = useState<AIUsage | null>(null)
  const [settingsOpen, setSettingsOpen] = useState(false)
  const [dashboard, setDashboard] = useState<Dashboard | null>(null)
  const [items, setItems] = useState<LibraryItem[]>([])
  const [workspaces, setWorkspaces] = useState<WorkspaceRecord[]>([])
  const [allItems, setAllItems] = useState<LibraryItem[]>([])
  const [organization, setOrganization] = useState<OrganizationState | null>(null)
  const [folderID, setFolderID] = useState('all')
  const [profileProgress, setProfileProgress] = useState<ProfileProgress | null>(null)
  const [openMode, setOpenMode] = useState<'editor' | 'virgil' | null>(null)
  const [status, setStatus] = useState('all')
  const [kind, setKind] = useState('all')
  const [query, setQuery] = useState('')
  const [selectedItem, setSelectedItem] = useState<LibraryItem | null>(null)
  const [entityDetail, setEntityDetail] = useState<EntityDetail | null>(null)
  const [entityLoading, setEntityLoading] = useState(false)
  const [creatingWorkspace, setCreatingWorkspace] = useState(false)
  const [selectedWorkspaceID, setSelectedWorkspaceID] = useState('')
  const [workspaceDetail, setWorkspaceDetail] = useState<WorkspaceDetail | null>(null)
  const [workspaceLoading, setWorkspaceLoading] = useState(false)
  const [editorStatus, setEditorStatus] = useState<EditorStatus>({ path: '', dirty: false })
  const [scan, setScan] = useState<ScanProgress | null>(null)
  const [scanning, setScanning] = useState(false)
  const [loading, setLoading] = useState(true)
  const [toast, setToast] = useState<ToastState>({ message: '', tone: 'info', visible: false })
  const toastTimer = useRef<number>()
  const autoScanStarted = useRef(false)

  const notify = useCallback((message: string, tone: ToastTone = 'info') => {
    setToast({ message, tone, visible: true })
    window.clearTimeout(toastTimer.current)
    toastTimer.current = window.setTimeout(() => setToast(current => ({ ...current, visible: false })), 5000)
  }, [])

  const handleError = useCallback((error: unknown) => {
    let message = 'Unexpected application error'
    if (error instanceof Error) message = error.message
    else if (typeof error === 'string') message = error
    else if (error && typeof error === 'object' && 'message' in error && typeof error.message === 'string') message = error.message
    notify(message, 'error')
  }, [notify])

  const loadShell = useCallback(async () => {
    const [nextConfig, nextDashboard, workspaceResult] = await Promise.all([API.Config(), API.Dashboard(), API.ListWorkspaces()])
    const nextWorkspaces = workspaceResult ?? []
    setConfig(nextConfig)
    setDashboard(nextDashboard)
    setWorkspaces(nextWorkspaces)
    if (!selectedWorkspaceID && nextWorkspaces.length) setSelectedWorkspaceID(nextWorkspaces[0].id)
    return nextDashboard
  }, [selectedWorkspaceID])

  const loadLibrary = useCallback(async () => {
    const nextItems = await API.ListLibrary(status, kind, query, folderID) ?? []
    setItems(nextItems)
    if (selectedItem) {
      const replacement = nextItems.find(item => item.entityId === selectedItem.entityId)
      if (replacement) setSelectedItem(replacement)
      else {
        setSelectedItem(null)
        setEntityDetail(null)
      }
    }
  }, [status, kind, query, folderID, selectedItem?.entityId])
  const loadOrganization = useCallback(async () => {
    const [nextOrganization, nextItems] = await Promise.all([API.Organization(), API.ListLibrary('all', 'all', '', 'all')])
    setOrganization(nextOrganization)
    setAllItems(nextItems ?? [])
  }, [])

  const reloadWorkspace = useCallback(async () => {
    if (!selectedWorkspaceID) return
    const [nextDetail, workspaceResult, nextDashboard] = await Promise.all([API.GetWorkspace(selectedWorkspaceID), API.ListWorkspaces(), API.Dashboard()])
    setWorkspaceDetail(nextDetail)
    setWorkspaces(workspaceResult ?? [])
    setDashboard(nextDashboard)
  }, [selectedWorkspaceID])

  const startScan = useCallback(async () => {
    setScanning(true)
    setScan(current => current ? { ...current, done: false, error: '' } : null)
    try {
      const summary = await API.ScanLibrary()
      notify(`Processed ${summary.analyzed.toLocaleString()} archives${summary.cached > 0 ? ` · ${summary.cached.toLocaleString()} cached` : ''}`, summary.failed ? 'info' : 'success')
    } catch (error) {
      if (!(error instanceof Error) || !error.message.toLowerCase().includes('canceled')) handleError(error)
    } finally {
      setScanning(false)
      try {
        await Promise.all([loadLibrary(), loadShell(), loadOrganization()])
      } catch (error) {
        handleError(error)
      }
    }
  }, [handleError, loadLibrary, loadOrganization, loadShell, notify])

  useEffect(() => {
    let active = true
    API.Dashboard().then(nextDashboard => {
      if (!active) return Promise.reject(new Error('cancelled'))
      setDashboard(nextDashboard)
      return Promise.all([API.Config(), API.ListWorkspaces(), API.ListLibrary('all', 'all', '', 'all'), API.Organization(), API.Settings(), API.AIUsage(), Promise.resolve(nextDashboard)])
    }).then(([nextConfig, workspaceResult, itemResult, nextOrganization, nextSettings, nextUsage, nextDashboard]) => {
      if (!active) return
      const nextWorkspaces = workspaceResult ?? []
      setConfig(nextConfig)
      setWorkspaces(nextWorkspaces)
      setItems(itemResult ?? [])
      setAllItems(itemResult ?? [])
      setOrganization(nextOrganization)
      setSettings(nextSettings)
      setUsage(nextUsage)
      if (nextWorkspaces.length) setSelectedWorkspaceID(nextWorkspaces[0].id)
      setLoading(false)
      if (nextDashboard.entities === 0 && !autoScanStarted.current) {
        autoScanStarted.current = true
        void startScan()
      }
    }).catch(error => {
      if (active && (!(error instanceof Error) || error.message !== 'cancelled')) {
        setLoading(false)
        handleError(error)
      }
    })
    return () => { active = false }
  }, [])

  useEffect(() => {
    const timer = setTimeout(() => { void loadLibrary().catch(handleError) }, 120)
    return () => clearTimeout(timer)
  }, [status, kind, query, folderID])

  useEffect(() => {
    document.documentElement.dataset.theme = settings?.theme ?? 'dark'
  }, [settings?.theme])

  useEffect(() => {
    const stopScan = Events.On('library:scan', event => {
      setScan(event.data)
      setScanning(!event.data.done)
    })
    const stopItem = Events.On('library:item', event => {
      setItems(current => {
        const index = current.findIndex(item => item.entityId === event.data.entityId)
        if (index < 0) return [event.data, ...current]
        const next = [...current]
        next[index] = event.data
        return next
      })
    })
    const stopProfile = Events.On('profile:progress', event => {
      setProfileProgress(event.data)
    })
    return () => {
      stopScan()
      stopItem()
      stopProfile()
      clearTimeout(toastTimer.current)
    }
  }, [])

  useEffect(() => {
    if (view !== 'workspaces' || !selectedWorkspaceID) return
    setWorkspaceLoading(true)
    API.GetWorkspace(selectedWorkspaceID).then(setWorkspaceDetail).catch(handleError).finally(() => setWorkspaceLoading(false))
  }, [view, selectedWorkspaceID])

  useEffect(() => {
    if (view !== 'workspaces') setEditorStatus({ path: '', dirty: false })
  }, [view])

  const selectItem = async (item: LibraryItem) => {
    setSelectedItem(item)
    setEntityDetail(null)
    setEntityLoading(true)
    try {
      setEntityDetail(await API.GetEntity(item.entityId))
    } catch (error) {
      handleError(error)
    } finally {
      setEntityLoading(false)
    }
  }

  const createWorkspace = async (mode: 'editor' | 'virgil') => {
    if (!selectedItem) return
    setCreatingWorkspace(true)
    try {
      const detail = await API.CreateWorkspace(selectedItem.entityId)
      setWorkspaceDetail(detail)
      setSelectedWorkspaceID(detail.workspace.id)
      setWorkspaces(await API.ListWorkspaces() ?? [])
      setSelectedItem(null)
      setOpenMode(mode)
      setView('workspaces')
      notify(`${detail.entity.displayName} opened in ModMaker`, 'success')
    } catch (error) {
      handleError(error)
    } finally {
      setCreatingWorkspace(false)
    }
  }

  const createNewMod = async (request: NewModRequest, virgilPrompt = '', modelOverride = '') => {
    const detail = await API.CreateNewMod(request)
    setWorkspaceDetail(detail)
    setSelectedWorkspaceID(detail.workspace.id)
    const [nextWorkspaces, nextDashboard, nextSettings] = await Promise.all([API.ListWorkspaces(), API.Dashboard(), API.Settings()])
    setWorkspaces(nextWorkspaces ?? [])
    setDashboard(nextDashboard)
    setSettings(nextSettings)
    setView('workspaces')
    if (virgilPrompt.trim()) {
      const run = await API.StartAgent(detail.workspace.id, virgilPrompt.trim(), modelOverride)
      setWorkspaceDetail({ ...detail, agentRuns: [run, ...(detail.agentRuns ?? [])] } as WorkspaceDetail)
      setOpenMode('virgil')
      setUsage(await API.AIUsage())
      notify(`Virgil started on ${detail.entity.displayName}`, 'info')
    } else {
      setOpenMode('editor')
      notify(`${detail.entity.displayName} project created`, 'success')
    }
    await loadOrganization()
  }

  const saveSettings = async (update: SettingsUpdate) => {
    try {
      const next = await API.SaveSettings(update)
      setSettings(next)
      if (next.showAIUsage) setUsage(await API.AIUsage())
      notify('Settings saved', 'success')
      return true
    } catch (error) {
      handleError(error)
      return false
    }
  }

  const createFolder = async (name: string) => {
    try { setOrganization(await API.CreateLibraryFolder(name, '')); await loadLibrary(); notify(`Created folder ${name}`, 'success') } catch (error) { handleError(error) }
  }
  const renameFolder = async (id: string, name: string) => {
    try { setOrganization(await API.RenameLibraryFolder(id, name)); notify('Folder renamed', 'success') } catch (error) { handleError(error) }
  }
  const deleteFolder = async (id: string) => {
    try { setOrganization(await API.DeleteLibraryFolder(id)); setFolderID('all'); await loadLibrary(); notify('Folder removed; mods are now unfiled', 'success') } catch (error) { handleError(error) }
  }
  const moveSelectedItem = async (nextFolderID: string) => {
    if (!selectedItem) return
    try {
      await API.MoveLibraryItem(selectedItem.entityId, nextFolderID)
      setSelectedItem({ ...selectedItem, folderId: nextFolderID } as LibraryItem)
      await Promise.all([loadLibrary(), loadOrganization()])
      notify(nextFolderID ? 'Mod moved to library folder' : 'Mod moved to Unfiled', 'success')
    } catch (error) { handleError(error) }
  }

  const cancelScan = async () => {
    try {
      await API.CancelScan()
    } catch (error) {
      handleError(error)
    }
  }

  const changeView = (next: View) => {
    setView(next)
    setSelectedItem(null)
    setEntityDetail(null)
  }

  const startupCount = dashboard?.entities ?? 0
  const activeWorkspace = workspaces.find(workspace => workspace.id === selectedWorkspaceID)
  const usageLimits = usage?.limits?.filter(limit => limit.status === 'ok').slice(0, 2) ?? []

  return <div className="app-shell">
    <aside className="app-sidebar">
      <div className="brand-lockup" aria-label="BeamNG Mod Studio">
        <span className="beam-mark" aria-hidden="true"><i/><i/><i/><i/></span>
        <div><strong>BeamNG</strong><span>Mod Studio</span></div>
      </div>
      <nav className="main-nav" aria-label="Primary navigation">
        <button className={view === 'library' ? 'is-active' : ''} onClick={() => changeView('library')}><Icon name="library"/><span>Library</span><small>{dashboard?.entities ?? 0}</small></button>
        <button className={view === 'workspaces' ? 'is-active' : ''} onClick={() => changeView('workspaces')}><Icon name="workspace"/><span>ModMaker</span><small>{workspaces.length}</small></button>
        <button className={view === 'activity' ? 'is-active' : ''} onClick={() => changeView('activity')}><Icon name="activity"/><span>Activity</span></button>
        <button className={view === 'profiles' ? 'is-active' : ''} onClick={() => changeView('profiles')}><Icon name="play"/><span>Profiles</span><small>{organization?.profiles?.length ?? 0}</small></button>
      </nav>
      <button className="sidebar-settings" onClick={() => setSettingsOpen(true)}><Icon name="settings" size={16}/><span>Settings</span></button>
    </aside>

    <main className={`app-main ${selectedItem && view === 'library' ? 'has-inspector' : ''}`}>
      {loading ? <div className="splash"><span className="beam-mark beam-mark--large"><i/><i/><i/><i/></span><div className="splash__line"/><p>{startupCount > 0 ? `Loading ${startupCount.toLocaleString()} mods` : 'Loading mod library'}</p><span>This may take a few seconds.</span></div> : <>
        {view === 'library' && <LibraryView items={items} folders={organization?.folders ?? []} dashboard={dashboard} scan={scan} scanning={scanning} status={status} kind={kind} query={query} folderID={folderID} selectedID={selectedItem?.entityId ?? ''} onStatusChange={setStatus} onKindChange={setKind} onQueryChange={setQuery} onFolderChange={setFolderID} onCreateFolder={name => void createFolder(name)} onRenameFolder={(id, name) => void renameFolder(id, name)} onDeleteFolder={id => void deleteFolder(id)} onSelect={item => void selectItem(item)} onScan={() => void startScan()} onCancelScan={() => void cancelScan()}/>}
        {view === 'workspaces' && <ModMaker workspaces={workspaces} detail={workspaceDetail} selectedID={selectedWorkspaceID} loading={workspaceLoading} defaultAuthor={settings?.defaultAuthor ?? ''} openMode={openMode} onOpenModeHandled={() => setOpenMode(null)} onSelect={setSelectedWorkspaceID} onReload={reloadWorkspace} onCreateMod={createNewMod} onAgentStarted={() => { void API.AIUsage().then(setUsage).catch(handleError) }} onEditorStatus={setEditorStatus} onNotify={notify} onError={handleError}/>}
        {view === 'profiles' && <ProfilesView organization={organization} items={allItems} progress={profileProgress} onOrganization={setOrganization} onNotify={notify} onError={handleError}/>}
        {view === 'activity' && <ActivityView config={config} dashboard={dashboard} onScan={() => void startScan()}/>}
      </>}
    </main>

    {selectedItem && view === 'library' && <Inspector item={selectedItem} detail={entityDetail} folders={organization?.folders ?? []} loading={entityLoading} creatingWorkspace={creatingWorkspace} onClose={() => { setSelectedItem(null); setEntityDetail(null) }} onMoveFolder={folder => void moveSelectedItem(folder)} onCreateWorkspace={mode => void createWorkspace(mode)}/>}

    <footer className="app-statusbar" aria-label="Application status">
      <div><span className="status-led status-led--ready"/>{scanning ? `Scanning ${scan?.analyzed ?? 0}/${scan?.discovered ?? 0}` : 'Ready'}</div>
      <div>{view === 'workspaces' && activeWorkspace ? <><strong>{activeWorkspace.displayName}</strong>{editorStatus.path && <span title={editorStatus.path}>{editorStatus.dirty ? 'Unsaved' : 'Saved'} · {editorStatus.path}</span>}</> : <span>{(dashboard?.entities ?? items.length).toLocaleString()} mods</span>}</div>
      {settings?.showAIUsage && usage?.hasRuns && <div className="usage-compact"><Icon name="agent" size={13}/>{usageLimits.map(limit => <UsageChip key={`${limit.provider}-${limit.label}`} limit={limit}/>)}{usage.totalTokens > 0 && <span>{usage.totalTokens.toLocaleString()} tokens</span>}</div>}
    </footer>

    <SettingsPanel open={settingsOpen} settings={settings} usage={usage} onClose={() => setSettingsOpen(false)} onSave={saveSettings}/>
    <div className={`toast toast--${toast.tone} ${toast.visible ? 'is-visible' : ''}`} role="status" aria-live="polite"><Icon name={toast.tone === 'error' ? 'error' : toast.tone === 'success' ? 'check' : 'activity'} size={17}/><span>{toast.message}</span><button onClick={() => setToast(current => ({ ...current, visible: false }))} aria-label="Dismiss"><Icon name="close" size={14}/></button></div>
  </div>
}

function UsageChip({ limit }: { limit: NonNullable<AIUsage['limits']>[number] }) {
  const amount = limit.unit === 'percent' ? `${Math.round(limit.used)}%` : `${Math.round(limit.remaining)} ${limit.unit}`
  return <span title={`${limit.provider} · resets ${new Date(limit.resetsAt).toLocaleString()}`}>{limit.windowId || limit.label} {amount}</span>
}

function SettingsPanel({ open, settings, usage, onClose, onSave }: { open: boolean; settings: AppSettings | null; usage: AIUsage | null; onClose: () => void; onSave: (update: SettingsUpdate) => Promise<boolean> }) {
  const [draft, setDraft] = useState<AppSettings | null>(settings)
  const [apiKey, setAPIKey] = useState('')
  const [clearAPIKey, setClearAPIKey] = useState(false)
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    if (!open) return
    setDraft(settings)
    setAPIKey('')
    setClearAPIKey(false)
  }, [open, settings])

  if (!open || !draft) return null

  const submit = async () => {
    setSaving(true)
    const saved = await onSave({
      theme: draft.theme,
      defaultAuthor: draft.defaultAuthor,
      agentProfile: draft.agentProfile,
      agentModel: draft.agentModel,
      contextMode: draft.contextMode,
      showAIUsage: draft.showAIUsage,
      apiKey,
      clearApiKey: clearAPIKey,
    })
    setSaving(false)
    if (saved) onClose()
  }

  return <div className="modal-backdrop" role="presentation" onMouseDown={event => { if (event.currentTarget === event.target) onClose() }}>
    <section className="settings-panel" role="dialog" aria-modal="true" aria-labelledby="settings-title">
      <header><div><h2 id="settings-title">Settings</h2><span>Interface, author defaults, and Virgil</span></div><button className="icon-button" onClick={onClose} aria-label="Close settings"><Icon name="close"/></button></header>
      <div className="settings-body">
        <fieldset><legend>Interface</legend><div className="settings-row"><label>Theme<span>Applied to every pane and editor surface.</span></label><div className="segmented"><button className={draft.theme === 'dark' ? 'is-active' : ''} onClick={() => setDraft({ ...draft, theme: 'dark' })}>Glass black</button><button className={draft.theme === 'light' ? 'is-active' : ''} onClick={() => setDraft({ ...draft, theme: 'light' })}>Bright glass</button></div></div></fieldset>
        <fieldset><legend>Project defaults</legend><label className="settings-field"><span>Author</span><input value={draft.defaultAuthor} onChange={event => setDraft({ ...draft, defaultAuthor: event.target.value })} placeholder="Used by new mods" maxLength={80}/><small>The first author entered in the manual wizard becomes this default.</small></label></fieldset>
        <fieldset><legend>Virgil</legend>
          <div className="settings-grid">
            <label className="settings-field"><span>Connection</span><select value={draft.agentProfile} onChange={event => setDraft({ ...draft, agentProfile: event.target.value })}><option value="omp">OMP default</option><option value="codex">ChatGPT / Codex</option><option value="claude">Claude</option><option value="openrouter">OpenRouter</option><option value="openai">OpenAI API</option></select></label>
            <label className="settings-field"><span>Model</span><input value={draft.agentModel} onChange={event => setDraft({ ...draft, agentModel: event.target.value })} placeholder="Use profile default" maxLength={120}/></label>
            <label className="settings-field"><span>Context</span><select value={draft.contextMode} onChange={event => setDraft({ ...draft, contextMode: event.target.value })}><option value="focused">Focused · category only</option><option value="balanced">Balanced · project and category</option><option value="deep">Deep · cross-system references</option></select></label>
            <label className="settings-field"><span>API key</span><input type="password" value={apiKey} onChange={event => { setAPIKey(event.target.value); setClearAPIKey(false) }} placeholder={draft.hasApiKey && !clearAPIKey ? 'Stored securely for this user' : 'Optional; OMP OAuth needs no key'} autoComplete="off"/><small>Windows stores entered keys with DPAPI. ChatGPT and Claude subscriptions use accounts already connected in OMP.</small></label>
          </div>
          {draft.hasApiKey && <button className={`text-button ${clearAPIKey ? 'is-active' : ''}`} onClick={() => { setClearAPIKey(!clearAPIKey); setAPIKey('') }}>{clearAPIKey ? 'Stored API key will be removed' : 'Remove stored API key'}</button>}
        </fieldset>
        <fieldset><legend>Status bar</legend><label className="toggle-row"><input type="checkbox" checked={draft.showAIUsage} onChange={event => setDraft({ ...draft, showAIUsage: event.target.checked })}/><span>Show authenticated provider usage after Virgil has been used</span></label>
          {usage?.hasRuns && <div className="usage-table"><div><span>ModMaker runs</span><strong>{usage.runCount.toLocaleString()}</strong></div>{usage.totalTokens > 0 && <div><span>Recorded tokens</span><strong>{usage.totalTokens.toLocaleString()}</strong></div>}{(usage.limits ?? []).map(limit => <div key={`${limit.provider}-${limit.label}`}><span>{limit.provider} · {limit.label}</span><strong>{limit.unit === 'percent' ? `${Math.round(limit.used)}% used` : `${Math.round(limit.remaining)} ${limit.unit} left`}</strong></div>)}{usage.usageError && <p>{usage.usageError}</p>}</div>}
        </fieldset>
      </div>
      <footer><Button onClick={onClose}>Cancel</Button><Button tone="primary" disabled={saving} onClick={() => void submit()}>{saving ? 'Saving' : 'Apply settings'}</Button></footer>
    </section>
  </div>
}

export default App
