import { useCallback, useEffect, useRef, useState } from 'react'
import { AppService as API } from '../bindings/github.com/SignedAdam/beamng-mod-studio/index.js'
import type { ArchiveCapability, SetupInput, SetupState } from '../bindings/github.com/SignedAdam/beamng-mod-studio/models.js'
import { DeploymentModeSelector } from './ArchiveDeployment'
import type { DeploymentMode } from './archiveTypes'
import { DEPLOYMENT_MODE_LABELS } from './archiveTypes'
import { BeamWorldsMark, Icon } from './icons'
import { Button } from './ui'

interface SetupWizardProps {
  state: SetupState
  required: boolean
  onCancel?: () => void
  onError: (error: unknown) => void
}

const steps = ['BeamNG', 'Mods', 'Storage', 'Review']

export function SetupWizard({ state, required, onCancel, onError }: SetupWizardProps) {
  const [step, setStep] = useState(0)
  const [draft, setDraft] = useState<SetupInput>(normalizeSetup(state.suggested))
  // Deployment mode is kept in local draft state only; persisted through SaveSetup.
  const [draftMode, setDraftMode] = useState<DeploymentMode>('auto')
  const [saving, setSaving] = useState(false)
  const [restarting, setRestarting] = useState(false)
  const [error, setError] = useState('')

  // Capability probe for the draft paths.
  const [capabilities, setCapabilities] = useState<ArchiveCapability[]>([])
  const [capMixed, setCapMixed] = useState(false)
  const [capWarning, setCapWarning] = useState('')
  const [capLoading, setCapLoading] = useState(false)
  const [capError, setCapError] = useState('')
  const probeVersionRef = useRef(0)
  const inflightRef = useRef<{ cancel(): void } | null>(null)

  useEffect(() => {
    setDraft(normalizeSetup(state.suggested))
    setStep(0)
    setError('')
  }, [state])

  // Probe capabilities when draft paths change (targeted probe).
  const probeCapabilities = useCallback((src: string, dst: string) => {
    if (inflightRef.current) {
      inflightRef.current.cancel()
      inflightRef.current = null
    }
    if (!src.trim() || !dst.trim()) {
      setCapabilities([])
      setCapLoading(false)
      setCapError('')
      return
    }
    const version = ++probeVersionRef.current
    setCapLoading(true)
    setCapError('')
    const request = API.ProbeArchiveDeployment(src, dst)
    inflightRef.current = request
    request
      .then((result) => {
        if (version !== probeVersionRef.current) return
        inflightRef.current = null
        // ProbeArchiveDeployment returns a single ArchiveCapability.
        setCapabilities([result as ArchiveCapability])
        setCapMixed(false)
        setCapWarning('')
        setCapLoading(false)
      })
      .catch((err: unknown) => {
        if (version !== probeVersionRef.current) return
        inflightRef.current = null
        if (/cancel/i.test(String(err))) {
          setCapLoading(false)
          return
        }
        setCapError(err instanceof Error ? err.message : 'Could not check capabilities.')
        setCapLoading(false)
      })
  }, [])

  useEffect(() => {
    probeCapabilities(draft.libraryDir, draft.activeModsDir)
    return () => {
      if (inflightRef.current) {
        inflightRef.current.cancel()
        inflightRef.current = null
      }
    }
  }, [draft.libraryDir, draft.activeModsDir, probeCapabilities])

  const choose = async (field: keyof Pick<SetupInput, 'beamngRoot' | 'activeModsDir' | 'gameInstallDir' | 'libraryDir' | 'dataDir'>, title: string) => {
    try {
      const selected = await API.PickDirectory(title, draft[field])
      if (selected) setDraft(current => ({ ...current, [field]: selected }))
    } catch (reason) {
      setError(errorMessage(reason))
      onError(reason)
    }
  }

  const next = () => {
    const message = validateStep(step, draft)
    if (message) {
      setError(message)
      return
    }
    setError('')
    setStep(current => Math.min(current + 1, steps.length - 1))
  }

  const finish = async () => {
    setSaving(true)
    setError('')
    try {
      // archiveDeploymentMode is persisted through SaveSetup alongside the paths.
      await API.SaveSetup({ ...draft, archiveDeploymentMode: draftMode } as SetupInput)
      setRestarting(true)
      await API.RestartApplication()
    } catch (reason) {
      setSaving(false)
      setRestarting(false)
      setError(errorMessage(reason))
    }
  }

  return <div className="setup-shell">
    <section className="setup-wizard" aria-labelledby="setup-title">
      <header className="setup-header">
        <div className="brand-lockup" aria-label="BeamWorlds Mod Studio"><BeamWorldsMark/><div><strong>BeamWorlds</strong><span>Mod Studio</span></div></div>
        {!required && onCancel && <button className="icon-button" onClick={onCancel} aria-label="Close setup"><Icon name="close"/></button>}
      </header>
      <div className="setup-progress" aria-label={`Setup step ${step + 1} of ${steps.length}`}>
        {steps.map((label, index) => <div key={label} className={index === step ? 'is-active' : index < step ? 'is-complete' : ''}><span>{index < step ? '✓' : index + 1}</span><small>{label}</small></div>)}
      </div>

      <div className="setup-content">
        {step === 0 && <>
          <SetupTitle eyebrow="First-run setup" title="Connect BeamNG" detail="These locations were detected when possible. Nothing is moved or changed yet."/>
          <DirectoryField label="Game installation" detail="The folder containing Bin64/BeamNG.drive.x64.exe" value={draft.gameInstallDir} onChange={value => setDraft({ ...draft, gameInstallDir: value })} onBrowse={() => void choose('gameInstallDir', 'Choose the BeamNG installation')}/>
          <DirectoryField label="BeamNG user folder" detail="Your existing settings, controls, saves, and current version folder stay here." value={draft.beamngRoot} onChange={value => setDraft({ ...draft, beamngRoot: value })} onBrowse={() => void choose('beamngRoot', 'Choose the BeamNG user folder')}/>
        </>}

        {step === 1 && <>
          <SetupTitle eyebrow="Mod locations" title="Find your mods" detail="Mod profiles only change which mods load. Your normal BeamNG game data remains shared."/>
          <DirectoryField label="Current BeamNG mods" detail="Usually the current/mods folder inside your BeamNG user folder." value={draft.activeModsDir} onChange={value => setDraft({ ...draft, activeModsDir: value })} onBrowse={() => void choose('activeModsDir', 'Choose the current BeamNG mods folder')}/>
          <DirectoryField label="Mod library" detail="Archives stored outside the active game folder can remain here." value={draft.libraryDir} onChange={value => setDraft({ ...draft, libraryDir: value })} onBrowse={() => void choose('libraryDir', 'Choose the mod library')}/>
          {state.nativeModCount > 0 && <div className="setup-detection"><Icon name="check"/><span>Found <strong>{state.nativeModCount.toLocaleString()}</strong> registered mods · <strong>{state.nativeEnabledCount.toLocaleString()}</strong> currently enabled</span></div>}
        </>}

        {step === 2 && <>
          <SetupTitle eyebrow="BeamWorlds storage" title="Storage and deployment" detail="Choose where BeamWorlds stores its working data, and how mod archives reach the game."/>
          <DirectoryField label="Studio data and profile staging" detail="Database, previews, editable workspaces, exports, and managed profile files." value={draft.dataDir} onChange={value => setDraft({ ...draft, dataDir: value })} onBrowse={() => void choose('dataDir', 'Choose BeamWorlds storage')}/>
          <DeploymentModeSelector
            mode={draftMode}
            capabilities={capabilities}
            mixed={capMixed}
            warning={capWarning}
            loading={capLoading}
            error={capError}
            onChange={setDraftMode}
            onRefresh={() => probeCapabilities(draft.libraryDir, draft.activeModsDir)}
          />
          <div className="setup-note"><Icon name="folder"/><p>This is not another BeamNG user profile. Settings, controls, and saves continue using <strong>{draft.beamngRoot || 'your BeamNG user folder'}</strong>.</p></div>
        </>}

        {step === 3 && <>
          <SetupTitle eyebrow="Ready" title="Review and start" detail="BeamWorlds will save these locations, restart once, and scan both mod folders."/>
          <dl className="setup-summary">
            <SummaryRow label="BeamNG" value={draft.gameInstallDir}/>
            <SummaryRow label="Game data" value={draft.beamngRoot}/>
            <SummaryRow label="Current mods" value={draft.activeModsDir}/>
            <SummaryRow label="Library" value={draft.libraryDir}/>
            <SummaryRow label="Studio storage" value={draft.dataDir}/>
            <SummaryRow label="Install method" value={DEPLOYMENT_MODE_LABELS[draftMode] ?? 'Automatic'}/>
          </dl>
          <div className="setup-note setup-note--accent"><Icon name="play"/><p>A mod profile is the union of its reusable presets and its individually selected mods. It changes only the loaded mod set.</p></div>
        </>}

        {error && <div className="setup-error" role="alert"><Icon name="error"/><span>{error}</span></div>}
      </div>

      <footer className="setup-footer">
        <div>{step > 0 && !restarting && <Button onClick={() => { setError(''); setStep(current => current - 1) }}>Back</Button>}</div>
        <div>
          {!required && onCancel && step === 0 && <Button onClick={onCancel}>Cancel</Button>}
          {step < steps.length - 1 ? <Button tone="primary" onClick={next}>Continue</Button> : <Button tone="primary" disabled={saving || restarting} onClick={() => void finish()}>{restarting ? 'Restarting BeamWorlds' : saving ? 'Saving setup' : 'Save and restart'}</Button>}
        </div>
      </footer>
    </section>
  </div>
}

function SetupTitle({ eyebrow, title, detail }: { eyebrow: string; title: string; detail: string }) {
  return <div className="setup-title"><span>{eyebrow}</span><h1 id="setup-title">{title}</h1><p>{detail}</p></div>
}

function DirectoryField({ label, detail, value, onChange, onBrowse }: { label: string; detail: string; value: string; onChange: (value: string) => void; onBrowse: () => void }) {
  return <label className="setup-field"><span><strong>{label}</strong><small>{detail}</small></span><div><input value={value} onChange={event => onChange(event.target.value)} spellCheck={false}/><Button type="button" onClick={onBrowse}>Browse</Button></div></label>
}

function SummaryRow({ label, value }: { label: string; value: string }) {
  return <div><dt>{label}</dt><dd title={value}>{value}</dd></div>
}

function normalizeSetup(input: SetupInput): SetupInput {
  return { ...input, additionalScanRoots: input.additionalScanRoots ?? [] }
}

function validateStep(step: number, input: SetupInput): string {
  if (step === 0 && (!input.gameInstallDir.trim() || !input.beamngRoot.trim())) return 'Choose the BeamNG installation and user folder.'
  if (step === 1 && (!input.activeModsDir.trim() || !input.libraryDir.trim())) return 'Choose the current mods folder and mod library.'
  if (step === 2 && !input.dataDir.trim()) return 'Choose where BeamWorlds should store its working data.'
  return ''
}

function errorMessage(error: unknown): string {
  if (error instanceof Error) return error.message
  return typeof error === 'string' ? error : 'Setup could not be saved.'
}
