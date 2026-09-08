import { useEffect, useLayoutEffect, useMemo, useState } from 'react'
import { Events } from '@wailsio/runtime'
import { AppService as API } from '../bindings/github.com/SignedAdam/beamng-mod-studio/index.js'
import type { LibraryItem, VirusScanProgress, VirusScanRun } from '../bindings/github.com/SignedAdam/beamng-mod-studio/models.js'
import { Icon } from './icons'
import { ModTable } from './ModTable'
import { Page, Spinner, formatDate } from './ui'
import './VirusScannerView.css'

type ScanMode = 'signature' | 'full'
type ScannerTab = 'scan' | 'history'
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

const SCANNER_FOCUS_FALLBACK = '#155E91'
const SCANNER_LIGHT_SURFACES = [
  '#F6FBFF',
  '#FFFFFF',
  '#EAF4FF',
  '#F0F8FF',
  '#DDEEFF',
  '#D3E8FF',
] as const

interface ScannerRGB {
  red: number
  green: number
  blue: number
  alpha: number
}

function parseScannerChannel(value: string, max: number): number | null {
  const normalized = value.trim()
  const amount = Number.parseFloat(normalized)
  if (!Number.isFinite(amount)) return null
  return Math.min(max, Math.max(0, normalized.endsWith('%') ? amount * max / 100 : amount))
}

function parseScannerAlpha(value: string): number | null {
  const normalized = value.trim()
  const amount = Number.parseFloat(normalized)
  if (!Number.isFinite(amount)) return null
  return Math.min(1, Math.max(0, normalized.endsWith('%') ? amount / 100 : amount))
}

function parseScannerColor(value: string): ScannerRGB | null {
  const normalized = value.trim()
  const hex = normalized.match(/^#([0-9a-f]{3,4}|[0-9a-f]{6}|[0-9a-f]{8})$/i)
  if (hex) {
    const digits = hex[1]
    const expanded = digits.length <= 4 ? digits.split('').map(digit => digit + digit).join('') : digits
    return {
      red: Number.parseInt(expanded.slice(0, 2), 16),
      green: Number.parseInt(expanded.slice(2, 4), 16),
      blue: Number.parseInt(expanded.slice(4, 6), 16),
      alpha: expanded.length === 8 ? Number.parseInt(expanded.slice(6, 8), 16) / 255 : 1,
    }
  }

  const rgb = normalized.match(/^rgba?\((.*)\)$/i)
  if (!rgb) return null
  const components = rgb[1].replace(/\s*\/\s*/, ',').split(/[\s,]+/).filter(Boolean)
  if (components.length < 3) return null
  const red = parseScannerChannel(components[0], 255)
  const green = parseScannerChannel(components[1], 255)
  const blue = parseScannerChannel(components[2], 255)
  const alpha = components.length > 3 ? parseScannerAlpha(components[3]) : 1
  if (red === null || green === null || blue === null || alpha === null) return null
  return { red, green, blue, alpha }
}

function resolveScannerColor(value: string): ScannerRGB | null {
  const direct = parseScannerColor(value)
  if (direct) return direct
  if (typeof document === 'undefined' || !document.body) return null

  const probe = document.createElement('span')
  probe.style.color = value
  if (!probe.style.color) return null
  probe.style.position = 'fixed'
  probe.style.visibility = 'hidden'
  probe.style.pointerEvents = 'none'
  document.body.append(probe)
  const computed = getComputedStyle(probe).color
  probe.remove()
  return parseScannerColor(computed)
}

function scannerChannelLuminance(value: number): number {
  const normalized = value / 255
  return normalized <= 0.03928
    ? normalized / 12.92
    : ((normalized + 0.055) / 1.055) ** 2.4
}

function scannerLuminance(color: ScannerRGB): number {
  return 0.2126 * scannerChannelLuminance(color.red)
    + 0.7152 * scannerChannelLuminance(color.green)
    + 0.0722 * scannerChannelLuminance(color.blue)
}

function compositeScannerColor(foreground: ScannerRGB, background: ScannerRGB): ScannerRGB {
  return {
    red: foreground.red * foreground.alpha + background.red * (1 - foreground.alpha),
    green: foreground.green * foreground.alpha + background.green * (1 - foreground.alpha),
    blue: foreground.blue * foreground.alpha + background.blue * (1 - foreground.alpha),
    alpha: 1,
  }
}

function scannerContrastRatio(first: ScannerRGB, second: ScannerRGB): number {
  const firstLuminance = scannerLuminance(first)
  const secondLuminance = scannerLuminance(second)
  const lighter = Math.max(firstLuminance, secondLuminance)
  const darker = Math.min(firstLuminance, secondLuminance)
  return (lighter + 0.05) / (darker + 0.05)
}

function scannerFocusIsAccessible(value: string): boolean {
  const focus = resolveScannerColor(value)
  if (!focus || focus.alpha <= 0) return false
  return SCANNER_LIGHT_SURFACES.every(surfaceValue => {
    const surface = parseScannerColor(surfaceValue)
    return surface !== null && scannerContrastRatio(compositeScannerColor(focus, surface), surface) >= 3
  })
}

export function VirusScannerView({ items, request, onLibraryChange, onNotify, onError }: VirusScannerProps) {
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [tab, setTab] = useState<ScannerTab>('scan')
  const [query, setQuery] = useState('')
  const [modeOpen, setModeOpen] = useState(false)
  const [running, setRunning] = useState(false)
  const [historyLoading, setHistoryLoading] = useState(true)
  const [runs, setRuns] = useState<VirusScanRun[]>([])
  const [queue, setQueue] = useState<Record<string, QueueEntry>>({})

  useLayoutEffect(() => {
    const root = document.documentElement
    const scanner = document.querySelector<HTMLElement>('.virus-scanner')
    if (!scanner) return

    const updateFocus = () => {
      if (root.dataset.theme !== 'light') {
        scanner.style.removeProperty('--scanner-focus')
        return
      }
      const configured = getComputedStyle(root).getPropertyValue('--user-emphasis').trim()
      const focus = scannerFocusIsAccessible(configured) ? configured : SCANNER_FOCUS_FALLBACK
      scanner.style.setProperty('--scanner-focus', focus)
    }

    updateFocus()
    const observer = typeof MutationObserver === 'undefined' ? null : new MutationObserver(records => {
      if (records.some(record => record.attributeName === 'data-theme' || record.attributeName === 'style')) {
        updateFocus()
      }
    })
    observer?.observe(root, { attributes: true, attributeFilter: ['data-theme', 'style'] })
    return () => {
      observer?.disconnect()
      scanner.style.removeProperty('--scanner-focus')
    }
  }, [])

  const visibleItems = useMemo(() => {
    const normalized = query.trim().toLowerCase()
    if (!normalized) return items
    return items.filter(item => [item.displayName, item.archivePath, item.healthLabel, String(item.kind)].some(value => value?.toLowerCase().includes(normalized)))
  }, [items, query])
  const selectedItems = useMemo(() => items.filter(item => selected.has(item.entityId)), [items, selected])
  const eligibleSelected = selectedItems.filter(item => item.linked)
  const queueEntries = useMemo(() => Object.values(queue), [queue])
  const itemsByID = useMemo(() => new Map(items.map(item => [item.entityId, item])), [items])
  const visibleRuns = useMemo(() => {
    const normalized = query.trim().toLowerCase()
    if (!normalized) return runs
    return runs.filter(run => [itemsByID.get(run.entityId)?.displayName, run.mode, run.verdict, run.status].some(value => value?.toLowerCase().includes(normalized)))
  }, [itemsByID, query, runs])

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

  return <Page
    title="Virus Scanner"
    className="virus-scanner"
    actions={[{
      key: 'scan-selected',
      label: `Scan selected (${eligibleSelected.length})`,
      icon: 'shield',
      role: 'primary',
      disabled: eligibleSelected.length === 0 || running,
      onClick: () => setModeOpen(true),
    }]}
  >
    <div className="page-toolbar">
      <label className="search-box scanner-search"><Icon name="search" size={16}/><input value={query} onChange={event => setQuery(event.target.value)} placeholder="Find a mod" aria-label="Find a mod"/></label>
      <div className="segmented" role="group" aria-label="Scanner section">
        <button type="button" aria-pressed={tab === 'scan'} className={tab === 'scan' ? 'is-active' : ''} onClick={() => setTab('scan')}>Scan</button>
        <button type="button" aria-pressed={tab === 'history'} className={tab === 'history' ? 'is-active' : ''} onClick={() => setTab('history')}>History</button>
      </div>
    </div>

    {tab === 'scan' ? <>
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

      <section className="scanner-library" aria-label="Mods available to scan">
        <ModTable
          surface="virus-scanner"
          className="scanner-mod-table"
          ariaLabel="Mods available to scan"
          items={visibleItems}
          emptyTitle="No mods match this search."
          resetKey={query}
          interaction={{
            kind: 'select',
            selectedIDs: selected,
            disabled: running,
            isSelectable: item => item.linked,
            onToggle: item => toggleItem(item.entityId),
            onToggleAll: toggleVisible,
          }}
        />
      </section>
    </> : <section className="scan-history" aria-label="Scan history">
      <header><h2>Scan history</h2><button onClick={() => void loadHistory()} disabled={historyLoading} aria-label="Refresh scan history"><Icon name="refresh" size={15}/></button></header>
      {historyLoading && runs.length === 0 ? <div className="scanner-loading"><Spinner/><span>Loading scan history</span></div> : visibleRuns.length === 0 ? <p className="scan-history__empty">{runs.length === 0 ? 'No virus scans have run yet.' : 'No scans match this search.'}</p> : <div className="scan-history__rows">{visibleRuns.map(run => <article className={`scan-history__row scan-history__row--${run.status === 'failed' ? 'scan_failed' : run.verdict || 'unscanned'}`} key={run.id}>
        <Icon name={run.status === 'failed' ? 'error' : healthIcon(run.verdict)} size={16}/>
        <div><strong>{itemsByID.get(run.entityId)?.displayName || 'Unknown mod'}</strong><span>{run.mode === 'full' ? 'Full Virgil scan' : 'Signature-based scan'} · {(run.stages?.length ?? 0).toLocaleString()} {(run.stages?.length ?? 0) === 1 ? 'stage' : 'stages'}</span></div>
        <time>{formatDate(run.createdAt)}</time>
        <b className={`health-text health-text--${run.status === 'failed' ? 'scan_failed' : run.verdict}`}>{run.status === 'failed' ? 'Scan failed' : healthLabel(run.verdict)}</b>
      </article>)}</div>}
    </section>}

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
</Page>
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
