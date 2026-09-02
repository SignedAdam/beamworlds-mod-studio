import { useEffect, useRef, useState } from 'react'
import type { AIUsage, AppSettings, SettingsUpdate } from '../bindings/github.com/SignedAdam/beamng-mod-studio/models.js'
import { Button } from './ui'

interface SettingsViewProps {
  settings: AppSettings | null
  usage: AIUsage | null
  onSave: (update: SettingsUpdate) => Promise<boolean>
  onOpenSetup: () => void
}

export function SettingsView({ settings, usage, onSave, onOpenSetup }: SettingsViewProps) {
  const [draft, setDraft] = useState<AppSettings | null>(settings)
  const [apiKey, setAPIKey] = useState('')
  const [clearAPIKey, setClearAPIKey] = useState(false)
  const [saving, setSaving] = useState(false)
  const savedTheme = useRef(settings?.theme ?? 'dark')

  useEffect(() => {
    savedTheme.current = settings?.theme ?? 'dark'
    setDraft(settings)
    setAPIKey('')
    setClearAPIKey(false)
  }, [settings])
  useEffect(() => () => {
    document.documentElement.dataset.theme = savedTheme.current
  }, [])

  const previewTheme = (theme: 'dark' | 'light') => {
    document.documentElement.dataset.theme = theme
    setDraft(current => current ? { ...current, theme } : current)
  }

  const reset = () => {
    document.documentElement.dataset.theme = settings?.theme ?? 'dark'
    setDraft(settings)
    setAPIKey('')
    setClearAPIKey(false)
  }

  const submit = async () => {
    if (!draft) return
    setSaving(true)
    const saved = await onSave(settingsUpdate(draft, { apiKey, clearApiKey: clearAPIKey }))
    setSaving(false)
    if (saved) {
      setAPIKey('')
      setClearAPIKey(false)
    }
  }

  return <section className="view settings-view" aria-labelledby="settings-title">
    <header className="view-header">
      <div><h1 id="settings-title">Settings</h1><p>Appearance, ModMaker, AI, and application storage</p></div>
      <div className="view-header__actions"><Button disabled={!draft || saving} onClick={reset}>Reset changes</Button><Button tone="primary" disabled={!draft || saving} onClick={() => void submit()}>{saving ? 'Saving' : 'Apply settings'}</Button></div>
    </header>

    {!draft ? <div className="settings-page-loading">Loading settings…</div> : <div className="settings-body"><div className="settings-form">
      <fieldset className="settings-section--wide"><legend>Appearance</legend>
        <div className="settings-row"><label>Theme<span>The quick switch remains available in the sidebar.</span></label><div className="segmented"><button className={draft.theme === 'dark' ? 'is-active' : ''} onClick={() => previewTheme('dark')}>Black glass</button><button className={draft.theme === 'light' ? 'is-active' : ''} onClick={() => previewTheme('light')}>Light</button></div></div>
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
      <fieldset><legend>Status bar</legend><label className="toggle-row"><input type="checkbox" checked={draft.showAIUsage} onChange={event => setDraft({ ...draft, showAIUsage: event.target.checked })}/><span>Show authenticated provider usage after Virgil has been used</span></label>
        {usage?.hasRuns && <div className="usage-table"><div><span>ModMaker runs</span><strong>{usage.runCount.toLocaleString()}</strong></div>{usage.totalTokens > 0 && <div><span>Recorded tokens</span><strong>{usage.totalTokens.toLocaleString()}</strong></div>}{(usage.limits ?? []).map(limit => <div key={`${limit.provider}-${limit.label}`}><span>{limit.provider} · {limit.label}</span><strong>{limit.unit === 'percent' ? `${Math.round(limit.used)}% used` : `${Math.round(limit.remaining)} ${limit.unit} left`}</strong></div>)}{usage.usageError && <p>{usage.usageError}</p>}</div>}
      </fieldset>

      <fieldset className="settings-section--wide"><legend>Virus Scanner · Virgil</legend><p className="settings-explainer">Signature-based scans run locally without AI. Full scans use these models for the automatic file review and final evidence-based assessment.</p><div className="settings-grid">
        <label className="settings-field"><span>File-review model</span><input value={draft.preScanModel} onChange={event => setDraft({ ...draft, preScanModel: event.target.value })}/><small>Small model recommended: gpt-5.6-luna</small></label>
        <label className="settings-field"><span>File-review reasoning</span><select value={draft.preScanReasoning} onChange={event => setDraft({ ...draft, preScanReasoning: event.target.value })}><option value="low">Low</option><option value="medium">Medium · recommended</option><option value="high">High</option><option value="xhigh">Extra high</option></select></label>
        <label className="settings-field"><span>Final assessment model</span><input value={draft.fullScanModel} onChange={event => setDraft({ ...draft, fullScanModel: event.target.value })}/><small>Large multimodal model recommended: gpt-5.6-sol</small></label>
        <label className="settings-field"><span>Final assessment reasoning</span><select value={draft.fullScanReasoning} onChange={event => setDraft({ ...draft, fullScanReasoning: event.target.value })}><option value="low">Low</option><option value="medium">Medium</option><option value="high">High</option><option value="xhigh">Extra high · recommended</option></select></label>
      </div></fieldset>

      <fieldset className="settings-section--wide"><legend>Virgil</legend>
        <div className="settings-grid">
          <label className="settings-field"><span>Connection</span><select value={draft.agentProfile} onChange={event => setDraft({ ...draft, agentProfile: event.target.value })}><option value="omp">OMP default</option><option value="codex">ChatGPT / Codex</option><option value="claude">Claude</option><option value="openrouter">OpenRouter</option><option value="openai">OpenAI API</option></select></label>
          <label className="settings-field"><span>Model</span><input value={draft.agentModel} onChange={event => setDraft({ ...draft, agentModel: event.target.value })} placeholder="Use profile default" maxLength={120}/></label>
          <label className="settings-field"><span>Context</span><select value={draft.contextMode} onChange={event => setDraft({ ...draft, contextMode: event.target.value })}><option value="focused">Focused · category only</option><option value="balanced">Balanced · project and category</option><option value="deep">Deep · cross-system references</option></select></label>
          <label className="settings-field"><span>API key</span><input type="password" value={apiKey} onChange={event => { setAPIKey(event.target.value); setClearAPIKey(false) }} placeholder={draft.hasApiKey && !clearAPIKey ? 'Stored securely for this user' : 'Optional; OMP OAuth needs no key'} autoComplete="off"/><small>Windows stores entered keys with DPAPI. ChatGPT and Claude subscriptions use accounts already connected in OMP.</small></label>
        </div>
        {draft.hasApiKey && <button className={`text-button ${clearAPIKey ? 'is-active' : ''}`} onClick={() => { setClearAPIKey(!clearAPIKey); setAPIKey('') }}>{clearAPIKey ? 'Stored API key will be removed' : 'Remove stored API key'}</button>}
      </fieldset>
    </div></div>}
  </section>
}

export function settingsUpdate(settings: AppSettings, overrides: Partial<SettingsUpdate> = {}): SettingsUpdate {
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
