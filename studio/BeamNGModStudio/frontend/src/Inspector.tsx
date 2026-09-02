import { useEffect, useMemo, useState } from 'react'
import type { EntityDetail, EventRecord, LibraryFolder, LibraryItem, ModTag } from '../bindings/github.com/SignedAdam/beamng-mod-studio/models.js'
import type { ArchiveMember, Variant } from '../bindings/github.com/SignedAdam/beamworlds-modkit/models.js'
import { Icon } from './icons'
import { TagEditor } from './TagEditor'
import { Badge, Button, EmptyState, Spinner, formatBytes, formatDate, issueTone, kindIcon, kindLabel } from './ui'

type InspectorTab = 'overview' | 'variants' | 'structure' | 'files' | 'history'

interface InspectorProps {
  item: LibraryItem
  detail: EntityDetail | null
  folders: LibraryFolder[]
  tags: ModTag[]
  loading: boolean
  creatingWorkspace: boolean
  onClose: () => void
  onMoveFolder: (folderID: string) => void
  onSetTags: (tagIDs: string[]) => Promise<void>
  onCreateTag: (name: string) => Promise<ModTag | null>
  onRenameTag: (tagID: string, name: string) => Promise<void>
  onDeleteTag: (tagID: string) => Promise<void>
  onCreateWorkspace: () => void
  onVirusScan: (item: LibraryItem) => void
  onError: (error: unknown) => void
}

export function Inspector({ item, detail, folders, tags, loading, creatingWorkspace, onClose, onMoveFolder, onSetTags, onCreateTag, onRenameTag, onDeleteTag, onCreateWorkspace, onVirusScan }: InspectorProps) {
  const [tab, setTab] = useState<InspectorTab>('overview')
  const [fileFilter, setFileFilter] = useState('')
  const [selectedVariant, setSelectedVariant] = useState<Variant | null>(null)
  useEffect(() => {
    setTab('overview')
    setFileFilter('')
    setSelectedVariant(null)
  }, [item.entityId])
  const manifest = detail?.item.manifest ?? item.manifest
  const variantCount = manifest?.variants?.length ?? 0
  const tabs: { id: InspectorTab; label: string }[] = [
    { id: 'overview', label: 'Overview' },
    ...(String(item.kind) === 'vehicle' && variantCount > 0 ? [{ id: 'variants' as const, label: `Variants (${variantCount})` }] : []),
    { id: 'structure', label: 'Structure' },
    { id: 'files', label: 'Files' },
    { id: 'history', label: 'History' },
  ]
  const members = useMemo(() => {
    const query = fileFilter.toLowerCase().trim()
    const filtered = (manifest?.members ?? []).filter(member => !query || member.path.toLowerCase().includes(query))
    return { total: filtered.length, visible: filtered.slice(0, 500) }
  }, [manifest, fileFilter])


  return <aside className="inspector" aria-label="Mod inspector">
    <header className="inspector__header">
      <div className="inspector__identity"><Icon name={kindIcon(String(item.kind))} size={19}/><div><h2>{item.displayName}</h2><span>{kindLabel(String(item.kind))} · <b className={`health-text health-text--${item.healthStatus || 'unscanned'}`}>{item.healthLabel || 'Not scanned'}</b>{!item.linked && ' · Source unavailable'}</span></div></div>
      <button className="icon-button" onClick={onClose} aria-label="Close inspector"><Icon name="close" size={16}/></button>
    </header>

    <div className="inspector__actions">
      <Button icon="workspace" tone="primary" disabled={!item.linked || creatingWorkspace} onClick={onCreateWorkspace}>{creatingWorkspace ? 'Opening project' : 'Open in ModMaker'}</Button>
      <Button icon="shield" disabled={!item.linked} onClick={() => onVirusScan(item)}>Scan for threats</Button>
      <label className="inspector-folder"><span>Collection</span><select value={item.folderId || ''} onChange={event => onMoveFolder(event.target.value)}><option value="">Unfiled</option>{folders.map(folder => <option key={folder.id} value={folder.id}>{folder.name}</option>)}</select></label>
    </div>

    <nav className="inspector-tabs" aria-label="Inspector sections">
      {tabs.map(value => <button key={value.id} className={tab === value.id ? 'is-active' : ''} onClick={() => setTab(value.id)}>{value.label}</button>)}
    </nav>

    <div className="inspector__body">
      {loading && !detail ? <div className="center-loader"><Spinner/><span>Loading artifact detail</span></div> : <>
        {tab === 'overview' && <Overview item={item} detail={detail} tags={tags} onSetTags={onSetTags} onCreateTag={onCreateTag} onRenameTag={onRenameTag} onDeleteTag={onDeleteTag}/>}
        {tab === 'variants' && <Variants variants={manifest?.variants ?? []} selected={selectedVariant} onSelect={setSelectedVariant}/>} 
        {tab === 'structure' && <Structure item={item}/>} 
        {tab === 'files' && <Files members={members} query={fileFilter} onQuery={setFileFilter}/>} 
        {tab === 'history' && <History detail={detail}/>} 
      </>}
    </div>
  </aside>
}

function Overview({ item, detail, tags, onSetTags, onCreateTag, onRenameTag, onDeleteTag }: { item: LibraryItem; detail: EntityDetail | null; tags: ModTag[]; onSetTags: (tagIDs: string[]) => Promise<void>; onCreateTag: (name: string) => Promise<ModTag | null>; onRenameTag: (tagID: string, name: string) => Promise<void>; onDeleteTag: (tagID: string) => Promise<void> }) {
  const manifest = detail?.item.manifest ?? item.manifest
  const issues = manifest.issues ?? []
  const health = item.healthStatus || 'unscanned'
  return <div className="inspector-section-stack">
    <div className={`health-summary health-summary--${health}`}>
      <Icon name={health === 'safe' ? 'check' : health === 'threat' || health === 'broken' || health === 'scan_failed' ? 'error' : health === 'scanning' ? 'scan' : 'shield'} size={18}/>
      <div><strong>{item.healthLabel || 'Not scanned'}</strong><span>{healthDescription(health, item.lastSecurityScanAt)}</span></div>
    </div>
    {!item.linked && <div className="source-note"><Icon name="unlink" size={16}/><span>The source archive is unavailable. Health remains based on the latest analyzed artifact.</span></div>}
    {manifest.description && <p className="inspector-description">{manifest.description}</p>}
    <TagEditor assigned={item.tags ?? []} tags={tags} onSet={onSetTags} onCreate={onCreateTag} onRename={onRenameTag} onDelete={onDeleteTag}/>
    <section>
      <h3 className="section-title">Details</h3>
      <dl className="fact-grid">
        <Fact label="Author" value={manifest.author || 'Unknown'}/>
        <Fact label="Version" value={manifest.version || 'Not declared'}/>
        <Fact label="Archive size" value={formatBytes(item.sizeBytes)}/>
        <Fact label="Files" value={(manifest.entryCount ?? item.memberCount).toLocaleString()}/>
        <Fact label="Updated" value={formatDate(manifest.analyzedAt)}/>
      </dl>
    </section>
    {issues.length > 0 && <section>
      <h3 className="section-title">Structural issues</h3>
      <div className="issue-list">{issues.map((issue, index) => <div className={`issue issue--${issue.severity}`} key={`${issue.code}-${index}`}><Icon name={issue.severity === 'error' ? 'error' : 'warning'} size={16}/><div><div><Badge tone={issueTone(issue.severity)}>{issue.severity}</Badge><strong>{issue.code}</strong></div><p>{issue.message}</p>{issue.path && <code>{issue.path}</code>}</div></div>)}</div>
    </section>}
  </div>
}

function Variants({ variants, selected, onSelect }: { variants: Variant[]; selected: Variant | null; onSelect: (variant: Variant) => void }) {
  if (variants.length === 0) return <EmptyState icon="vehicle" title="No vehicle variants" detail="This artifact does not expose matched .pc configuration records."/>
  const active = selected ?? variants[0]
  return <div className="variant-layout">
    <div className="variant-list">{variants.map(variant => <button key={variant.configPath} className={active.configPath === variant.configPath ? 'is-active' : ''} onClick={() => onSelect(variant)}><strong>{variant.configuration || variant.baseName}</strong><span>{variant.configType || variant.bodyStyle || variant.namespace}</span></button>)}</div>
    <section className="variant-detail">
      <div className="variant-detail__title"><div><p>{active.namespace}</p><h3>{active.configuration || active.baseName}</h3></div>{active.configType && <Badge tone="accent">{active.configType}</Badge>}</div>
      {active.description && <p className="variant-description">{active.description}</p>}
      <dl className="fact-grid fact-grid--compact">
        <Fact label="Body" value={active.bodyStyle || '—'}/><Fact label="Drivetrain" value={active.drivetrain || '—'}/>
        <Fact label="Transmission" value={active.transmission || '—'}/><Fact label="Fuel" value={active.fuelType || '—'}/>
        <Fact label="Propulsion" value={active.propulsion || '—'}/><Fact label="Power" value={active.power ? String(active.power) : '—'}/>
        <Fact label="Torque" value={active.torque ? String(active.torque) : '—'}/><Fact label="Weight" value={active.weight ? String(active.weight) : '—'}/>
        <Fact label="Value" value={active.value ? String(active.value) : '—'}/><Fact label="Top speed" value={active.topSpeed ? String(active.topSpeed) : '—'}/>
      </dl>
      <div className="path-stack"><code>{active.configPath}</code>{active.metadataPath && <code>{active.metadataPath}</code>}{active.thumbnailPath && <code>{active.thumbnailPath}</code>}</div>
      {active.fields && Object.keys(active.fields).length > 0 && <details className="raw-details"><summary>All metadata fields</summary><pre>{JSON.stringify(active.fields, null, 2)}</pre></details>}
    </section>
  </div>
}

function Structure({ item }: { item: LibraryItem }) {
  const manifest = item.manifest
  const namespaceEntries = Object.entries(manifest.namespaces ?? {}) as [string, string[]][]
  const levelIDs = manifest.map.levelIds ?? []
  const controllers = manifest.jbeam.controllers ?? []
  const metadataDocuments = manifest.metadataDocuments ?? []
  return <div className="inspector-section-stack">
    <section><h3 className="section-title">Namespaces</h3><div className="namespace-list">{namespaceEntries.map(([root, values]) => <div key={root}><strong>{root}</strong><div>{values.length ? values.map(value => <code key={value}>{value}</code>) : <span className="muted">None</span>}</div></div>)}</div></section>
    {(manifest.jbeam?.files ?? 0) > 0 && <MetricSection title="JBeam model" metrics={{ Files: manifest.jbeam.files, Parsed: manifest.jbeam.parsedFiles, Nodes: manifest.jbeam.declaredNodes, Beams: manifest.jbeam.declaredBeams, Triangles: manifest.jbeam.declaredTriangles, Slots: manifest.jbeam.declaredSlots, Hydros: manifest.jbeam.declaredHydros }}/>} 
    {levelIDs.length > 0 && <MetricSection title="Level model" metrics={{ Levels: levelIDs.length, Terrain: manifest.map.terrainFiles, 'Object files': manifest.map.levelObjectFiles, Forests: manifest.map.forestFiles, Facilities: manifest.map.facilityFiles, Materials: manifest.map.materialFiles, Models: manifest.map.modelFiles, Textures: manifest.map.textureFiles, Spawns: manifest.map.spawnPoints }}/>} 
    {((manifest.ui?.appRoots?.length ?? 0) > 0 || (manifest.ui?.luaFiles ?? 0) > 0) && <MetricSection title="UI and script model" metrics={{ Apps: manifest.ui.appRoots?.length ?? 0, HTML: manifest.ui.htmlFiles, CSS: manifest.ui.cssFiles, JavaScript: manifest.ui.javaScriptFiles, Lua: manifest.ui.luaFiles, Settings: manifest.ui.settingsFiles, Scripts: manifest.ui.scriptFiles }}/>} 
    {controllers.length > 0 && <section><h3 className="section-title">Controller references</h3><div className="code-list">{controllers.map(value => <code key={value}>{value}</code>)}</div></section>}
    {metadataDocuments.length > 0 && <section><h3 className="section-title">Metadata documents</h3>{metadataDocuments.map(document => <details className="raw-details" key={document.path}><summary>{document.path}</summary><pre>{JSON.stringify(document.data, null, 2)}</pre></details>)}</section>}
  </div>
}

function Files({ members, query, onQuery }: { members: { total: number; visible: ArchiveMember[] }; query: string; onQuery: (value: string) => void }) {
  return <div className="file-inventory">
    <label className="search-box search-box--wide"><Icon name="search" size={15}/><input value={query} onChange={event => onQuery(event.target.value)} placeholder="Filter archive members"/></label>
    <p className="inventory-count">Showing {members.visible.length.toLocaleString()} of {members.total.toLocaleString()} matching entries</p>
    <div className="inventory-table"><div className="inventory-table__head"><span>Path</span><span>Expanded</span><span>Method</span></div>{members.visible.map(member => <div className="inventory-table__row" key={`${member.path}-${member.crc32}`}><span title={member.path}><Icon name={member.directory ? 'folder' : 'files'} size={13}/>{member.path}</span><span>{member.directory ? '—' : formatBytes(member.uncompressedBytes)}</span><span>{member.method === 0 ? 'Store' : member.method === 8 ? 'Deflate' : String(member.method)}</span></div>)}</div>
  </div>
}

function History({ detail }: { detail: EntityDetail | null }) {
  if (!detail) return <div className="center-loader"><Spinner/><span>Loading history</span></div>
  const history = detail.history ?? []
  return <section className="entity-history">
    <header><h3>History</h3><span>{history.length.toLocaleString()} events</span></header>
    {history.length === 0 ? <p className="muted">No activity recorded.</p> : <div className="history-list">{history.map(event => {
      const detailText = historyEventDetail(event)
      return <article key={event.id}><time>{formatDate(event.at)}</time><div><strong>{historyEventTitle(event.type)}</strong>{detailText && <p>{detailText}</p>}</div></article>
    })}</div>}
  </section>
}


function healthDescription(status: string, scannedAt: string) {
  const when = scannedAt ? ` Latest scan ${formatDate(scannedAt)}.` : ''
  switch (status) {
  case 'safe':
    return `No threat signals found.${when}`
  case 'review':
    return `The latest scan found items that need review.${when}`
  case 'threat':
    return `The latest scan found a high-risk threat.${when}`
  case 'broken':
    return 'Structural errors prevent this mod from loading cleanly.'
  case 'scanning':
    return 'A security scan is currently running.'
  case 'scan_failed':
    return `The latest scan did not complete.${when}`
  default:
    return 'Run a signature-based or full scan to establish health.'
  }
}

function historyEventTitle(type: string) {
  const titles: Record<string, string> = {
    archive_discovered: 'Added to library',
    archive_changed: 'Archive updated',
    archive_unlinked: 'Source archive removed',
    archive_relinked: 'Source archive restored',
    workspace_created: 'ModMaker project created',
    mod_project_created: 'ModMaker project created',
    workspace_exported: 'Build exported',
    virus_scan_complete: 'Virus scan completed',
    virus_scan_failed: 'Virus scan failed',
    mod_audit_local: 'Signature scan completed',
    mod_audit_pre_scan: 'Virgil file review completed',
    mod_audit_full: 'Security assessment completed',
    test_installed: 'Test build installed',
    test_removed: 'Test build removed',
    game_launched: 'BeamNG launched',
  }
  return titles[type] ?? type.replace(/_/g, ' ').replace(/^\w/, value => value.toUpperCase())
}

function historyEventDetail(event: EventRecord) {
  const data = event.data ?? {}
  if (event.type === 'virus_scan_complete') {
    const mode = data.mode === 'full' ? 'Full scan' : 'Signature-based scan'
    const verdict = typeof data.verdict === 'string' ? data.verdict.replace(/_/g, ' ') : ''
    return [mode, verdict].filter(Boolean).join(' · ')
  }
  for (const key of ['summary', 'message', 'result']) {
    if (typeof data[key] === 'string' && data[key].trim()) return data[key]
  }
  return ''
}

function Fact({ label, value }: { label: string; value: string }) {
  return <div><dt>{label}</dt><dd>{value}</dd></div>
}

function MetricSection({ title, metrics }: { title: string; metrics: Record<string, number> }) {
  return <section><h3 className="section-title">{title}</h3><div className="metric-grid">{Object.entries(metrics).map(([label, value]) => <div key={label}><strong>{value.toLocaleString()}</strong><span>{label}</span></div>)}</div></section>
}
