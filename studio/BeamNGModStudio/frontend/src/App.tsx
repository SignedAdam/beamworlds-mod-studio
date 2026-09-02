import { useCallback, useEffect, useRef, useState } from 'react'
import { Events } from '@wailsio/runtime'
import { AppService as API } from '../bindings/github.com/SignedAdam/beamng-mod-studio/index.js'
import type { AIUsage, AppConfig, AppSettings, Dashboard, EntityDetail, LibraryItem, ModTag, NewModRequest, OrganizationState, ProfileProgress, ScanProgress, SettingsUpdate, SetupState, WorkspaceDetail, WorkspaceRecord } from '../bindings/github.com/SignedAdam/beamng-mod-studio/models.js'
import { ActivityView } from './ActivityView'
import { Inspector } from './Inspector'
import { LibraryView } from './LibraryView'
import { ModMaker } from './ModMaker'
import { ProfilesView } from './ProfilesView'
import { SetupWizard } from './SetupWizard'
import { BeamWorldsMark, Icon } from './icons'
import { Button, formatBytes } from './ui'

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
  sizeBytes: number
}

function App() {
  const [view, setView] = useState<View>('library')
  const [config, setConfig] = useState<AppConfig | null>(null)
  const [settings, setSettings] = useState<AppSettings | null>(null)
  const [usage, setUsage] = useState<AIUsage | null>(null)
  const [settingsOpen, setSettingsOpen] = useState(false)
  const [sidebarCollapsed, setSidebarCollapsed] = useState(() => window.localStorage.getItem('beamworlds.sidebar-collapsed') === 'true')
  const [setupState, setSetupState] = useState<SetupState | null>(null)
  const [setupOpen, setSetupOpen] = useState(false)
  const [dashboard, setDashboard] = useState<Dashboard | null>(null)
  const [items, setItems] = useState<LibraryItem[]>([])
  const [libraryLoading, setLibraryLoading] = useState(false)
  const [workspaces, setWorkspaces] = useState<WorkspaceRecord[]>([])
  const [allItems, setAllItems] = useState<LibraryItem[]>([])
  const [organization, setOrganization] = useState<OrganizationState | null>(null)
  const [folderID, setFolderID] = useState('all')
  const [profileProgress, setProfileProgress] = useState<ProfileProgress | null>(null)
  const [openMode, setOpenMode] = useState<'editor' | 'virgil' | null>(null)
  const [query, setQuery] = useState('')
  const [selectedItem, setSelectedItem] = useState<LibraryItem | null>(null)
  const [entityDetail, setEntityDetail] = useState<EntityDetail | null>(null)
  const [entityLoading, setEntityLoading] = useState(false)
  const [creatingWorkspace, setCreatingWorkspace] = useState(false)
  const [selectedWorkspaceID, setSelectedWorkspaceID] = useState('')
  const [workspaceDetail, setWorkspaceDetail] = useState<WorkspaceDetail | null>(null)
  const [workspaceLoading, setWorkspaceLoading] = useState(false)
  const [editorStatus, setEditorStatus] = useState<EditorStatus>({ path: '', dirty: false, sizeBytes: 0 })
  const [scan, setScan] = useState<ScanProgress | null>(null)
  const [scanning, setScanning] = useState(false)
  const [loading, setLoading] = useState(true)
  const [toast, setToast] = useState<ToastState>({ message: '', tone: 'info', visible: false })
  const toastTimer = useRef<number>()
  const autoScanStarted = useRef(false)
  const libraryLoadVersion = useRef(0)

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
    return nextDashboard
  }, [])

  const loadLibrary = useCallback(async () => {
    const requestVersion = libraryLoadVersion.current
    const nextItems = await API.ListLibrary('all', 'all', query, folderID) ?? []
    if (requestVersion !== libraryLoadVersion.current) return
    setItems(nextItems)
    if (selectedItem) {
      const replacement = nextItems.find(item => item.entityId === selectedItem.entityId)
      if (replacement) setSelectedItem(replacement)
      else {
        setSelectedItem(null)
        setEntityDetail(null)
      }
    }
  }, [query, folderID, selectedItem?.entityId])
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
    void (async () => {
      try {
        const nextSetup = await API.GetSetupState()
        if (!active) return
        setSetupState(nextSetup)
        if (nextSetup.required) {
          setLoading(false)
          return
        }
        const nextDashboard = await API.Dashboard()
        if (!active) return
        setDashboard(nextDashboard)
        const [nextConfig, workspaceResult, itemResult, nextOrganization, nextSettings, nextUsage] = await Promise.all([API.Config(), API.ListWorkspaces(), API.ListLibrary('all', 'all', '', 'all'), API.Organization(), API.Settings(), API.AIUsage()])
        if (!active) return
        const nextWorkspaces = workspaceResult ?? []
        setConfig(nextConfig)
        setWorkspaces(nextWorkspaces)
        setItems(itemResult ?? [])
        setAllItems(itemResult ?? [])
        setOrganization(nextOrganization)
        setSettings(nextSettings)
        setUsage(nextUsage)
        setLoading(false)
        if (nextDashboard.entities === 0 && !autoScanStarted.current) {
          autoScanStarted.current = true
          void startScan()
        }
      } catch (error) {
        if (active) {
          setLoading(false)
          handleError(error)
        }
      }
    })()
    return () => { active = false }
  }, [])

  useEffect(() => {
    if (!setupState || setupState.required) return
    const requestVersion = ++libraryLoadVersion.current
    setLibraryLoading(true)
    const timer = setTimeout(() => {
      void loadLibrary().catch(error => {
        if (requestVersion === libraryLoadVersion.current) handleError(error)
      }).finally(() => {
        if (requestVersion === libraryLoadVersion.current) setLibraryLoading(false)
      })
    }, 120)
    return () => clearTimeout(timer)
  }, [query, folderID, setupState?.required])

  useEffect(() => {
    const root = document.documentElement
    root.dataset.theme = settings?.theme ?? 'dark'
    if (!settings) return
    const colors: Record<string, string> = {
      '--user-emphasis': settings.emphasisColor,
      '--user-active-tab': settings.activeTabColor,
      '--user-dark-surface': settings.darkSurfaceColor,
      '--user-dark-border': settings.darkBorderColor,
      '--user-dark-text': settings.darkTextColor,
      '--user-light-surface': settings.lightSurfaceColor,
      '--user-light-border': settings.lightBorderColor,
      '--user-light-text': settings.lightTextColor,
    }
    for (const [name, value] of Object.entries(colors)) root.style.setProperty(name, value)
  }, [settings])

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
    if (view !== 'workspaces') return
    if (!selectedWorkspaceID) {
      setWorkspaceDetail(null)
      return
    }
    setWorkspaceLoading(true)
    API.GetWorkspace(selectedWorkspaceID).then(setWorkspaceDetail).catch(handleError).finally(() => setWorkspaceLoading(false))
  }, [view, selectedWorkspaceID])

  useEffect(() => {
    if (view !== 'workspaces') setEditorStatus({ path: '', dirty: false, sizeBytes: 0 })
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
    try { setOrganization(await API.CreateLibraryFolder(name, '')); await loadLibrary(); notify(`Created collection ${name}`, 'success') } catch (error) { handleError(error) }
  }
  const renameFolder = async (id: string, name: string) => {
    try { setOrganization(await API.RenameLibraryFolder(id, name)); notify('Collection renamed', 'success') } catch (error) { handleError(error) }
  }
  const deleteFolder = async (id: string) => {
    try { setOrganization(await API.DeleteLibraryFolder(id)); setFolderID('all'); await loadLibrary(); notify('Collection removed; its mods are now Unfiled', 'success') } catch (error) { handleError(error) }
  }
  const moveSelectedItem = async (nextFolderID: string) => {
    if (!selectedItem) return
    try {
      await API.MoveLibraryItem(selectedItem.entityId, nextFolderID)
      setSelectedItem({ ...selectedItem, folderId: nextFolderID } as LibraryItem)
      await Promise.all([loadLibrary(), loadOrganization()])
      notify(nextFolderID ? 'Mod moved to collection' : 'Mod moved to Unfiled', 'success')
    } catch (error) { handleError(error) }
  }
  const createTag = async (name: string): Promise<ModTag | null> => {
    try {
      const next = await API.CreateModTag(name)
      setOrganization(next)
      notify(`Created tag ${name}`, 'success')
      return (next.tags ?? []).find(tag => tag.name.localeCompare(name, undefined, { sensitivity: 'base' }) === 0) ?? null
    } catch (error) {
      handleError(error)
      return null
    }
  }
  const renameTag = async (tagID: string, name: string) => {
    try {
      setOrganization(await API.RenameModTag(tagID, name))
      const renameItemTag = (item: LibraryItem) => ({ ...item, tags: (item.tags ?? []).map(tag => tag.id === tagID ? { ...tag, name } as ModTag : tag) } as LibraryItem)
      setSelectedItem(current => current ? renameItemTag(current) : current)
      setEntityDetail(current => current ? { ...current, item: renameItemTag(current.item) } as EntityDetail : current)
      await Promise.all([loadLibrary(), loadOrganization()])
      notify('Tag renamed', 'success')
    } catch (error) { handleError(error) }
  }
  const deleteTag = async (tagID: string) => {
    try {
      setOrganization(await API.DeleteModTag(tagID))
      const removeItemTag = (item: LibraryItem) => ({ ...item, tags: (item.tags ?? []).filter(tag => tag.id !== tagID) } as LibraryItem)
      setSelectedItem(current => current ? removeItemTag(current) : current)
      setEntityDetail(current => current ? { ...current, item: removeItemTag(current.item) } as EntityDetail : current)
      await Promise.all([loadLibrary(), loadOrganization()])
      notify('Tag deleted', 'success')
    } catch (error) { handleError(error) }
  }
  const setSelectedTags = async (tagIDs: string[]) => {
    if (!selectedItem) return
    try {
      const next = await API.SetLibraryItemTags(selectedItem.entityId, tagIDs)
      setSelectedItem(next)
      setEntityDetail(current => current ? { ...current, item: next } as EntityDetail : current)
      await Promise.all([loadLibrary(), loadOrganization()])
    } catch (error) { handleError(error) }
  }

  const cancelScan = async () => {
    try {
      await API.CancelScan()
    } catch (error) {
      handleError(error)
    }
  }

  const openSetup = async () => {
    try {
      setSetupState(await API.GetSetupState())
      setSettingsOpen(false)
      setSetupOpen(true)
    } catch (error) {
      handleError(error)
    }
  }

  const changeView = (next: View) => {
    setView(next)
    setSelectedItem(null)
    setEntityDetail(null)
  }

  const toggleSidebar = () => {
    setSidebarCollapsed(current => {
      window.localStorage.setItem('beamworlds.sidebar-collapsed', String(!current))
      return !current
    })
  }

  const toggleTheme = () => {
    if (!settings) return
    void saveSettings(settingsUpdate(settings, { theme: settings.theme === 'dark' ? 'light' : 'dark' }))
  }

  if (setupState && (setupState.required || setupOpen)) {
    return <SetupWizard state={setupState} required={setupState.required} onCancel={setupState.required ? undefined : () => setSetupOpen(false)} onError={handleError}/>
  }

  const startupCount = dashboard?.entities ?? 0
  const activeWorkspace = workspaces.find(workspace => workspace.id === selectedWorkspaceID)
  const usageLimits = usage?.limits?.filter(limit => limit.status === 'ok').slice(0, 2) ?? []

  return <div className={`app-shell ${sidebarCollapsed ? 'app-shell--sidebar-collapsed' : ''}`}>
    <aside id="app-sidebar" className="app-sidebar">
      <div className="brand-lockup" aria-label="BeamWorlds Mod Studio">
        <BeamWorldsMark/>
        <div><strong>BeamWorlds</strong><span>Mod Studio</span></div>
        <button className="sidebar-collapse" onClick={toggleSidebar} aria-controls="app-sidebar" aria-expanded={!sidebarCollapsed} aria-label={sidebarCollapsed ? 'Expand sidebar' : 'Collapse sidebar'} title={sidebarCollapsed ? 'Expand sidebar' : 'Collapse sidebar'}><Icon name="collapse" size={15}/></button>
      </div>
      <nav className="main-nav" aria-label="Primary navigation">
        <button className={view === 'library' ? 'is-active' : ''} onClick={() => changeView('library')} aria-label="Mod Library"><Icon name="library"/><span className="nav-label">Mod Library</span><small>{dashboard?.entities ?? 0}</small><span className="nav-tooltip">Mod Library</span></button>
        <button className={view === 'workspaces' ? 'is-active' : ''} onClick={() => changeView('workspaces')} aria-label="ModMaker"><Icon name="workspace"/><span className="nav-label">ModMaker</span><small>{workspaces.length}</small><span className="nav-tooltip">ModMaker</span></button>
        <button className={view === 'activity' ? 'is-active' : ''} onClick={() => changeView('activity')} aria-label="Activity"><Icon name="activity"/><span className="nav-label">Activity</span><span className="nav-tooltip">Activity</span></button>
        <button className={view === 'profiles' ? 'is-active' : ''} onClick={() => changeView('profiles')} aria-label="Mod Profiles"><Icon name="play"/><span className="nav-label">Mod Profiles</span><small>{organization?.profiles?.length ?? 0}</small><span className="nav-tooltip">Mod Profiles</span></button>
      </nav>
      <div className="sidebar-actions">
        <button className="sidebar-theme" onClick={toggleTheme} aria-label={`Use ${settings?.theme === 'dark' ? 'light' : 'dark'} theme`}><Icon name={settings?.theme === 'dark' ? 'sun' : 'moon'} size={17}/><span>{settings?.theme === 'dark' ? 'Light theme' : 'Dark theme'}</span><span className="nav-tooltip">{settings?.theme === 'dark' ? 'Light theme' : 'Dark theme'}</span></button>
        <button className="sidebar-settings" onClick={() => setSettingsOpen(true)} aria-label="Settings"><Icon name="settings" size={17}/><span>Settings</span><span className="nav-tooltip">Settings</span></button>
      </div>
    </aside>

    <main className={`app-main ${selectedItem && view === 'library' ? 'has-inspector' : ''}`}>
      {loading ? <div className="splash"><BeamWorldsMark size={64}/><div className="splash__line"/><p>{startupCount > 0 ? `Loading ${startupCount.toLocaleString()} mods` : 'Loading mod library'}</p><span>This may take a few seconds.</span></div> : <>
        {view === 'library' && <LibraryView items={items} catalogItems={allItems} folders={organization?.folders ?? []} tags={organization?.tags ?? []} scan={scan} scanning={scanning} loading={libraryLoading} query={query} folderID={folderID} selectedID={selectedItem?.entityId ?? ''} onQueryChange={setQuery} onFolderChange={setFolderID} onCreateFolder={name => void createFolder(name)} onRenameFolder={(id, name) => void renameFolder(id, name)} onDeleteFolder={id => void deleteFolder(id)} onSelect={item => void selectItem(item)} onScan={() => void startScan()} onCancelScan={() => void cancelScan()}/>}
        {view === 'workspaces' && <ModMaker workspaces={workspaces} detail={workspaceDetail} selectedID={selectedWorkspaceID} loading={workspaceLoading} defaultAuthor={settings?.defaultAuthor ?? ''} showFileSizes={settings?.showFileSizes ?? true} openMode={openMode} onOpenModeHandled={() => setOpenMode(null)} onSelect={setSelectedWorkspaceID} onReload={reloadWorkspace} onCreateMod={createNewMod} onAgentStarted={() => { void API.AIUsage().then(setUsage).catch(handleError) }} onEditorStatus={setEditorStatus} onNotify={notify} onError={handleError}/>}
        {view === 'profiles' && <ProfilesView organization={organization} items={allItems} progress={profileProgress} onOrganization={setOrganization} onNotify={notify} onError={handleError}/>}
        {view === 'activity' && <ActivityView config={config} dashboard={dashboard} onScan={() => void startScan()}/>}
      </>}
    </main>

    {selectedItem && view === 'library' && <Inspector item={selectedItem} detail={entityDetail} folders={organization?.folders ?? []} tags={organization?.tags ?? []} loading={entityLoading} creatingWorkspace={creatingWorkspace} onClose={() => { setSelectedItem(null); setEntityDetail(null) }} onMoveFolder={folder => void moveSelectedItem(folder)} onSetTags={setSelectedTags} onCreateTag={createTag} onRenameTag={renameTag} onDeleteTag={deleteTag} onCreateWorkspace={mode => void createWorkspace(mode)} onError={handleError}/>}

    <footer className="app-statusbar" aria-label="Application status">
      <div><span className="status-led status-led--ready"/>{scanning ? `Scanning ${scan?.analyzed ?? 0}/${scan?.discovered ?? 0}` : 'Ready'}</div>
      <div>{view === 'workspaces' && activeWorkspace ? <><strong>{activeWorkspace.displayName}</strong>{editorStatus.path && <span title={editorStatus.path}>{editorStatus.dirty ? 'Unsaved' : 'Saved'} · {editorStatus.path} · {formatBytes(editorStatus.sizeBytes)}</span>}</> : <span>{(dashboard?.entities ?? items.length).toLocaleString()} mods</span>}</div>
      {settings?.showAIUsage && usage?.hasRuns && <div className="usage-compact"><Icon name="agent" size={13}/>{usageLimits.map(limit => <UsageChip key={`${limit.provider}-${limit.label}`} limit={limit}/>)}{usage.totalTokens > 0 && <span>{usage.totalTokens.toLocaleString()} tokens</span>}</div>}
    </footer>

    <SettingsPanel open={settingsOpen} settings={settings} usage={usage} onClose={() => setSettingsOpen(false)} onSave={saveSettings} onOpenSetup={() => void openSetup()}/>
    <div className={`toast toast--${toast.tone} ${toast.visible ? 'is-visible' : ''}`} role="status" aria-live="polite"><Icon name={toast.tone === 'error' ? 'error' : toast.tone === 'success' ? 'check' : 'activity'} size={17}/><span>{toast.message}</span><button onClick={() => setToast(current => ({ ...current, visible: false }))} aria-label="Dismiss"><Icon name="close" size={14}/></button></div>
  </div>
}

function UsageChip({ limit }: { limit: NonNullable<AIUsage['limits']>[number] }) {
  const amount = limit.unit === 'percent' ? `${Math.round(limit.used)}%` : `${Math.round(limit.remaining)} ${limit.unit}`
  return <span title={`${limit.provider} · resets ${new Date(limit.resetsAt).toLocaleString()}`}>{limit.windowId || limit.label} {amount}</span>
}

function settingsUpdate(settings: AppSettings, overrides: Partial<SettingsUpdate> = {}): SettingsUpdate {
  return {
    theme: settings.theme,
    defaultAuthor: settings.defaultAuthor,
    agentProfile: settings.agentProfile,
    agentModel: settings.agentModel,
    contextMode: settings.contextMode,
    showAIUsage: settings.showAIUsage,
    showFileSizes: settings.showFileSizes,
    emphasisColor: settings.emphasisColor,
    activeTabColor: settings.activeTabColor,
    darkSurfaceColor: settings.darkSurfaceColor,
    darkBorderColor: settings.darkBorderColor,
    darkTextColor: settings.darkTextColor,
    lightSurfaceColor: settings.lightSurfaceColor,
    lightBorderColor: settings.lightBorderColor,
    lightTextColor: settings.lightTextColor,
    preScanModel: settings.preScanModel,
    preScanReasoning: settings.preScanReasoning,
    fullScanModel: settings.fullScanModel,
    fullScanReasoning: settings.fullScanReasoning,
    apiKey: '',
    clearApiKey: false,
    ...overrides,
  }
}

function ColorSetting({ label, value, onChange }: { label: string; value: string; onChange: (value: string) => void }) {
  return <label className="color-setting"><span>{label}</span><input type="color" value={value} onChange={event => onChange(event.target.value)}/><code>{value}</code></label>
}

function SettingsPanel({ open, settings, usage, onClose, onSave, onOpenSetup }: { open: boolean; settings: AppSettings | null; usage: AIUsage | null; onClose: () => void; onSave: (update: SettingsUpdate) => Promise<boolean>; onOpenSetup: () => void }) {
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
    const saved = await onSave(settingsUpdate(draft, { apiKey, clearApiKey: clearAPIKey }))
    setSaving(false)
    if (saved) onClose()
  }

  return <div className="modal-backdrop" role="presentation" onMouseDown={event => { if (event.currentTarget === event.target) onClose() }}>
    <section className="settings-panel" role="dialog" aria-modal="true" aria-labelledby="settings-title">
      <header><div><h2 id="settings-title">Settings</h2><span>Appearance, ModMaker, AI, and storage</span></div><button className="icon-button" onClick={onClose} aria-label="Close settings"><Icon name="close"/></button></header>
      <div className="settings-body">
        <fieldset><legend>Appearance</legend>
          <div className="settings-row"><label>Theme<span>The switch also stays visible in the sidebar.</span></label><div className="segmented"><button className={draft.theme === 'dark' ? 'is-active' : ''} onClick={() => setDraft({ ...draft, theme: 'dark' })}>Black glass</button><button className={draft.theme === 'light' ? 'is-active' : ''} onClick={() => setDraft({ ...draft, theme: 'light' })}>Light</button></div></div>
          <div className="appearance-grid">
            <ColorSetting label="Emphasis" value={draft.emphasisColor} onChange={emphasisColor => setDraft({ ...draft, emphasisColor })}/>
            <ColorSetting label="Active tabs" value={draft.activeTabColor} onChange={activeTabColor => setDraft({ ...draft, activeTabColor })}/>
            <ColorSetting label="Dark surface" value={draft.darkSurfaceColor} onChange={darkSurfaceColor => setDraft({ ...draft, darkSurfaceColor })}/>
            <ColorSetting label="Dark borders" value={draft.darkBorderColor} onChange={darkBorderColor => setDraft({ ...draft, darkBorderColor })}/>
            <ColorSetting label="Dark text" value={draft.darkTextColor} onChange={darkTextColor => setDraft({ ...draft, darkTextColor })}/>
            <ColorSetting label="Light surface" value={draft.lightSurfaceColor} onChange={lightSurfaceColor => setDraft({ ...draft, lightSurfaceColor })}/>
            <ColorSetting label="Light borders" value={draft.lightBorderColor} onChange={lightBorderColor => setDraft({ ...draft, lightBorderColor })}/>
            <ColorSetting label="Light text" value={draft.lightTextColor} onChange={lightTextColor => setDraft({ ...draft, lightTextColor })}/>
          </div>
        </fieldset>
        <fieldset><legend>ModMaker</legend><label className="toggle-row"><input type="checkbox" checked={draft.showFileSizes} onChange={event => setDraft({ ...draft, showFileSizes: event.target.checked })}/><span><strong>Files navigator</strong> · Show file sizes in the file navigator</span></label></fieldset>
        <fieldset><legend>Project defaults</legend><label className="settings-field"><span>Author</span><input value={draft.defaultAuthor} onChange={event => setDraft({ ...draft, defaultAuthor: event.target.value })} placeholder="Used by new mods" maxLength={80}/><small>The first author entered in the manual wizard becomes this default.</small></label></fieldset>
        <fieldset><legend>Storage and paths</legend><div className="settings-row"><label>BeamNG and staging locations<span>Change the game, mod library, or BeamWorlds storage folders.</span></label><Button onClick={onOpenSetup}>Open setup</Button></div></fieldset>
        <fieldset><legend>AI · Mod Audit</legend><p className="settings-explainer">The pre-scan model describes candidate files without a verdict. The full model reviews that evidence, visually inspects bounded raster attachments, and may request focused files.</p><div className="settings-grid">
          <label className="settings-field"><span>Pre-scan analysis model</span><input value={draft.preScanModel} onChange={event => setDraft({ ...draft, preScanModel: event.target.value })}/><small>Small model recommended: gpt-5.6-luna</small></label>
          <label className="settings-field"><span>Pre-scan reasoning</span><select value={draft.preScanReasoning} onChange={event => setDraft({ ...draft, preScanReasoning: event.target.value })}><option value="low">Low</option><option value="medium">Medium · recommended</option><option value="high">High</option><option value="xhigh">Extra high</option></select></label>
          <label className="settings-field"><span>Full vulnerability analysis model</span><input value={draft.fullScanModel} onChange={event => setDraft({ ...draft, fullScanModel: event.target.value })}/><small>Large multimodal model recommended: gpt-5.6-sol</small></label>
          <label className="settings-field"><span>Full analysis reasoning</span><select value={draft.fullScanReasoning} onChange={event => setDraft({ ...draft, fullScanReasoning: event.target.value })}><option value="low">Low</option><option value="medium">Medium</option><option value="high">High</option><option value="xhigh">Extra high · recommended</option></select></label>
        </div></fieldset>
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
