import { useMemo, useState } from 'react'
import type { EventRecord } from '../bindings/github.com/SignedAdam/beamng-mod-studio/models.js'
import type { Issue, Manifest } from '../bindings/github.com/SignedAdam/beamworlds-modkit/models.js'
import { Icon, type IconName } from './icons'
import { Spinner, formatDate } from './ui'
import './InspectorPanels.css'

export interface StructuralIssuesPanelProps {
  issues?: readonly Issue[] | null
}

export interface MetaPanelProps {
  kind?: string | null
  manifest?: Manifest | null
}

export interface HistoryPanelProps {
  history?: readonly EventRecord[] | null
  loading?: boolean
}

type IssueSeverity = 'error' | 'warning' | 'info' | 'other'

type IssueGroup = {
  code: string
  severity: IssueSeverity
  issues: readonly Issue[]
}

export function StructuralIssuesPanel({ issues }: StructuralIssuesPanelProps) {
  const groups = useMemo(() => groupIssues(issues ?? []), [issues])
  const [expanded, setExpanded] = useState<Record<string, boolean>>({})

  if (groups.length === 0) {
    return <div className="inspector-panels inspector-structural-issues"><p className="inspector-panels__empty" role="status">No structural issues found.</p></div>
  }

  const toggleGroup = (code: string) => {
    setExpanded(current => ({ ...current, [code]: !(current[code] ?? true) }))
  }

  return <div className="inspector-panels inspector-structural-issues">
    {groups.map(group => {
      const groupKey = group.code
      const expandedGroup = expanded[groupKey] ?? true
      const fragment = stableDOMFragment(groupKey)
      const panelID = `structural-issues-group-${fragment}`
      const headerID = `${panelID}-header`
      return <section className={`inspector-issues__group inspector-issues__group--${group.severity}`} key={groupKey}>
        <h3 className="inspector-issues__heading">
          <button
            id={headerID}
            type="button"
            className="inspector-issues__toggle"
            aria-expanded={expandedGroup}
            aria-controls={panelID}
            aria-label={`${group.code || 'Unknown issue type'}, ${group.issues.length.toLocaleString()} ${group.issues.length === 1 ? 'issue' : 'issues'}, ${severityLabel(group.severity)} severity`}
            onClick={() => toggleGroup(groupKey)}
          >
            <Icon className="inspector-issues__severity" name={severityIcon(group.severity)} size={16}/>
            <span className="inspector-issues__type">{group.code || 'Unknown issue type'}</span>
            <span className="inspector-issues__count">{group.issues.length.toLocaleString()}</span>
            <Icon className={`inspector-issues__disclosure${expandedGroup ? ' is-expanded' : ''}`} name="chevron" size={15}/>
          </button>
        </h3>
        {expandedGroup && <div className="inspector-issues__rows" id={panelID} aria-labelledby={headerID}>
          {group.issues.map((issue, index) => <article className="inspector-issues__row" key={issueRowKey(issue, index)}>
            <p className="inspector-issues__message">{issue.message || 'No message recorded.'}</p>
            {issue.path && <code className="inspector-issues__path" title={issue.path}>{issue.path}</code>}
          </article>)}
        </div>}
      </section>
    })}
  </div>
}

export function MetaPanel({ kind, manifest }: MetaPanelProps) {
  const selectedKind = String(kind ?? manifest?.kind ?? '').toLowerCase()
  const ui = manifest ? objectField(manifest, 'ui') : undefined
  const jbeam = manifest ? objectField(manifest, 'jbeam') : undefined
  const sharedAssets = manifest ? objectField(manifest, 'sharedAssets') : undefined
  const hasMetaCounters = sharedAssets !== undefined && sharedAssets !== null
  const indexedUI = hasMetaCounters ? ui : undefined
  const sharedAssetCount = indexedCount(sharedAssets, 'files')

  return <div className="inspector-panels inspector-meta-panel">
    <dl className="inspector-meta__list">
      <MetaRow label="Lua scripts" value={indexedDisplay(indexedCount(indexedUI, 'luaFiles'))}/>
      <MetaRow label="JavaScript files" value={indexedDisplay(indexedCount(indexedUI, 'javaScriptFiles'))}/>
      <MetaRow label="CSS files" value={indexedDisplay(indexedCount(indexedUI, 'cssFiles'))}/>
      <MetaRow label="HTML files" value={indexedDisplay(indexedCount(indexedUI, 'htmlFiles'))}/>
      {selectedKind === 'vehicle' && <MetaRow label="Beams" value={indexedDisplay(indexedCount(jbeam, 'declaredBeams'))}/>}
      <MetaRow label="Shared assets" value={sharedAssetsDisplay(sharedAssetCount)}/>
    </dl>
  </div>
}


function MetaRow({ label, value }: { label: string; value: string }) {
  const unavailable = value === 'Unavailable'
  return <div className="inspector-meta__row">
    <dt>{label}</dt>
    <dd className={unavailable ? 'is-unavailable' : undefined}>{value}</dd>
  </div>
}

export function HistoryPanel({ history, loading = false }: HistoryPanelProps) {
  const events = history ?? []
  const [expanded, setExpanded] = useState<Set<string>>(() => new Set())
  const eventEntries = useMemo(() => events.map(event => ({ event, identity: eventIdentity(event) })), [events])

  if (loading && history == null) {
    return <div className="inspector-panels inspector-history-panel inspector-history-panel--loading"><Spinner/><span>Loading history</span></div>
  }

  if (eventEntries.length === 0) {
    return <div className="inspector-panels inspector-history-panel"><p className="inspector-panels__empty" role="status">No history recorded.</p></div>
  }

  const toggleEvent = (identity: string) => {
    setExpanded(current => {
      const next = new Set(current)
      if (next.has(identity)) next.delete(identity)
      else next.add(identity)
      return next
    })
  }

  return <div className="inspector-panels inspector-history-panel">
    {eventEntries.map(({ event, identity }) => {
      const expandedEvent = expanded.has(identity)
      const fragment = stableDOMFragment(identity)
      const panelID = `history-event-${fragment}`
      const headerID = `${panelID}-header`
      const summary = historyEventSummary(event)
      return <article className={`inspector-history__event${expandedEvent ? ' is-expanded' : ''}`} key={identity}>
        <h3 className="inspector-history__heading">
          <button
            id={headerID}
            type="button"
            className="inspector-history__toggle"
            aria-expanded={expandedEvent}
            aria-controls={panelID}
            aria-label={`${formatDate(event.at)}: ${summary}`}
            onClick={() => toggleEvent(identity)}
          >
            <Icon className={`inspector-history__disclosure${expandedEvent ? ' is-expanded' : ''}`} name="chevron" size={14}/>
            <time dateTime={event.at || undefined}>{formatDate(event.at)}</time>
            <span className="inspector-history__summary" title={summary}>{summary}</span>
          </button>
        </h3>
        {expandedEvent && <dl className="inspector-history__details" id={panelID} aria-labelledby={headerID}>
          <HistoryDetail label="ID" value={formatEventValue(event.id)}/>
          <HistoryDetail label="Type" value={event.type || 'Unknown event'}/>
          <HistoryDetail label="Recorded" value={formatDate(event.at)}/>
          <HistoryDetail label="Entity" value={event.entityId || 'Not recorded'}/>
          <HistoryDetail label="Summary" value={summary}/>
          {Object.entries(event.data ?? {}).map(([key, value]) => <HistoryDetail key={key} label={readableFieldLabel(key)} value={formatEventValue(value)}/>) }
        </dl>}
      </article>
    })}
  </div>
}

function groupIssues(issues: readonly Issue[]): IssueGroup[] {
  const groups = new Map<string, Issue[]>()
  for (const issue of issues) {
    const code = String(issue.code ?? '')
    const existing = groups.get(code)
    if (existing) existing.push(issue)
    else groups.set(code, [issue])
  }

  return [...groups.entries()]
    .map(([code, groupedIssues]) => ({ code, severity: groupSeverity(groupedIssues), issues: groupedIssues }))
    .sort((left, right) => {
      const severityOrder = severityRank(right.severity) - severityRank(left.severity)
      if (severityOrder !== 0) return severityOrder
      return left.code < right.code ? -1 : left.code > right.code ? 1 : 0
    })
}

function groupSeverity(issues: readonly Issue[]): IssueSeverity {
  let result: IssueSeverity = 'other'
  for (const issue of issues) {
    const severity = normalizeIssueSeverity(issue.severity)
    if (severityRank(severity) > severityRank(result)) result = severity
  }
  return result
}

function normalizeIssueSeverity(value: unknown): IssueSeverity {
  const severity = String(value ?? '').toLowerCase()
  if (severity === 'error') return 'error'
  if (severity === 'warning') return 'warning'
  if (severity === 'info') return 'info'
  return 'other'
}

function severityRank(severity: IssueSeverity): number {
  if (severity === 'error') return 3
  if (severity === 'warning') return 2
  if (severity === 'info') return 1
  return 0
}

function severityIcon(severity: IssueSeverity): IconName {
  if (severity === 'error') return 'error'
  if (severity === 'warning') return 'warning'
  return 'activity'
}

function severityLabel(severity: IssueSeverity): string {
  if (severity === 'error') return 'Error'
  if (severity === 'warning') return 'Warning'
  if (severity === 'info') return 'Info'
  return 'Unspecified'
}

function issueRowKey(issue: Issue, index: number): string {
  return `${String(issue.code ?? '')}\u0000${String(issue.path ?? '')}\u0000${String(issue.message ?? '')}\u0000${index}`
}

function indexedCount(source: unknown, key: string): number | null {
  const value = objectField(source, key)
  return typeof value === 'number' && Number.isFinite(value) && value >= 0 ? value : null
}

function indexedDisplay(value: number | null): string {
  return value == null ? 'Unavailable' : value.toLocaleString()
}


function sharedAssetsDisplay(value: number | null): string {
  if (value == null) return 'Unavailable'
  return value === 0 ? 'None found (0)' : `${value.toLocaleString()} present`
}

function objectField(source: unknown, key: string): unknown {
  if (!source || typeof source !== 'object') return undefined
  return (source as Record<string, unknown>)[key]
}


function eventIdentity(event: EventRecord): string {
  if (Number.isFinite(event.id) && event.id !== 0) return `event-id-${event.id}`
  return `event-${event.at}\u0000${event.entityId}\u0000${event.type}\u0000${stableSerialize(event.data)}`
}

function stableSerialize(value: unknown): string {
  if (Array.isArray(value)) return `[${value.map(entry => stableSerialize(entry)).join(',')}]`
  if (value && typeof value === 'object') {
    return `{${Object.entries(value as Record<string, unknown>).sort(([left], [right]) => left < right ? -1 : left > right ? 1 : 0).map(([key, entry]) => `${key}:${stableSerialize(entry)}`).join(',')}}`
  }
  return JSON.stringify(value) ?? String(value)
}

function stableDOMFragment(value: string): string {
  let hash = 2166136261
  for (let index = 0; index < value.length; index += 1) hash = Math.imul(hash ^ value.charCodeAt(index), 16777619)
  const readable = value.replace(/[^a-zA-Z0-9_-]+/g, '-').replace(/^-+|-+$/g, '') || 'unknown'
  return `${readable}-${(hash >>> 0).toString(36)}`
}

function historyEventSummary(event: EventRecord): string {
  const title = historyEventTitle(event.type)
  const detail = historyEventDetail(event)
  return detail ? `${title} · ${detail}` : title
}

function historyEventTitle(type: string): string {
  const titles: Record<string, string> = {
    archive_discovered: 'Added to library',
    archive_changed: 'Archive updated',
    archive_unlinked: 'Source archive removed',
    archive_relinked: 'Source archive restored',
    workspace_created: 'ModMaker workspace created',
    mod_project_created: 'Mod created in ModMaker',
    workspace_exported: 'Build exported',
    virus_scan_complete: 'Virus scan completed',
    virus_scan_failed: 'Virus scan failed',
    mod_audit_local: 'Signature scan completed',
    mod_audit_pre_scan: 'Virgil file review completed',
    mod_audit_full: 'Security assessment completed',
    test_installed: 'Test build installed',
    test_removed: 'Test build removed',
    game_launched: 'BeamNG launched',
    tag_added: 'Tag assigned',
    tag_removed: 'Tag removed',
    tag_visual_updated: 'Tag appearance updated',
    library_item_details_updated: 'Details updated',
    item_details_updated: 'Details updated',
    metadata_updated: 'Details updated',
    library_variant_updated: 'Variant updated',
    variant_updated: 'Variant updated',
    variant_details_updated: 'Variant updated',
  }
  const known = titles[type]
  if (known) return known
  const readable = type.replace(/[_-]+/g, ' ').trim()
  return readable ? readable.replace(/^\w/, value => value.toUpperCase()) : 'Recorded event'
}

function historyEventDetail(event: EventRecord): string {
  const data = event.data ?? {}
  if (event.type === 'virus_scan_complete') {
    const mode = data.mode === 'full' ? 'Full scan' : 'Signature-based scan'
    const verdict = typeof data.verdict === 'string' ? data.verdict.replace(/_/g, ' ') : ''
    return [mode, verdict].filter(Boolean).join(' · ')
  }
  if (event.type === 'tag_added' || event.type === 'tag_removed' || event.type === 'tag_visual_updated') {
    const name = firstString(data, ['tagName', 'name']) || 'Unnamed tag'
    const visual = [firstString(data, ['icon']) && `Icon ${firstString(data, ['icon'])}`, firstString(data, ['color'])].filter(Boolean)
    return [name, ...visual].join(' · ')
  }
  if (event.type.includes('variant')) {
    const name = firstString(data, ['configuration', 'variantName', 'name'])
    const fields = stringArray(data.fields).join(', ')
    return [name, fields ? `Updated ${fields}` : 'Variant presentation saved'].filter(Boolean).join(' · ')
  }
  if (event.type.includes('details') || event.type === 'metadata_updated') {
    const fields = stringArray(data.fields)
    return fields.length > 0 ? `Updated ${fields.join(', ')}` : firstString(data, ['summary', 'message', 'result'])
  }
  for (const key of ['summary', 'message', 'result']) {
    if (typeof data[key] === 'string' && data[key].trim()) return data[key]
  }
  return ''
}

function firstString(data: Record<string, unknown>, keys: readonly string[]): string {
  for (const key of keys) {
    const value = data[key]
    if (typeof value === 'string' && value.trim()) return value.trim()
  }
  return ''
}

function stringArray(value: unknown): string[] {
  if (!Array.isArray(value)) return []
  return value.filter((entry): entry is string => typeof entry === 'string' && entry.trim().length > 0).map(entry => entry.trim())
}

function readableFieldLabel(value: string): string {
  const readable = value.replace(/([a-z])([A-Z])/g, '$1 $2').replace(/[_-]+/g, ' ').trim()
  return readable ? readable.replace(/^\w/, character => character.toUpperCase()) : 'Value'
}

function formatEventValue(value: unknown): string {
  if (typeof value === 'string') return value
  if (value === null) return 'null'
  if (value === undefined) return 'undefined'
  if (typeof value === 'number' || typeof value === 'boolean' || typeof value === 'bigint') return String(value)
  try {
    return JSON.stringify(value, null, 2) ?? String(value)
  } catch {
    return String(value)
  }
}

function HistoryDetail({ label, value }: { label: string; value: string }) {
  return <div className="inspector-history__detail"><dt>{label}</dt><dd>{value}</dd></div>
}
