import { useEffect, useMemo, useState } from 'react'
import type { EntityDetail, LibraryFolder, LibraryItem } from '../bindings/github.com/SignedAdam/beamng-mod-studio/models.js'
import type { ArchiveMember, Variant } from '../bindings/github.com/SignedAdam/beamworlds-modkit/models.js'
import { Icon } from './icons'
import { Badge, Button, EmptyState, Spinner, formatBytes, formatDate, issueTone, kindIcon, kindLabel } from './ui'

type InspectorTab = 'overview' | 'variants' | 'structure' | 'files' | 'history'

interface InspectorProps {
  item: LibraryItem
  detail: EntityDetail | null
  folders: LibraryFolder[]
  loading: boolean
  creatingWorkspace: boolean
  onClose: () => void
  onMoveFolder: (folderID: string) => void
  onCreateWorkspace: (mode: 'editor' | 'virgil') => void
}

export function Inspector({ item, detail, folders, loading, creatingWorkspace, onClose, onMoveFolder, onCreateWorkspace }: InspectorProps) {
  const [tab, setTab] = useState<InspectorTab>('overview')
  const [fileFilter, setFileFilter] = useState('')
  const [selectedVariant, setSelectedVariant] = useState<Variant | null>(null)
  useEffect(() => {
    setTab('overview')
    setFileFilter('')
    setSelectedVariant(null)
  }, [item.entityId])
  const manifest = detail?.item.manifest ?? item.manifest
  const members = useMemo(() => {
    const query = fileFilter.toLowerCase().trim()
    const filtered = (manifest?.members ?? []).filter(member => !query || member.path.toLowerCase().includes(query))
    return { total: filtered.length, visible: filtered.slice(0, 500) }
  }, [manifest, fileFilter])

  return <aside className="inspector" aria-label="Mod inspector">
    <header className="inspector__header">
      <div className="inspector__identity"><Icon name={kindIcon(String(item.kind))} size={18}/><div><h2>{item.displayName}</h2><span>{kindLabel(String(item.kind))} · {item.linked ? 'Linked' : 'Missing source'}</span></div></div>
      <button className="icon-button" onClick={onClose} aria-label="Close inspector"><Icon name="close" size={16}/></button>
    </header>

    <div className="inspector__actions">
      <Button icon="edit" tone="primary" disabled={!item.linked || creatingWorkspace} onClick={() => onCreateWorkspace('editor')}>{creatingWorkspace ? 'Opening project' : 'Edit in ModMaker'}</Button>
      <Button icon="agent" disabled={!item.linked || creatingWorkspace} onClick={() => onCreateWorkspace('virgil')}>Work with Virgil</Button>
      <label className="inspector-folder"><span>Library folder</span><select value={item.folderId || ''} onChange={event => onMoveFolder(event.target.value)}><option value="">Unfiled</option>{folders.map(folder => <option key={folder.id} value={folder.id}>{folder.name}</option>)}</select></label>
      <code title={item.fingerprint}>SHA {item.fingerprint?.slice(0, 12) || 'pending'}</code>
    </div>

    <nav className="glass-tabs glass-tabs--inspector">
      {(['overview', 'variants', 'structure', 'files', 'history'] as InspectorTab[]).map(value => <button key={value} className={tab === value ? 'is-active' : ''} onClick={() => setTab(value)}>{value === 'variants' ? `Variants ${manifest?.variants?.length ? `(${manifest.variants.length})` : ''}` : value.charAt(0).toUpperCase() + value.slice(1)}</button>)}
    </nav>

    <div className="inspector__body">
      {loading && !detail ? <div className="center-loader"><Spinner/><span>Loading artifact detail</span></div> : <>
        {tab === 'overview' && <Overview item={item} detail={detail} />}
        {tab === 'variants' && <Variants variants={manifest?.variants ?? []} selected={selectedVariant} onSelect={setSelectedVariant}/>} 
        {tab === 'structure' && <Structure item={item}/>} 
        {tab === 'files' && <Files members={members} query={fileFilter} onQuery={setFileFilter}/>} 
        {tab === 'history' && <History detail={detail}/>} 
      </>}
    </div>
  </aside>
}

function Overview({ item, detail }: { item: LibraryItem; detail: EntityDetail | null }) {
  const manifest = detail?.item.manifest ?? item.manifest
  const contentTags = manifest.contentTags ?? []
  const issues = manifest.issues ?? []
  return <div className="inspector-section-stack">
    {manifest.description && <p className="inspector-description">{manifest.description}</p>}
    <section>
      <h3 className="section-title">Artifact facts</h3>
      <dl className="fact-grid">
        <Fact label="Author" value={manifest.author || 'Unknown'}/>
        <Fact label="Version" value={manifest.version || 'Not declared'}/>
        <Fact label="Archive size" value={formatBytes(item.sizeBytes)}/>
        <Fact label="Entries" value={(manifest.entryCount ?? item.memberCount).toLocaleString()}/>
        <Fact label="Compressed" value={formatBytes(manifest.compressedBytes)}/>
        <Fact label="Expanded" value={formatBytes(manifest.uncompressedBytes)}/>
        <Fact label="Wrapper" value={manifest.wrapper || 'None'}/>
        <Fact label="Analyzed" value={formatDate(manifest.analyzedAt)}/>
      </dl>
    </section>
    <section>
      <h3 className="section-title">Content</h3>
      <div className="chip-row">{contentTags.length > 0 ? contentTags.map(tag => <Badge key={tag} tone="accent">{tag}</Badge>) : <span className="muted">No content tags inferred</span>}</div>
    </section>
    <section>
      <h3 className="section-title">Source archive</h3>
      <div className="path-card"><Icon name={item.linked ? 'link' : 'unlink'} size={16}/><span>{item.archivePath || 'No linked path'}</span></div>
      <dl className="identity-list"><div><dt>Entity</dt><dd>{item.entityId}</dd></div><div><dt>Artifact</dt><dd>{item.artifactId}</dd></div><div><dt>Fingerprint</dt><dd>{item.fingerprint}</dd></div>{item.sha256 && <div><dt>SHA-256</dt><dd>{item.sha256}</dd></div>}</dl>
    </section>
    <section>
      <h3 className="section-title">Analysis issues</h3>
      {issues.length === 0 ? <div className="success-line"><Icon name="check" size={16}/>No structural issues reported</div> : <div className="issue-list">{issues.map((issue, index) => <div className={`issue issue--${issue.severity}`} key={`${issue.code}-${index}`}><Icon name={issue.severity === 'error' ? 'error' : 'warning'} size={16}/><div><div><Badge tone={issueTone(issue.severity)}>{issue.severity}</Badge><strong>{issue.code}</strong></div><p>{issue.message}</p>{issue.path && <code>{issue.path}</code>}</div></div>)}</div>}
    </section>
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
  if (!detail) return <div className="center-loader"><Spinner/><span>Loading archive links</span></div>
  const links = detail.links ?? []
  const history = detail.history ?? []
  return <div className="inspector-section-stack">
    <section><h3 className="section-title">Archive links</h3><div className="link-list">{links.map(link => <div key={link.id}><Icon name={link.linked ? 'link' : 'unlink'} size={15}/><div><strong>{link.path}</strong><span>{link.linked ? 'Linked' : 'Unlinked'} · last seen {formatDate(link.lastSeenAt)}</span></div></div>)}</div></section>
    <section><h3 className="section-title">Entity history</h3>{history.length === 0 ? <p className="muted">No recorded transitions.</p> : <div className="timeline">{history.map(event => <div className="timeline__item" key={event.id}><span className="timeline__dot"/><div><strong>{event.type.replace(/_/g, ' ')}</strong><time>{formatDate(event.at)}</time>{event.data && <p>{Object.values(event.data).filter(value => typeof value === 'string').join(' · ')}</p>}</div></div>)}</div>}</section>
  </div>
}

function Fact({ label, value }: { label: string; value: string }) {
  return <div><dt>{label}</dt><dd>{value}</dd></div>
}

function MetricSection({ title, metrics }: { title: string; metrics: Record<string, number> }) {
  return <section><h3 className="section-title">{title}</h3><div className="metric-grid">{Object.entries(metrics).map(([label, value]) => <div key={label}><strong>{value.toLocaleString()}</strong><span>{label}</span></div>)}</div></section>
}
