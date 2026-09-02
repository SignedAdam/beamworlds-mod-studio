import { useCallback, useEffect, useRef, useState } from 'react'
import { Events } from '@wailsio/runtime'
import { AppService as API } from '../bindings/github.com/SignedAdam/beamng-mod-studio/index.js'
import type { AIUsage, AppConfig, AppSettings, ArchiveMemberPreview, Dashboard, EntityDetail, LibraryItem, LibraryItemDetailsUpdate, LibraryVariantUpdate, ModTag, NewModRequest, OrganizationState, ProfileProgress, ScanProgress, SettingsUpdate, SetupState, WorkspaceDetail, WorkspaceRecord } from '../bindings/github.com/SignedAdam/beamng-mod-studio/models.js'
import { ActivityView } from './ActivityView'
import { Inspector } from './Inspector'
import { LibraryView } from './LibraryView'
import { ModMaker } from './ModMaker'
import { ProfilesView } from './ProfilesView'
import { VirusScannerView } from './VirusScannerView'
import { SettingsView, settingsUpdate } from './SettingsView'
import { SetupWizard } from './SetupWizard'
import { BeamWorldsMark, Icon } from './icons'
import { formatBytes } from './ui'

type View = 'library' | 'workspaces' | 'scanner' | 'profiles' | 'activity' | 'settings'
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
  const [virusScanRequest, setVirusScanRequest] = useState<{ entityIDs: string[]; nonce: number } | null>(null)
  const [loading, setLoading] = useState(true)
  const [toast, setToast] = useState<ToastState>({ message: '', tone: 'info', visible: false })
  const toastTimer = useRef<number>()
  const autoScanStarted = useRef(false)
  const libraryLoadVersion = useRef(0)
  const virusScanRequestVersion = useRef(0)

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
      '--user-subsection-title': settings.subsectionTitleColor,
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

  const createWorkspace = async () => {
    if (!selectedItem) return
    setCreatingWorkspace(true)
    try {
      const detail = await API.CreateWorkspace(selectedItem.entityId)
      setWorkspaceDetail(detail)
      setSelectedWorkspaceID(detail.workspace.id)
      setWorkspaces(await API.ListWorkspaces() ?? [])
      setSelectedItem(null)
      setOpenMode('editor')
      setView('workspaces')
      notify(`${detail.entity.displayName} opened in ModMaker`, 'success')
    } catch (error) {
      handleError(error)
    } finally {
      setCreatingWorkspace(false)
    }
  }

  const openVirusScanner = (item: LibraryItem) => {
    setVirusScanRequest({ entityIDs: [item.entityId], nonce: ++virusScanRequestVersion.current })
    setSelectedItem(null)
    setEntityDetail(null)
    setView('scanner')
  }

  const refreshAfterVirusScan = async () => {
    await Promise.all([loadLibrary(), loadOrganization(), loadShell()])
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
  const createTag = async (name: string, color: string, icon: string): Promise<ModTag | null> => {
    const next = await API.CreateModTag(name, color, icon)
    setOrganization(next)
    notify(`Created tag ${name}`, 'success')
    return (next.tags ?? []).find(tag => tag.name.localeCompare(name, undefined, { sensitivity: 'base' }) === 0) ?? null
  }
  const updateSelectedTag = (tagID: string, replacement: ModTag | null) => {
    const updateItem = (item: LibraryItem) => ({
      ...item,
      tags: replacement
        ? (item.tags ?? []).map(tag => tag.id === tagID ? replacement : tag)
        : (item.tags ?? []).filter(tag => tag.id !== tagID),
    })
    setSelectedItem(current => current ? updateItem(current) : current)
    setEntityDetail(current => current ? { ...current, item: updateItem(current.item) } : current)
  }
  const updateTagVisual = async (tagID: string, color: string, icon: string): Promise<void> => {
    const next = await API.UpdateModTagVisual(tagID, color, icon)
    setOrganization(next)
    const updatedTag = (next.tags ?? []).find(tag => tag.id === tagID)
    const currentTag = selectedItem?.tags?.find(tag => tag.id === tagID)
    updateSelectedTag(tagID, updatedTag ?? (currentTag ? { ...currentTag, color, icon } : { id: tagID, name: '', modCount: 0, color, icon }))
    await Promise.all([loadLibrary(), loadOrganization()])
  }
  const renameTag = async (tagID: string, name: string) => {
    const next = await API.RenameModTag(tagID, name)
    setOrganization(next)
    const updatedTag = (next.tags ?? []).find(tag => tag.id === tagID)
    const currentTag = selectedItem?.tags?.find(tag => tag.id === tagID)
    updateSelectedTag(tagID, updatedTag ?? (currentTag ? { ...currentTag, name } : { id: tagID, name, modCount: 0, color: '', icon: '' }))
    await Promise.all([loadLibrary(), loadOrganization()])
    notify('Tag renamed', 'success')
  }
  const deleteTag = async (tagID: string) => {
    const next = await API.DeleteModTag(tagID)
    setOrganization(next)
    updateSelectedTag(tagID, null)
    await Promise.all([loadLibrary(), loadOrganization()])
    notify('Tag deleted', 'success')
  }
  const setSelectedTags = async (tagIDs: string[]) => {
    if (!selectedItem) return
    const next = await API.SetLibraryItemTags(selectedItem.entityId, tagIDs)
    const nextDetail = await API.GetEntity(next.entityId)
    setSelectedItem(nextDetail.item)
    setEntityDetail(nextDetail)
    await Promise.all([loadLibrary(), loadOrganization()])
  }
  const saveDetails = async (update: LibraryItemDetailsUpdate): Promise<void> => {
    if (!selectedItem) return
    const next = await API.UpdateLibraryItemDetails(selectedItem.entityId, update)
    setSelectedItem(next.item)
    setEntityDetail(next)
    await Promise.all([loadLibrary(), loadOrganization()])
  }
  const saveVariant = async (update: LibraryVariantUpdate): Promise<void> => {
    if (!selectedItem) return
    const next = await API.UpdateLibraryVariant(selectedItem.entityId, update)
    setSelectedItem(next.item)
    setEntityDetail(next)
    await Promise.all([loadLibrary(), loadOrganization()])
  }
  const previewMember = async (path: string): Promise<ArchiveMemberPreview | null> => {
    if (!selectedItem) return null
    try {
      return await API.PreviewLibraryArchiveMember(selectedItem.entityId, path)
    } catch (error) {
      handleError(error)
      return null
    }
  }
  const extractMember = async (path: string): Promise<void> => {
    if (!selectedItem) return
    const savedPath = await API.ExtractLibraryArchiveMember(selectedItem.entityId, path)
    if (savedPath) notify(`Extracted ${path} to ${savedPath}`, 'success')
  }
  const revealArchive = async (): Promise<void> => {
    if (!selectedItem) return
    await API.RevealLibraryArchive(selectedItem.entityId)
    notify('Archive revealed', 'success')
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
      setSetupOpen(true)
    } catch (error) {
      handleError(error)
    }
  }

  const changeView = (next: View) => {
    setView(next)
    setSelectedItem(null)
    setEntityDetail(null)
    if (next === 'scanner') setVirusScanRequest(null)
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
        <button className={view === 'library' ? 'is-active' : ''} onClick={() => changeView('library')} aria-label="Mod Library"><Icon name="library"/><span className="nav-label">Mod Library</span><span className="nav-tooltip">Mod Library</span></button>
        <button className={view === 'workspaces' ? 'is-active' : ''} onClick={() => changeView('workspaces')} aria-label="ModMaker"><Icon name="workspace"/><span className="nav-label">ModMaker</span><span className="nav-tooltip">ModMaker</span></button>
        <button className={view === 'scanner' ? 'is-active' : ''} onClick={() => changeView('scanner')} aria-label="Virus Scanner"><Icon name="shield"/><span className="nav-label">Virus Scanner</span><span className="nav-tooltip">Virus Scanner</span></button>
        <button className={view === 'profiles' ? 'is-active' : ''} onClick={() => changeView('profiles')} aria-label="Mod Profiles"><Icon name="play"/><span className="nav-label">Mod Profiles</span><span className="nav-tooltip">Mod Profiles</span></button>
        <button className={view === 'activity' ? 'is-active' : ''} onClick={() => changeView('activity')} aria-label="Activity"><Icon name="activity"/><span className="nav-label">Activity</span><span className="nav-tooltip">Activity</span></button>
      </nav>
      <div className="sidebar-actions">
        <button className="sidebar-theme" onClick={toggleTheme} aria-label={`Use ${settings?.theme === 'dark' ? 'light' : 'dark'} theme`}><Icon name={settings?.theme === 'dark' ? 'sun' : 'moon'} size={17}/><span>{settings?.theme === 'dark' ? 'Light theme' : 'Dark theme'}</span><span className="nav-tooltip">{settings?.theme === 'dark' ? 'Light theme' : 'Dark theme'}</span></button>
        <button className={`sidebar-settings ${view === 'settings' ? 'is-active' : ''}`} onClick={() => changeView('settings')} aria-label="Settings"><Icon name="settings" size={17}/><span>Settings</span><span className="nav-tooltip">Settings</span></button>
      </div>
    </aside>

    <main className={`app-main ${selectedItem && view === 'library' ? 'has-inspector' : ''}`}>
      {loading ? <div className="splash"><BeamWorldsMark size={64}/><div className="splash__line"/><p>{startupCount > 0 ? `Loading ${startupCount.toLocaleString()} mods` : 'Loading mod library'}</p><span>This may take a few seconds.</span></div> : <>
        {view === 'library' && <LibraryView items={items} catalogItems={allItems} folders={organization?.folders ?? []} tags={organization?.tags ?? []} scan={scan} scanning={scanning} loading={libraryLoading} query={query} folderID={folderID} selectedID={selectedItem?.entityId ?? ''} onQueryChange={setQuery} onFolderChange={setFolderID} onCreateFolder={name => void createFolder(name)} onRenameFolder={(id, name) => void renameFolder(id, name)} onDeleteFolder={id => void deleteFolder(id)} onSelect={item => void selectItem(item)} onVirusScan={openVirusScanner} onScan={() => void startScan()} onCancelScan={() => void cancelScan()}/>}
        {view === 'workspaces' && <ModMaker workspaces={workspaces} detail={workspaceDetail} selectedID={selectedWorkspaceID} loading={workspaceLoading} defaultAuthor={settings?.defaultAuthor ?? ''} showFileSizes={settings?.showFileSizes ?? true} openMode={openMode} onOpenModeHandled={() => setOpenMode(null)} onSelect={setSelectedWorkspaceID} onReload={reloadWorkspace} onCreateMod={createNewMod} onAgentStarted={() => { void API.AIUsage().then(setUsage).catch(handleError) }} onEditorStatus={setEditorStatus} onNotify={notify} onError={handleError}/>}
        {view === 'scanner' && <VirusScannerView items={allItems} request={virusScanRequest} onLibraryChange={refreshAfterVirusScan} onNotify={notify} onError={handleError}/>}
        {view === 'profiles' && <ProfilesView organization={organization} items={allItems} progress={profileProgress} onOrganization={setOrganization} onNotify={notify} onError={handleError}/>}
        {view === 'activity' && <ActivityView config={config} dashboard={dashboard} onScan={() => void startScan()}/>}
        {view === 'settings' && <SettingsView settings={settings} usage={usage} onSave={saveSettings} onOpenSetup={() => void openSetup()}/>}
      </>}
    </main>

    {selectedItem && view === 'library' && <Inspector item={selectedItem} detail={entityDetail} folders={organization?.folders ?? []} tags={organization?.tags ?? []} loading={entityLoading} creatingWorkspace={creatingWorkspace} onClose={() => { setSelectedItem(null); setEntityDetail(null) }} onMoveFolder={folder => void moveSelectedItem(folder)} onSetTags={setSelectedTags} onCreateTag={createTag} onUpdateTagVisual={updateTagVisual} onRenameTag={renameTag} onDeleteTag={deleteTag} onSaveDetails={saveDetails} onSaveVariant={saveVariant} onPreviewMember={previewMember} onExtractMember={extractMember} onRevealArchive={revealArchive} onCreateWorkspace={() => void createWorkspace()} onVirusScan={openVirusScanner} onError={handleError}/>}

    <footer className="app-statusbar" aria-label="Application status">
      <div><span className="status-led status-led--ready"/>{scanning ? `Scanning ${scan?.analyzed ?? 0}/${scan?.discovered ?? 0}` : 'Ready'}</div>
      <div>{view === 'workspaces' && activeWorkspace ? <><strong>{activeWorkspace.displayName}</strong>{editorStatus.path && <span title={editorStatus.path}>{editorStatus.dirty ? 'Unsaved' : 'Saved'} · {editorStatus.path} · {formatBytes(editorStatus.sizeBytes)}</span>}</> : view === 'settings' ? <span>Application settings</span> : <span>{(dashboard?.entities ?? items.length).toLocaleString()} mods</span>}</div>
      {settings?.showAIUsage && usage?.hasRuns && <div className="usage-compact"><Icon name="agent" size={13}/>{usageLimits.map(limit => <UsageChip key={`${limit.provider}-${limit.label}`} limit={limit}/>)}{usage.totalTokens > 0 && <span>{usage.totalTokens.toLocaleString()} tokens</span>}</div>}
    </footer>

    <div className={`toast toast--${toast.tone} ${toast.visible ? 'is-visible' : ''}`} role="status" aria-live="polite"><Icon name={toast.tone === 'error' ? 'error' : toast.tone === 'success' ? 'check' : 'activity'} size={17}/><span>{toast.message}</span><button onClick={() => setToast(current => ({ ...current, visible: false }))} aria-label="Dismiss"><Icon name="close" size={14}/></button></div>
  </div>
}

function UsageChip({ limit }: { limit: NonNullable<AIUsage['limits']>[number] }) {
  const amount = limit.unit === 'percent' ? `${Math.round(limit.used)}%` : `${Math.round(limit.remaining)} ${limit.unit}`
  return <span title={`${limit.provider} · resets ${new Date(limit.resetsAt).toLocaleString()}`}>{limit.windowId || limit.label} {amount}</span>
}


export default App
