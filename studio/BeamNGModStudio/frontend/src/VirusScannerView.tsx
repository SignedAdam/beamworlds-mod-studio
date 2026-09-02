import { useEffect, useMemo, useState } from 'react'
import { Events } from '@wailsio/runtime'
import { AppService as API } from '../bindings/github.com/SignedAdam/beamng-mod-studio/index.js'
import type { LibraryItem, VirusScanProgress, VirusScanRun } from '../bindings/github.com/SignedAdam/beamng-mod-studio/models.js'
import { Icon } from './icons'
import { Button, Spinner, formatDate, kindLabel } from './ui'

type ScanMode = 'signature' | 'full'
type QueueStatus = 'queued' | 'running' | 'complete' | 'failed'

interface ScannerRequest {
  entityIDs: string[]
  nonce: number
}

interface VirusScannerProps {
  items: LibraryItem[]
  request: ScannerRequest | null
  onLibraryChange: () => Promise<void>
  onNotify: (message: string, tone?: 'success' | 'error' | 'info') => void
  onError: (error: unknown) => void
}

interface QueueEntry {
  entityID: string
  mode: ScanMode
  status: QueueStatus
  stage: string
  stageIndex: number
  stageTotal: number
  message: string
  verdict: string
  error: string
}

export function VirusScannerView({ items, request, onLibraryChange, onNotify, onError }: VirusScannerProps) {
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [query, setQuery] = useState('')
  const [modeOpen, setModeOpen] = useState(false)
  const [running, setRunning] = useState(false)
  const [historyLoading, setHistoryLoading] = useState(true)
  const [runs, setRuns] = useState<VirusScanRun[]>([])
  const [queue, setQueue] = useState<Record<string, QueueEntry>>({})

  const visibleItems = useMemo(() => {
    const normalized = query.trim().toLowerCase()
    if (!normalized) return items
    return items.filter(item => [item.displayName, item.archivePath, item.healthLabel, String(item.kind)].some(value => value?.toLowerCase().includes(normalized)))
  }, [items, query])
  const selectedItems = useMemo(() => items.filter(item => selected.has(item.entityId)), [items, selected])
  const eligibleSelected = selectedItems.filter(item => item.linked)
  const queueEntries = useMemo(() => Object.values(queue), [queue])
  const itemsByID = useMemo(() => new Map(items.map(item => [item.entityId, item])), [items])

  const loadHistory = async () => {
    setHistoryLoading(true)
    try {
      setRuns(await API.ListVirusScans('') ?? [])
    } catch (error) {
      onError(error)
    } finally {
      setHistoryLoading(false)
    }
  }

  useEffect(() => {
    void loadHistory()
  }, [])

  useEffect(() => {
    if (!request) return
    setSelected(new Set(request.entityIDs))
    setModeOpen(true)
  }, [request?.nonce])

  useEffect(() => {
    const stop = Events.On('virus:scan', event => {
      const progress = event.data as VirusScanProgress
      setQueue(current => {
        const previous = current[progress.entityId]
        if (!previous) return current
        return {
          ...current,
          [progress.entityId]: {
            ...previous,
            status: progress.status === 'complete' ? 'complete' : progress.status === 'failed' ? 'failed' : 'running',
            stage: progress.stage,
            stageIndex: progress.stageIndex || previous.stageIndex,
            stageTotal: progress.stageTotal || previous.stageTotal,
            message: progress.message || previous.message,
            error: progress.error || '',
          },
        }
      })
    })
    return stop
  }, [])

  const toggleItem = (entityID: string) => {
    setSelected(current => {
      const next = new Set(current)
      if (next.has(entityID)) next.delete(entityID)
      else next.add(entityID)
      return next
    })
  }

  const toggleVisible = () => {
    const selectable = visibleItems.filter(item => item.linked)
    const everySelected = selectable.length > 0 && selectable.every(item => selected.has(item.entityId))
    setSelected(current => {
      const next = new Set(current)
      for (const item of selectable) {
        if (everySelected) next.delete(item.entityId)
        else next.add(item.entityId)
      }
      return next
    })
  }

  const runSelected = async (mode: ScanMode) => {
    const targets = items.filter(item => selected.has(item.entityId) && item.linked)
    if (targets.length === 0 || running) return
    setModeOpen(false)
    setRunning(true)
    setQueue(Object.fromEntries(targets.map(item => [item.entityId, {
      entityID: item.entityId,
      mode,
      status: 'queued' as const,
      stage: 'signature',
      stageIndex: 0,
      stageTotal: mode === 'full' ? 3 : 1,
      message: 'Waiting to scan',
      verdict: '',
      error: '',
    }])))
    let completed = 0
    let failed = 0
    for (const item of targets) {
      setQueue(current => ({ ...current, [item.entityId]: { ...current[item.entityId], status: 'running', stageIndex: 1, message: 'Starting signature analysis' } }))
      try {
        const result = await API.RunVirusScan(item.entityId, mode)
        completed++
        setQueue(current => ({ ...current, [item.entityId]: { ...current[item.entityId], status: 'complete', stage: result.currentStage, stageIndex: mode === 'full' ? 3 : 1, message: healthLabel(result.verdict), verdict: result.verdict, error: '' } }))
      } catch (error) {
        failed++
        const message = error instanceof Error ? error.message : String(error)
        setQueue(current => ({ ...current, [item.entityId]: { ...current[item.entityId], status: 'failed', message: 'Scan failed', error: message } }))
      }
    }
    try {
      await Promise.all([loadHistory(), onLibraryChange()])
    } catch (error) {
      onError(error)
    } finally {
      setRunning(false)
    }
    if (failed > 0) onNotify(`${completed.toLocaleString()} scans completed · ${failed.toLocaleString()} failed`, 'error')
    else onNotify(`${completed.toLocaleString()} ${mode === 'full' ? 'full' : 'signature-based'} scans completed`, 'success')
  }

  return <div className="virus-scanner">
    <header className="virus-scanner__header">
      <div><span className="eyebrow">SECURITY</span><h1>Virus Scanner</h1><p>Check mods without launching or extracting them.</p></div>
      <Button icon="shield" tone="primary" disabled={eligibleSelected.length === 0 || running} onClick={() => setModeOpen(true)}>Scan selected ({eligibleSelected.length})</Button>
    </header>

    {queueEntries.length > 0 && <section className="scan-queue" aria-label="Scan queue">
      <header><h2>{running ? 'Scanning mods' : 'Latest scan queue'}</h2><span>{queueEntries.filter(entry => entry.status === 'complete').length}/{queueEntries.length} complete</span></header>
      <div>{queueEntries.map(entry => {
        const item = itemsByID.get(entry.entityID)
        const progress = entry.status === 'queued' ? 0 : entry.status === 'complete' ? 100 : Math.max(8, Math.round((entry.stageIndex / Math.max(1, entry.stageTotal)) * 100))
        return <article className={`scan-queue__row scan-queue__row--${entry.status}`} key={entry.entityID}>
          <Icon name={entry.status === 'complete' ? healthIcon(entry.verdict) : entry.status === 'failed' ? 'error' : entry.status === 'queued' ? 'activity' : 'scan'} size={17}/>
          <div className="scan-queue__identity"><strong>{item?.displayName || entry.entityID}</strong><span>{entry.status === 'running' ? `${stageLabel(entry.stage)} · ${entry.message}` : entry.error || entry.message}</span></div>
          <div className="scan-progress"><span style={{ width: `${progress}%` }}/></div>
          <b>{entry.status === 'complete' ? healthLabel(entry.verdict) : entry.status === 'failed' ? 'Failed' : entry.status === 'queued' ? 'Queued' : `${entry.stageIndex}/${entry.stageTotal}`}</b>
        </article>
      })}</div>
    </section>}

    <section className="scanner-library">
      <header>
        <div><h2>Mod library</h2><span>Select one or more available source archives.</span></div>
        <label className="scanner-search"><Icon name="search" size={16}/><input value={query} onChange={event => setQuery(event.target.value)} placeholder="Find a mod"/></label>
      </header>
      <div className="scanner-table" role="table">
        <div className="scanner-table__head" role="row">
          <button className="scanner-check" onClick={toggleVisible} aria-label="Select all visible mods"><span className={visibleItems.filter(item => item.linked).length > 0 && visibleItems.filter(item => item.linked).every(item => selected.has(item.entityId)) ? 'is-checked' : ''}/></button>
          <span>Mod</span><span>Type</span><span>Health</span><span>Last scan</span>
        </div>
        {visibleItems.length === 0 ? <div className="scanner-table__empty">No mods match this search.</div> : visibleItems.map(item => <button className={`scanner-table__row ${selected.has(item.entityId) ? 'is-selected' : ''}`} key={item.entityId} disabled={!item.linked || running} onClick={() => toggleItem(item.entityId)} role="row">
          <span className="scanner-check"><span className={selected.has(item.entityId) ? 'is-checked' : ''}/></span>
          <span className="scanner-table__mod"><Icon name="archive" size={16}/><span><strong>{item.displayName}</strong>{!item.linked && <small>Source unavailable</small>}</span></span>
          <span>{kindLabel(String(item.kind))}</span>
          <span className={`health-pill health-pill--${item.healthStatus || 'unscanned'}`}><Icon name={healthIcon(item.healthStatus)} size={14}/>{item.healthLabel || 'Not scanned'}</span>
          <time>{item.lastSecurityScanAt ? formatDate(item.lastSecurityScanAt) : 'Never'}</time>
        </button>)}
      </div>
    </section>

    <section className="scan-history">
      <header><div><h2>Scan history</h2><span>Every run and stage remains attached to its mod metadata.</span></div><button onClick={() => void loadHistory()} disabled={historyLoading} aria-label="Refresh scan history"><Icon name="refresh" size={15}/></button></header>
      {historyLoading && runs.length === 0 ? <div className="scanner-loading"><Spinner/><span>Loading scan history</span></div> : runs.length === 0 ? <p className="scan-history__empty">No virus scans have run yet.</p> : <div className="scan-history__rows">{runs.map(run => <article key={run.id}>
        <Icon name={run.status === 'failed' ? 'error' : healthIcon(run.verdict)} size={16}/>
        <div><strong>{itemsByID.get(run.entityId)?.displayName || 'Unknown mod'}</strong><span>{run.mode === 'full' ? 'Full Virgil scan' : 'Signature-based scan'} · {(run.stages?.length ?? 0).toLocaleString()} {(run.stages?.length ?? 0) === 1 ? 'stage' : 'stages'}</span></div>
        <time>{formatDate(run.createdAt)}</time>
        <b className={`health-text health-text--${run.status === 'failed' ? 'scan_failed' : run.verdict}`}>{run.status === 'failed' ? 'Scan failed' : healthLabel(run.verdict)}</b>
      </article>)}</div>}
    </section>

    {modeOpen && <div className="scan-mode-backdrop" role="presentation" onMouseDown={event => { if (event.currentTarget === event.target && !running) setModeOpen(false) }}>
      <section className="scan-mode-dialog" role="dialog" aria-modal="true" aria-labelledby="scan-mode-title">
        <header><div><span className="eyebrow">SCAN {eligibleSelected.length} {eligibleSelected.length === 1 ? 'MOD' : 'MODS'}</span><h2 id="scan-mode-title">Choose scan depth</h2></div><button onClick={() => setModeOpen(false)} aria-label="Close scan options"><Icon name="close" size={16}/></button></header>
        <button className="scan-mode-option" onClick={() => void runSelected('signature')} disabled={running || eligibleSelected.length === 0}>
          <Icon name="scan" size={24}/><div><strong>Signature-based scan</strong><p>Checks every archive entry for known suspicious code signatures, executable markers, path tricks, and structural damage.</p><span>No AI · runs locally · fastest</span></div><Icon name="arrow" size={16}/>
        </button>
        <button className="scan-mode-option scan-mode-option--full" onClick={() => void runSelected('full')} disabled={running || eligibleSelected.length === 0}>
          <Icon name="shield" size={24}/><div><strong>Full Virgil scan</strong><p>Runs the signature scan, reads every file, reviews files in AI batches, then produces a final security assessment automatically.</p><span>Three automatic stages · archive content is never executed</span></div><Icon name="arrow" size={16}/>
        </button>
      </section>
    </div>}
  </div>
}

function stageLabel(stage: string) {
  switch (stage) {
  case 'signature': return 'Signature analysis'
  case 'ai_triage': return 'Virgil file review'
  case 'deep_analysis': return 'Final assessment'
  default: return 'Preparing scan'
  }
}

function healthLabel(status: string) {
  switch (status) {
  case 'safe': return 'Safe'
  case 'review': return 'Review needed'
  case 'threat': return 'Threat found'
  case 'broken': return 'Broken'
  case 'scanning': return 'Scanning'
  case 'scan_failed': return 'Scan failed'
  default: return 'Not scanned'
  }
}

function healthIcon(status: string): 'check' | 'warning' | 'error' | 'scan' | 'shield' {
  switch (status) {
  case 'safe': return 'check'
  case 'review': return 'warning'
  case 'threat':
  case 'broken':
  case 'scan_failed': return 'error'
  case 'scanning': return 'scan'
  default: return 'shield'
  }
}
