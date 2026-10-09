import { useEffect, useLayoutEffect, useRef, useState, type FormEvent } from 'react'
import { Browser, Events } from '@wailsio/runtime'
import { AppService as API } from '../bindings/github.com/SignedAdam/beamng-mod-studio/index.js'
import type { AIConnectionEvent, AIConnectionStart, AIConnectionState, AIProviderConnection, AIUsage, AppSettings, SettingsUpdate } from '../bindings/github.com/SignedAdam/beamng-mod-studio/models.js'
import { DeploymentModeSelector } from './ArchiveDeployment'
import { useArchiveCapability } from './useArchiveCapability'
import type { DeploymentMode } from './archiveTypes'
import { StorageReview } from './StorageReview'
import { Icon } from './icons'
import { Badge, Button, Page, Spinner } from './ui'

type SubscriptionProviderID = 'chatgpt' | 'claude'
type KeyProviderID = 'openrouter' | 'openai' | 'anthropic'
type ProviderID = SubscriptionProviderID | KeyProviderID

interface SubscriptionProvider { id: SubscriptionProviderID; kind: 'subscription'; label: string; detail: string }
interface KeyProvider {
  id: KeyProviderID
  kind: 'apiKey'
  label: string
  detail: string
  stored: 'hasOpenRouterApiKey' | 'hasOpenAIApiKey' | 'hasAnthropicApiKey'
  field: 'openRouterApiKey' | 'openAIApiKey' | 'anthropicApiKey'
  clear: 'clearOpenRouterApiKey' | 'clearOpenAIAPIKey' | 'clearAnthropicAPIKey'
}
type Provider = SubscriptionProvider | KeyProvider
type InterfaceSize = 'compact' | 'default' | 'comfortable' | 'large'
type TextSize = 'small' | 'default' | 'large' | 'extra-large'

const INTERFACE_SIZE_OPTIONS: readonly { value: InterfaceSize; label: string; percentage: string }[] = [
  { value: 'compact', label: 'Compact', percentage: '90%' },
  { value: 'default', label: 'Default', percentage: '100%' },
  { value: 'comfortable', label: 'Large', percentage: '110%' },
  { value: 'large', label: 'Extra large', percentage: '125%' },
]

const TEXT_SIZE_OPTIONS: readonly { value: TextSize; label: string; percentage: string }[] = [
  { value: 'small', label: 'Small', percentage: '90%' },
  { value: 'default', label: 'Default', percentage: '100%' },
  { value: 'large', label: 'Large', percentage: '115%' },
  { value: 'extra-large', label: 'Extra large', percentage: '130%' },
]

const INTERFACE_SIZE_LABELS: Record<InterfaceSize, string> = {
  compact: 'Compact',
  default: 'Default',
  comfortable: 'Large',
  large: 'Extra large',
}
const TEXT_SIZE_LABELS: Record<TextSize, string> = {
  small: 'Small',
  default: 'Default',
  large: 'Large',
  'extra-large': 'Extra large',
}

function normalizeInterfaceSize(value: unknown): InterfaceSize {
  return INTERFACE_SIZE_OPTIONS.some(option => option.value === value) ? value as InterfaceSize : 'default'
}

function normalizeTextSize(value: unknown): TextSize {
  return TEXT_SIZE_OPTIONS.some(option => option.value === value) ? value as TextSize : 'default'
}

function applySizingAttributes(interfaceSize: unknown, textSize: unknown) {
  const root = document.documentElement
  root.dataset.interfaceSize = normalizeInterfaceSize(interfaceSize)
  root.dataset.textSize = normalizeTextSize(textSize)
}

const PROVIDERS: readonly Provider[] = [
  { id: 'chatgpt', kind: 'subscription', label: 'ChatGPT', detail: 'Subscription' },
  { id: 'claude', kind: 'subscription', label: 'Claude', detail: 'Subscription' },
  { id: 'openrouter', kind: 'apiKey', label: 'OpenRouter', detail: 'API key', stored: 'hasOpenRouterApiKey', field: 'openRouterApiKey', clear: 'clearOpenRouterApiKey' },
  { id: 'openai', kind: 'apiKey', label: 'OpenAI', detail: 'API key', stored: 'hasOpenAIApiKey', field: 'openAIApiKey', clear: 'clearOpenAIAPIKey' },
  { id: 'anthropic', kind: 'apiKey', label: 'Anthropic', detail: 'API key', stored: 'hasAnthropicApiKey', field: 'anthropicApiKey', clear: 'clearAnthropicAPIKey' },
]

const providerByID = (id: string) => PROVIDERS.find(provider => provider.id === id)

interface KeyDraft { value: string; clear: boolean }
type KeyDrafts = Record<KeyProviderID, KeyDraft>
const emptyKeyDrafts = (): KeyDrafts => ({ openrouter: { value: '', clear: false }, openai: { value: '', clear: false }, anthropic: { value: '', clear: false } })

type LoginStatus = 'starting' | 'pending' | 'input' | 'success' | 'error' | 'cancelled'
const EVENT_STATUSES: Record<string, LoginStatus> = { pending: 'pending', waiting: 'pending', input: 'input', success: 'success', connected: 'success', error: 'error', cancelled: 'cancelled' }
const isTerminal = (status: LoginStatus) => status === 'success' || status === 'error' || status === 'cancelled'
const VERIFYING_AUTHORIZATION_MESSAGE = 'Checking code…'

const rememberEarlyEvent = (events: Map<string, AIConnectionEvent>, event: AIConnectionEvent) => {
  const previous = events.get(event.loginId)
  if (previous) {
    const previousStatus = EVENT_STATUSES[previous.status] ?? 'pending'
    const nextStatus = EVENT_STATUSES[event.status] ?? 'pending'
    if (isTerminal(previousStatus) || (previousStatus === 'input' && nextStatus === 'pending')) return
  }
  events.set(event.loginId, event)
}

interface LoginState {
  providerId: ProviderID
  loginId: string
  url: string
  status: LoginStatus
  message: string
  requestId: string
  inputLabel: string
}

interface SettingsViewProps {
  settings: AppSettings | null
  usage: AIUsage | null
  onSave: (update: SettingsUpdate) => Promise<boolean>
  onOpenSetup: () => void
  onNotify: (message: string, tone?: 'success' | 'error' | 'info') => void
  onRefreshLibrary: () => void
}

export function SettingsView({ settings, usage, onSave, onOpenSetup, onNotify, onRefreshLibrary }: SettingsViewProps) {
  const [draft, setDraft] = useState<AppSettings | null>(settings)
  const [keyDrafts, setKeyDrafts] = useState<KeyDrafts>(emptyKeyDrafts)
  const [saving, setSaving] = useState(false)
  const [connections, setConnections] = useState<AIConnectionState | null>(null)
  const [connectionsFailed, setConnectionsFailed] = useState(false)
  const [login, setLogin] = useState<LoginState | null>(null)
  const [sizingAnnouncement, setSizingAnnouncement] = useState('')
  const savedTheme = useRef(settings?.theme ?? 'dark')
  const savedInterfaceSize = useRef<InterfaceSize>(normalizeInterfaceSize(settings?.interfaceSize))
  const savedTextSize = useRef<TextSize>(normalizeTextSize(settings?.textSize))
  const loginRef = useRef<LoginState | null>(null)
  const loginAttempt = useRef(0)
  const lastEvents = useRef(new Map<string, AIConnectionEvent>())
  const loginTrigger = useRef<HTMLElement | null>(null)

  useEffect(() => {
    savedTheme.current = settings?.theme ?? 'dark'
    savedInterfaceSize.current = normalizeInterfaceSize(settings?.interfaceSize)
    savedTextSize.current = normalizeTextSize(settings?.textSize)
    setDraft(settings)
  }, [settings])
  useLayoutEffect(() => {
    if (draft) applySizingAttributes(draft.interfaceSize, draft.textSize)
  }, [draft?.interfaceSize, draft?.textSize])
  useEffect(() => () => {
    document.documentElement.dataset.theme = savedTheme.current
    applySizingAttributes(savedInterfaceSize.current, savedTextSize.current)
  }, [])

  const updateLogin = (next: LoginState | null) => {
    loginRef.current = next
    setLogin(next)
  }

  const loadConnections = () => API.AIConnections()
    .then(state => { setConnections(state); setConnectionsFailed(false) })
    .catch(() => {
      setConnectionsFailed(true)
      onNotify('Provider status unavailable', 'error')
    })

  useEffect(() => { void loadConnections() }, [])

  const applyEvent = (data: AIConnectionEvent) => {
    const current = loginRef.current
    if (!current || !current.loginId || current.loginId !== data.loginId) return
    if (isTerminal(current.status)) return
    const status = EVENT_STATUSES[data.status] ?? 'pending'
    const verifying = status === 'pending' && (current.status === 'input' || current.message === VERIFYING_AUTHORIZATION_MESSAGE)
    updateLogin({ ...current, status, message: verifying ? VERIFYING_AUTHORIZATION_MESSAGE : '', requestId: data.requestId || '', inputLabel: status === 'input' ? 'Code' : '' })
    if (isTerminal(status)) lastEvents.current.delete(data.loginId)
    if (status === 'success') {
      setConnections(previous => previous ? {
        ...previous,
        providers: (previous.providers ?? []).map(provider => provider.id === current.providerId ? { ...provider, connected: true } : provider),
      } : previous)
      setConnectionsFailed(false)
      onNotify(`${providerByID(current.providerId)?.label ?? 'Provider'} connected`, 'success')
      void loadConnections()
    }
  }

  useEffect(() => Events.On('ai:connection', event => {
    const data = event.data as AIConnectionEvent | undefined
    const current = loginRef.current
    if (!data?.loginId || !current) return
    if (current.loginId) {
      if (current.loginId === data.loginId) applyEvent(data)
      return
    }
    rememberEarlyEvent(lastEvents.current, data)
  }), [])
  useEffect(() => () => {
    loginAttempt.current++
    const current = loginRef.current
    if (current?.loginId && !isTerminal(current.status)) API.CancelAIConnection(current.loginId).catch(() => undefined)
  }, [])
  useEffect(() => {
    if (login || !loginTrigger.current) return
    if (loginTrigger.current.isConnected) loginTrigger.current.focus()
    loginTrigger.current = null
  }, [login])


  const connect = async (provider: Provider) => {
    const activeLogin = loginRef.current
    if (activeLogin && !isTerminal(activeLogin.status)) return
    lastEvents.current.clear()
    const attempt = ++loginAttempt.current
    if (!activeLogin) loginTrigger.current = document.activeElement instanceof HTMLElement ? document.activeElement : null
    updateLogin({ providerId: provider.id, loginId: '', url: '', status: 'starting', message: '', requestId: '', inputLabel: '' })
    let start: AIConnectionStart
    try {
      start = await API.StartAIConnection(provider.id)
    } catch {
      const current = loginRef.current
      if (current && attempt === loginAttempt.current) updateLogin({ ...current, status: 'error', message: 'Sign-in failed' })
      return
    }
    if (attempt !== loginAttempt.current) {
      lastEvents.current.delete(start.loginId)
      API.CancelAIConnection(start.loginId).catch(() => undefined)
      return
    }
    updateLogin({ providerId: provider.id, loginId: start.loginId, url: start.url || '', status: 'pending', message: '', requestId: '', inputLabel: '' })
    const buffered = lastEvents.current.get(start.loginId)
    lastEvents.current.clear()
    if (buffered) applyEvent(buffered)
    const current = loginRef.current
    if (start.url && current?.loginId === start.loginId && !isTerminal(current.status)) {
      void Browser.OpenURL(start.url).catch(() => onNotify('Sign-in page failed to open', 'error'))
    }
  }

  const cancelLogin = () => {
    const current = loginRef.current
    if (!current) return
    if (current.loginId) lastEvents.current.delete(current.loginId)
    loginAttempt.current++
    updateLogin(null)
    if (current.loginId && !isTerminal(current.status)) {
      API.CancelAIConnection(current.loginId).catch(() => onNotify('Sign-in cancel failed', 'error'))
    }
  }

  const closeLogin = () => {
    loginAttempt.current++
    updateLogin(null)
  }

  const submitCode = async (value: string) => {
    const current = loginRef.current
    if (!current?.loginId) return
    await API.SubmitAIConnection(current.loginId, current.requestId, value)
    const latest = loginRef.current
    if (latest && latest.loginId === current.loginId && latest.status === 'input' && latest.requestId === current.requestId) {
      updateLogin({ ...latest, status: 'pending', message: VERIFYING_AUTHORIZATION_MESSAGE, requestId: '', inputLabel: '' })
    }
  }

  const previewTheme = (theme: 'dark' | 'light') => {
    document.documentElement.dataset.theme = theme
    setDraft(current => current ? { ...current, theme } : current)
  }

  const updateSizing = (field: 'interfaceSize' | 'textSize', value: InterfaceSize | TextSize) => {
    if (!draft) return
    const next = { ...draft, [field]: value } as AppSettings
    setDraft(next)
    applySizingAttributes(next.interfaceSize, next.textSize)
    const label = field === 'interfaceSize'
      ? INTERFACE_SIZE_LABELS[value as InterfaceSize]
      : TEXT_SIZE_LABELS[value as TextSize]
    setSizingAnnouncement(`${field === 'interfaceSize' ? 'Interface size' : 'Text size'}: ${label}`)
  }

  const resetSizing = () => {
    if (!draft) return
    const next = { ...draft, interfaceSize: 'default', textSize: 'default' } as AppSettings
    setDraft(next)
    applySizingAttributes(next.interfaceSize, next.textSize)
    setSizingAnnouncement('Sizes reset')
  }

  const reset = () => {
    document.documentElement.dataset.theme = settings?.theme ?? 'dark'
    applySizingAttributes(savedInterfaceSize.current, savedTextSize.current)
    setDraft(settings)
    setKeyDrafts(emptyKeyDrafts())
    setSizingAnnouncement('')
  }

  const activeProvider = draft && (providerByID(draft.agentProfile)?.id ?? providerByID(connections?.activeProfile ?? '')?.id ?? '')

  const submit = async () => {
    if (!draft) return
    setSaving(true)
    const overrides: Partial<SettingsUpdate> = { agentProfile: activeProvider || draft.agentProfile }
    for (const provider of PROVIDERS) {
      if (provider.kind !== 'apiKey') continue
      overrides[provider.field] = keyDrafts[provider.id].value.trim()
      overrides[provider.clear] = keyDrafts[provider.id].clear
    }
    const saved = await onSave(settingsUpdate(draft, overrides))
    setSaving(false)
    if (saved) setKeyDrafts(emptyKeyDrafts())
  }

  const setKeyDraft = (id: KeyProviderID, next: KeyDraft) => setKeyDrafts(current => ({ ...current, [id]: next }))

  const selected = activeProvider ? providerByID(activeProvider) : undefined
  let selectedReady: boolean | null = null
  if (selected?.kind === 'subscription') {
    const connection = connectionsFailed ? undefined : connections?.providers?.find(provider => provider.id === selected.id)
    if (connection) selectedReady = connection.connected
  } else if (selected?.kind === 'apiKey') {
    selectedReady = !!keyDrafts[selected.id].value.trim() || (!!draft?.[selected.stored] && !keyDrafts[selected.id].clear)
  }
  const loginProvider = login ? providerByID(login.providerId) : undefined
  const connectionBusy = !!login && !isTerminal(login.status)

  return <Page title="Settings" className="settings-view" ariaLabel="Settings" actions={[
    { key: 'reset', label: 'Discard changes', role: 'secondary', disabled: !draft || saving, onClick: reset },
    { key: 'apply', label: saving ? 'Saving…' : 'Save', role: 'primary', disabled: !draft || saving, onClick: () => void submit() },
  ]}>

    {!draft ? <div className="settings-page-loading">Loading settings…</div> : <div className="settings-body"><div className="settings-form">
      <fieldset className="settings-section--wide"><legend>Appearance</legend>
        <div className="settings-row"><label>Theme</label><div className="segmented"><button className={draft.theme === 'dark' ? 'is-active' : ''} onClick={() => previewTheme('dark')}>Dark</button><button className={draft.theme === 'light' ? 'is-active' : ''} onClick={() => previewTheme('light')}>Light</button></div></div>
        <div className="sizing-controls">
          <fieldset className="sizing-group">
            <legend id="interface-size-label">Interface size</legend>
            <div className="sizing-group__options" role="radiogroup" aria-labelledby="interface-size-label">
              {INTERFACE_SIZE_OPTIONS.map(option => <label className="sizing-option" key={option.value}>
                <input type="radio" name="interface-size" value={option.value} checked={draft.interfaceSize === option.value} onChange={() => updateSizing('interfaceSize', option.value)} aria-label={`${option.label} (${option.percentage})`}/>
                <span>{option.label}</span>
                <small>{option.percentage}</small>
              </label>)}
            </div>
          </fieldset>
          <fieldset className="sizing-group">
            <legend id="text-size-label">Text size</legend>
            <div className="sizing-group__options" role="radiogroup" aria-labelledby="text-size-label">
              {TEXT_SIZE_OPTIONS.map(option => <label className="sizing-option" key={option.value}>
                <input type="radio" name="text-size" value={option.value} checked={draft.textSize === option.value} onChange={() => updateSizing('textSize', option.value)} aria-label={`${option.label} (${option.percentage})`}/>
                <span>{option.label}</span>
                <small>{option.percentage}</small>
              </label>)}
            </div>
          </fieldset>
        </div>
        <div className="sizing-actions">
          <button type="button" className="text-button" onClick={resetSizing} disabled={saving}>Reset to default</button>
          <p className="sr-only" role="status" aria-live="polite">{sizingAnnouncement}</p>
        </div>
        <div className="appearance-colors" role="group" aria-labelledby="appearance-colors-title">
          <h3 id="appearance-colors-title" className="appearance-colors__title">Colors</h3>
          <div className="color-groups">
            <div className="color-group" role="group" aria-labelledby="colors-both-title">
              <span id="colors-both-title" className="color-group__title">Both themes</span>
              <ColorSetting label="Accent" value={draft.emphasisColor} onChange={emphasisColor => setDraft({ ...draft, emphasisColor })}/>
              <ColorSetting label="Selected tab" value={draft.activeTabColor} onChange={activeTabColor => setDraft({ ...draft, activeTabColor })}/>
              <ColorSetting label="Scrollbar" value={draft.scrollbarColor} onChange={scrollbarColor => setDraft({ ...draft, scrollbarColor })}/>
            </div>
            <div className="color-group" role="group" aria-labelledby="colors-dark-title">
              <span id="colors-dark-title" className="color-group__title">Dark theme</span>
              <ColorSetting label="Panels" value={draft.darkSurfaceColor} onChange={darkSurfaceColor => setDraft({ ...draft, darkSurfaceColor })}/>
              <ColorSetting label="Borders" value={draft.darkBorderColor} onChange={darkBorderColor => setDraft({ ...draft, darkBorderColor })}/>
              <ColorSetting label="Text" value={draft.darkTextColor} onChange={darkTextColor => setDraft({ ...draft, darkTextColor })}/>
            </div>
            <div className="color-group" role="group" aria-labelledby="colors-light-title">
              <span id="colors-light-title" className="color-group__title">Light theme</span>
              <ColorSetting label="Panels" value={draft.lightSurfaceColor} onChange={lightSurfaceColor => setDraft({ ...draft, lightSurfaceColor })}/>
              <ColorSetting label="Borders" value={draft.lightBorderColor} onChange={lightBorderColor => setDraft({ ...draft, lightBorderColor })}/>
              <ColorSetting label="Text" value={draft.lightTextColor} onChange={lightTextColor => setDraft({ ...draft, lightTextColor })}/>
            </div>
          </div>
        </div>
      </fieldset>

      <fieldset className="settings-section--wide"><legend>Virgil</legend>
        <fieldset className="provider-group"><legend>AI provider</legend>
          <div className="provider-list">
            {PROVIDERS.map(provider => {
              const active = provider.id === activeProvider
              const connecting = login?.providerId === provider.id && !isTerminal(login.status)
              return <div key={provider.id} className={`provider-row ${active ? 'is-active' : ''}`}>
                <label className="provider-row__choice">
                  <input type="radio" name="virgil-provider" value={provider.id} checked={active} onChange={() => setDraft({ ...draft, agentProfile: provider.id })}/>
                  <span><strong>{provider.label}</strong><small>{provider.detail}</small></span>
                </label>
                {provider.kind === 'subscription'
                  ? <SubscriptionControls provider={provider} connection={connections?.providers?.find(item => item.id === provider.id)} loading={!connections && !connectionsFailed} failed={connectionsFailed} connecting={connecting} disabled={connectionBusy} onConnect={() => void connect(provider)}/>
                  : <KeyControls provider={provider} stored={draft[provider.stored]} draft={keyDrafts[provider.id]} disabled={saving} onChange={next => setKeyDraft(provider.id, next)}/>}
              </div>
            })}
          </div>
          {!selected
            ? <p className="settings-hint" role="status"><Icon name="unknown" size={15}/><span>No provider selected</span></p>
            : selectedReady === false && <p className="settings-hint settings-hint--warning" role="status"><Icon name="warning" size={15}/><span>{selected.kind === 'subscription' ? `${selected.label} not connected` : `No ${selected.label} API key`}</span></p>}
        </fieldset>
        <div className="settings-grid">
          <label className="settings-field"><span>Model</span><input value={draft.agentModel} onChange={event => setDraft({ ...draft, agentModel: event.target.value })} placeholder="Default" maxLength={120}/></label>
          <label className="settings-field"><span>BeamNG guides</span><select value={draft.contextMode} onChange={event => setDraft({ ...draft, contextMode: event.target.value })}><option value="focused">Mod type only</option><option value="balanced">General + mod type</option><option value="deep">All mod types (more tokens)</option></select></label>
        </div>
        <div className="settings-subsection" role="group" aria-labelledby="virgil-usage-title">
          <h3 id="virgil-usage-title" className="settings-subsection__title">Usage</h3>
          <label className="toggle-row"><input type="checkbox" checked={draft.showAIUsage} onChange={event => setDraft({ ...draft, showAIUsage: event.target.checked })}/><span>Show AI usage in status bar</span></label>
          {usage?.hasRuns && <div className="usage-table"><div><span>Virgil requests</span><strong>{usage.runCount.toLocaleString()}</strong></div>{usage.totalTokens > 0 && <div><span>Tokens used</span><strong>{usage.totalTokens.toLocaleString()}</strong></div>}{(usage.limits ?? []).map(limit => <div key={`${limit.provider}-${limit.label}`}><span>{limit.provider} · {limit.label}</span><strong>{limit.unit === 'percent' ? `${Math.round(limit.used)}% used` : `${Math.round(limit.remaining)} ${limit.unit} left`}</strong></div>)}{usage.usageError && <p title={usage.usageError}>Usage unavailable</p>}</div>}
        </div>
      </fieldset>

      <fieldset className="settings-section--wide"><legend>Virus Scanner</legend><div className="settings-grid">
        <label className="settings-field"><span>File review model</span><input value={draft.preScanModel} onChange={event => setDraft({ ...draft, preScanModel: event.target.value })} placeholder="Default"/></label>
        <label className="settings-field"><span>File review effort</span><select value={draft.preScanReasoning} onChange={event => setDraft({ ...draft, preScanReasoning: event.target.value })}><option value="low">Low</option><option value="medium">Medium (recommended)</option><option value="high">High</option><option value="xhigh">Extra high</option></select></label>
        <label className="settings-field"><span>Final assessment model</span><input value={draft.fullScanModel} onChange={event => setDraft({ ...draft, fullScanModel: event.target.value })} placeholder="Default"/></label>
        <label className="settings-field"><span>Final assessment effort</span><select value={draft.fullScanReasoning} onChange={event => setDraft({ ...draft, fullScanReasoning: event.target.value })}><option value="low">Low</option><option value="medium">Medium</option><option value="high">High</option><option value="xhigh">Extra high (recommended)</option></select></label>
      </div></fieldset>

      <fieldset><legend>ModMaker</legend><div className="settings-stack">
        <label className="toggle-row"><input type="checkbox" checked={draft.showFileSizes} onChange={event => setDraft({ ...draft, showFileSizes: event.target.checked })}/><span>Show file sizes in file tree</span></label>
        <label className="settings-field"><span>Auto-format delay (ms)</span><input type="number" min={AUTO_FORMAT_DELAY_MIN_MS} max={AUTO_FORMAT_DELAY_MAX_MS} step={10} value={draft.autoFormatDelayMs} onChange={event => setDraft({ ...draft, autoFormatDelayMs: clampAutoFormatDelay(event.target.valueAsNumber) })}/></label>
        <label className="settings-field"><span>Default author</span><input value={draft.defaultAuthor} onChange={event => setDraft({ ...draft, defaultAuthor: event.target.value })} maxLength={80}/></label>
      </div></fieldset>

      <fieldset><legend>Folders</legend><div className="settings-stack">
        <div className="settings-row"><label>Game and mod folders</label><Button onClick={onOpenSetup}>Change folders…</Button></div>
        <SettingsDeploymentMode onNotify={onNotify}/>
        <SettingsStorageReview onNotify={onNotify} onRefreshLibrary={onRefreshLibrary}/>
      </div></fieldset>
    </div></div>}

    {login && loginProvider && <ConnectionDialog login={login} providerLabel={loginProvider.label} onSubmit={submitCode} onCancel={cancelLogin} onClose={closeLogin} onRetry={() => void connect(loginProvider)} onOpenURL={url => { void Browser.OpenURL(url).catch(() => onNotify('Sign-in page failed to open', 'error')) }}/>}
</Page>
}

function SubscriptionControls({ provider, connection, loading, failed, connecting, disabled, onConnect }: {
  provider: SubscriptionProvider
  connection: AIProviderConnection | undefined
  loading: boolean
  failed: boolean
  connecting: boolean
  disabled: boolean
  onConnect: () => void
}) {
  const connected = !!connection?.connected
  const state = connecting ? { tone: 'cyan' as const, label: 'Connecting' }
    : loading ? { tone: 'neutral' as const, label: 'Checking' }
    : failed ? { tone: 'warning' as const, label: 'Unknown' }
    : connected ? { tone: 'success' as const, label: 'Connected' }
    : { tone: 'neutral' as const, label: 'Not connected' }
  return <>
    <div className="provider-row__state"><Badge tone={state.tone}>{state.label}</Badge></div>
    <div className="provider-row__control"><Button type="button" icon={connected ? 'refresh' : 'link'} disabled={loading || disabled} onClick={onConnect} aria-label={`${connected ? 'Reconnect' : 'Connect'} ${provider.label}`}>{connected ? 'Reconnect' : 'Connect'}</Button></div>
  </>
}

function KeyControls({ provider, stored, draft, disabled, onChange }: {
  provider: KeyProvider
  stored: boolean
  draft: KeyDraft
  disabled: boolean
  onChange: (next: KeyDraft) => void
}) {
  const pending = draft.value.trim().length > 0
  const state = pending ? { tone: 'cyan' as const, label: 'Unsaved key' }
    : draft.clear ? { tone: 'warning' as const, label: 'Removed on save' }
    : stored ? { tone: 'success' as const, label: 'Key saved' }
    : { tone: 'neutral' as const, label: 'No key' }
  return <>
    <div className="provider-row__state"><Badge tone={state.tone}>{state.label}</Badge></div>
    <div className="provider-row__control">
      <input type="password" value={draft.value} onChange={event => onChange({ value: event.target.value, clear: false })} placeholder={stored && !draft.clear ? 'Replace key' : 'API key'} aria-label={`${provider.label} API key`} autoComplete="off" spellCheck={false} disabled={disabled}/>
      {stored && <button type="button" className={`text-button ${draft.clear ? 'is-active' : ''}`} disabled={disabled} onClick={() => onChange({ value: '', clear: !draft.clear })} aria-label={`${draft.clear ? 'Keep' : 'Remove'} saved ${provider.label} API key`}>{draft.clear ? 'Undo' : 'Remove'}</button>}
    </div>
  </>
}

function ConnectionDialog({ login, providerLabel, onSubmit, onCancel, onClose, onRetry, onOpenURL }: {
  login: LoginState
  providerLabel: string
  onSubmit: (value: string) => Promise<void>
  onCancel: () => void
  onClose: () => void
  onRetry: () => void
  onOpenURL: (url: string) => void
}) {
  const dialogRef = useRef<HTMLDialogElement>(null)
  const inputRef = useRef<HTMLInputElement>(null)
  const [code, setCode] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [submitError, setSubmitError] = useState('')
  const terminal = isTerminal(login.status)

  useEffect(() => {
    const dialog = dialogRef.current
    if (!dialog) return
    if (!dialog.open) dialog.showModal()
    dialog.focus()
    return () => {
      if (dialog.open) dialog.close()
    }
  }, [])
  useEffect(() => {
    setCode('')
    setSubmitError('')
  }, [login.loginId, login.requestId])
  useEffect(() => {
    if (submitError) inputRef.current?.focus()
  }, [submitError])
  useEffect(() => {
    const dialog = dialogRef.current
    if (!dialog) return
    const target = login.status === 'input' ? inputRef.current : terminal ? dialog.querySelector<HTMLElement>('.connection-dialog__actions .button--primary') : null
    if (target) target.focus()
    else if (!dialog.contains(document.activeElement)) dialog.focus()
  }, [login.status, terminal])

  const submit = async (event: FormEvent) => {
    event.preventDefault()
    const value = code.trim()
    if (!value || submitting) return
    setSubmitting(true)
    setSubmitError('')
    try {
      await onSubmit(value)
    } catch {
      setSubmitError('Code rejected')
    } finally {
      setSubmitting(false)
    }
  }

  const message = login.message || {
    starting: 'Opening sign-in…',
    pending: 'Waiting for browser…',
    input: 'Waiting for code',
    success: 'Connected',
    error: 'Sign-in failed',
    cancelled: 'Sign-in cancelled',
  }[login.status]

  return <dialog ref={dialogRef} className="connection-dialog" tabIndex={-1} aria-labelledby="connection-dialog-title" aria-describedby="connection-dialog-status" onCancel={event => { event.preventDefault(); if (terminal) onClose(); else onCancel() }}>
    <form onSubmit={event => void submit(event)}>
      <header><h2 id="connection-dialog-title">Connect {providerLabel}</h2></header>
      <div id="connection-dialog-status" className={`connection-dialog__status is-${login.status}`} role="status">
        {login.status === 'success' ? <Icon name="check" size={16}/> : login.status === 'error' ? <Icon name="error" size={16}/> : login.status === 'cancelled' ? <Icon name="warning" size={16}/> : login.status === 'input' ? <Icon name="edit" size={16}/> : <Spinner small/>}
        <span>{message}</span>
      </div>
      {login.status === 'input' && <label className="settings-field"><span>{login.inputLabel || 'Code'}</span><input ref={inputRef} value={code} onChange={event => setCode(event.target.value)} autoComplete="off" spellCheck={false} readOnly={submitting} aria-invalid={submitError ? true : undefined} aria-describedby={submitError ? 'connection-dialog-error' : undefined}/>{submitError && <small id="connection-dialog-error" className="connection-dialog__error" role="alert">{submitError}</small>}</label>}
      <footer className="connection-dialog__actions">
        {login.url && !terminal && <Button type="button" tone="quiet" icon="link" onClick={() => onOpenURL(login.url)}>Reopen sign-in page</Button>}
        {terminal
          ? <>{login.status !== 'success' && <Button type="button" icon="refresh" onClick={onRetry}>Try again</Button>}<Button type="button" tone="primary" onClick={onClose}>{login.status === 'success' ? 'Done' : 'Close'}</Button></>
          : <><Button type="button" onClick={onCancel}>Cancel</Button>{login.status === 'input' && <Button type="submit" tone="primary" disabled={!code.trim() || submitting}>{submitting ? 'Submitting…' : 'Submit'}</Button>}</>}
      </footer>
    </form>
  </dialog>
}


export function settingsUpdate(settings: AppSettings, overrides: Partial<SettingsUpdate> = {}): SettingsUpdate {
  return {
    theme: settings.theme,
    interfaceSize: settings.interfaceSize,
    textSize: settings.textSize,
    defaultAuthor: settings.defaultAuthor,
    agentProfile: settings.agentProfile,
    agentModel: settings.agentModel,
    contextMode: settings.contextMode,
    showAIUsage: settings.showAIUsage,
    showFileSizes: settings.showFileSizes,
    autoFormatDelayMs: settings.autoFormatDelayMs,
    emphasisColor: settings.emphasisColor,
    activeTabColor: settings.activeTabColor,
    darkSurfaceColor: settings.darkSurfaceColor,
    darkBorderColor: settings.darkBorderColor,
    darkTextColor: settings.darkTextColor,
    lightSurfaceColor: settings.lightSurfaceColor,
    lightBorderColor: settings.lightBorderColor,
    lightTextColor: settings.lightTextColor,
    scrollbarColor: settings.scrollbarColor,
    preScanModel: settings.preScanModel,
    preScanReasoning: settings.preScanReasoning,
    fullScanModel: settings.fullScanModel,
    fullScanReasoning: settings.fullScanReasoning,
    openRouterApiKey: '',
    clearOpenRouterApiKey: false,
    openAIApiKey: '',
    clearOpenAIAPIKey: false,
    anthropicApiKey: '',
    clearAnthropicAPIKey: false,
    ...overrides,
  }
}

const AUTO_FORMAT_DELAY_DEFAULT_MS = 200
const AUTO_FORMAT_DELAY_MIN_MS = 50
const AUTO_FORMAT_DELAY_MAX_MS = 2000

function clampAutoFormatDelay(value: number) {
  if (!Number.isFinite(value) || value <= 0) return AUTO_FORMAT_DELAY_DEFAULT_MS
  return Math.min(AUTO_FORMAT_DELAY_MAX_MS, Math.max(AUTO_FORMAT_DELAY_MIN_MS, Math.round(value)))
}

function ColorSetting({ label, value, onChange }: { label: string; value: string; onChange: (value: string) => void }) {
  return <label className="color-setting"><span>{label}</span><input type="color" value={value} onChange={event => onChange(event.target.value)}/><code>{value}</code></label>
}

function SettingsDeploymentMode({ onNotify }: { onNotify: (message: string, tone?: 'success' | 'error' | 'info') => void }) {
  const cap = useArchiveCapability('', '')

  const handleChange = async (mode: DeploymentMode) => {
    const result = await cap.setMode(mode)
    if (result) onNotify('Mod linking saved', 'success')
  }

  return <DeploymentModeSelector
    mode={cap.mode}
    capabilities={cap.capabilities}
    mixed={cap.mixed}
    warning={cap.warning}
    loading={cap.loading}
    error={cap.error}
    onChange={mode => void handleChange(mode)}
    onRefresh={cap.refresh}
  />
}

function SettingsStorageReview({ onNotify, onRefreshLibrary }: { onNotify: (message: string, tone?: 'success' | 'error' | 'info') => void; onRefreshLibrary: () => void }) {
  const [open, setOpen] = useState(false)

  return <>
    <div className="settings-row">
      <label>Disk space</label>
      <Button onClick={() => setOpen(true)}>Review storage</Button>
    </div>
    {open && <StorageReview onClose={() => setOpen(false)} onNotify={onNotify} onRefreshLibrary={onRefreshLibrary}/>}
  </>
}
