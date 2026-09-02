import { useEffect, useMemo, useState } from 'react'
import { AppService as API } from '../bindings/github.com/SignedAdam/beamng-mod-studio/index.js'
import type { EntityDetail, LibraryFolder, LibraryItem, ModAudit, ModTag } from '../bindings/github.com/SignedAdam/beamng-mod-studio/models.js'
import type { ArchiveMember, Variant } from '../bindings/github.com/SignedAdam/beamworlds-modkit/models.js'
import { Icon } from './icons'
import { TagEditor } from './TagEditor'
import { Badge, Button, EmptyState, Spinner, formatBytes, formatDate, issueTone, kindIcon, kindLabel } from './ui'

type InspectorTab = 'overview' | 'variants' | 'structure' | 'files' | 'history' | 'audit'

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
  onCreateWorkspace: (mode: 'editor' | 'virgil') => void
  onError: (error: unknown) => void
}

export function Inspector({ item, detail, folders, tags, loading, creatingWorkspace, onClose, onMoveFolder, onSetTags, onCreateTag, onRenameTag, onDeleteTag, onCreateWorkspace, onError }: InspectorProps) {
  const [tab, setTab] = useState<InspectorTab>('overview')
  const [fileFilter, setFileFilter] = useState('')
  const [selectedVariant, setSelectedVariant] = useState<Variant | null>(null)
  const [audit, setAudit] = useState<ModAudit | null>(null)
  const [auditLoading, setAuditLoading] = useState(false)
  const [auditBusy, setAuditBusy] = useState('')
  const [auditError, setAuditError] = useState('')
  const [followUpPaths, setFollowUpPaths] = useState('')
  const [followUpQuestion, setFollowUpQuestion] = useState('')
  useEffect(() => {
    setTab('overview')
    setFileFilter('')
    setSelectedVariant(null)
    setAudit(null)
    setAuditError('')
    setFollowUpPaths('')
    setFollowUpQuestion('')
  }, [item.entityId])
  useEffect(() => {
    if (tab !== 'audit' || audit) return
    let active = true
    setAuditLoading(true)
    API.GetModAudit(item.entityId).then(value => {
      if (!active) return
      setAudit(value)
      setAuditError(value.error || '')
      if (value.final?.followUpPaths?.length) setFollowUpPaths(value.final.followUpPaths.join(', '))
    }).catch(error => {
      if (active) {
        setAuditError(auditErrorText(error))
        onError(error)
      }
    }).finally(() => {
      if (active) setAuditLoading(false)
    })
    return () => { active = false }
  }, [tab, item.entityId, audit, onError])
  const manifest = detail?.item.manifest ?? item.manifest
  const members = useMemo(() => {
    const query = fileFilter.toLowerCase().trim()
    const filtered = (manifest?.members ?? []).filter(member => !query || member.path.toLowerCase().includes(query))
    return { total: filtered.length, visible: filtered.slice(0, 500) }
  }, [manifest, fileFilter])

  const runAuditStage = async (stage: 'local' | 'pre' | 'full') => {
    setAuditBusy(stage)
    setAuditError('')
    try {
      const next = stage === 'local' ? await API.RunModAuditLocal(item.entityId) : stage === 'pre' ? await API.RunModAuditPreScan(item.entityId) : await API.RunModAuditFull(item.entityId)
      setAudit(next)
      setAuditError(next.error || '')
      if (next.final?.followUpPaths?.length) setFollowUpPaths(next.final.followUpPaths.join(', '))
    } catch (error) {
      setAuditError(auditErrorText(error))
      onError(error)
      try { setAudit(await API.GetModAudit(item.entityId)) } catch { /* preserve the original stage error */ }
    } finally {
      setAuditBusy('')
    }
  }

  const runAuditFollowUp = async () => {
    if (!followUpQuestion.trim()) return
    setAuditBusy('follow-up')
    setAuditError('')
    try {
      const paths = followUpPaths.split(',').map(value => value.trim()).filter(Boolean)
      const next = await API.FollowUpModAudit(item.entityId, paths, followUpQuestion.trim())
      setAudit(next)
      setFollowUpQuestion('')
    } catch (error) {
      setAuditError(auditErrorText(error))
      onError(error)
    } finally {
      setAuditBusy('')
    }
  }

  return <aside className="inspector" aria-label="Mod inspector">
    <header className="inspector__header">
      <div className="inspector__identity"><Icon name={kindIcon(String(item.kind))} size={18}/><div><h2>{item.displayName}</h2><span>{kindLabel(String(item.kind))} · {item.linked ? 'Source available' : 'Missing source'}</span></div></div>
      <button className="icon-button" onClick={onClose} aria-label="Close inspector"><Icon name="close" size={16}/></button>
    </header>

    <div className="inspector__actions">
      <Button icon="edit" tone="primary" disabled={!item.linked || creatingWorkspace} onClick={() => onCreateWorkspace('editor')}>{creatingWorkspace ? 'Opening project' : 'Edit in ModMaker'}</Button>
      <Button icon="agent" disabled={!item.linked || creatingWorkspace} onClick={() => onCreateWorkspace('virgil')}>Work with Virgil</Button>
      <label className="inspector-folder"><span>Collection</span><select value={item.folderId || ''} onChange={event => onMoveFolder(event.target.value)}><option value="">Unfiled</option>{folders.map(folder => <option key={folder.id} value={folder.id}>{folder.name}</option>)}</select></label>
      <code title={item.fingerprint}>SHA {item.fingerprint?.slice(0, 12) || 'pending'}</code>
    </div>

    <nav className="glass-tabs glass-tabs--inspector">
      {(['overview', 'variants', 'structure', 'files', 'history', 'audit'] as InspectorTab[]).map(value => <button key={value} className={tab === value ? 'is-active' : ''} onClick={() => setTab(value)}>{value === 'variants' ? `Variants ${manifest?.variants?.length ? `(${manifest.variants.length})` : ''}` : value === 'audit' ? 'Mod Audit' : value.charAt(0).toUpperCase() + value.slice(1)}</button>)}
    </nav>

    <div className="inspector__body">
      {loading && !detail ? <div className="center-loader"><Spinner/><span>Loading artifact detail</span></div> : <>
        {tab === 'overview' && <Overview item={item} detail={detail} tags={tags} onSetTags={onSetTags} onCreateTag={onCreateTag} onRenameTag={onRenameTag} onDeleteTag={onDeleteTag}/>}
        {tab === 'variants' && <Variants variants={manifest?.variants ?? []} selected={selectedVariant} onSelect={setSelectedVariant}/>} 
        {tab === 'structure' && <Structure item={item}/>} 
        {tab === 'files' && <Files members={members} query={fileFilter} onQuery={setFileFilter}/>} 
        {tab === 'history' && <History detail={detail}/>} 
        {tab === 'audit' && <ModAuditPanel audit={audit} linked={item.linked} loading={auditLoading} busy={auditBusy} error={auditError} followUpPaths={followUpPaths} followUpQuestion={followUpQuestion} onPaths={setFollowUpPaths} onQuestion={setFollowUpQuestion} onRun={stage => void runAuditStage(stage)} onFollowUp={() => void runAuditFollowUp()}/>}
      </>}
    </div>
  </aside>
}

function Overview({ item, detail, tags, onSetTags, onCreateTag, onRenameTag, onDeleteTag }: { item: LibraryItem; detail: EntityDetail | null; tags: ModTag[]; onSetTags: (tagIDs: string[]) => Promise<void>; onCreateTag: (name: string) => Promise<ModTag | null>; onRenameTag: (tagID: string, name: string) => Promise<void>; onDeleteTag: (tagID: string) => Promise<void> }) {
  const manifest = detail?.item.manifest ?? item.manifest
  const contentTags = manifest.contentTags ?? []
  const issues = manifest.issues ?? []
  return <div className="inspector-section-stack">
    {manifest.description && <p className="inspector-description">{manifest.description}</p>}
    <TagEditor assigned={item.tags ?? []} tags={tags} onSet={onSetTags} onCreate={onCreateTag} onRename={onRenameTag} onDelete={onDeleteTag}/>
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

function ModAuditPanel({ audit, linked, loading, busy, error, followUpPaths, followUpQuestion, onPaths, onQuestion, onRun, onFollowUp }: {
  audit: ModAudit | null
  linked: boolean
  loading: boolean
  busy: string
  error: string
  followUpPaths: string
  followUpQuestion: string
  onPaths: (value: string) => void
  onQuestion: (value: string) => void
  onRun: (stage: 'local' | 'pre' | 'full') => void
  onFollowUp: () => void
}) {
  if (loading) return <div className="center-loader"><Spinner/><span>Loading Mod Audit</span></div>
  const hasLocal = Boolean(audit?.id)
  const hasPreScan = Boolean(audit?.preScan?.model)
  const hasFinal = Boolean(audit?.final?.model)
  const local = audit?.deterministic
  const surface = audit?.attackSurface
  const localSignals = local?.signals ?? []
  const surfaceEntries = surface?.entries ?? []
  const preScanFiles = audit?.preScan?.files ?? []
  const focusedPaths = audit?.final?.focusedPaths ?? []
  const auditWarnings = audit?.final?.warnings ?? []
  const followUps = audit?.followUps ?? []
  return <div className="mod-audit">
    <section className="mod-audit__intro">
      <div><Icon name="shield" size={20}/><div><span>MOD AUDIT</span><strong>Staged security analysis</strong></div></div>
      <p>Local checks identify payloads and entrypoints. AI receives inert excerpts and bounded raster attachments; archive content is never executed.</p>
      {audit && audit.status !== 'not_started' && <div className="mod-audit__status"><Badge tone={audit.status === 'failed' ? 'danger' : audit.status === 'complete' ? 'success' : 'neutral'}>{audit.status.replace(/_/g, ' ')}</Badge><span>{audit.updatedAt ? `Updated ${formatDate(audit.updatedAt)}` : ''}</span></div>}
      {error && <div className="mod-audit__error"><Icon name="error" size={15}/><span>{error}</span></div>}
    </section>

    <section className="audit-stages">
      <article><header><span>1</span><div><strong>Local scan</strong><small>No AI · no execution</small></div></header><p>Executable signatures, suspicious primitives, archive safety, and content signals.</p><Button icon="scan" disabled={!linked || busy !== ''} onClick={() => onRun('local')}>{busy === 'local' ? 'Scanning' : hasLocal ? 'Run again' : 'Run local scan'}</Button></article>
      <article><header><span>2</span><div><strong>AI pre-scan</strong><small>Cheap model · no verdict</small></div></header><p>Records per-file observations for reuse by the final review.</p><Button icon="agent" disabled={!linked || busy !== ''} onClick={() => onRun('pre')}>{busy === 'pre' ? 'Analyzing' : hasPreScan ? 'Run again' : 'Run pre-scan'}</Button></article>
      <article><header><span>3</span><div><strong>Full analysis</strong><small>Strong model · focused follow-up</small></div></header><p>Produces evidence-backed findings, visually reviews supported images, and requests larger excerpts when needed.</p><Button icon="shield" tone="primary" disabled={!linked || busy !== ''} onClick={() => onRun('full')}>{busy === 'full' ? 'Reviewing' : hasFinal ? 'Run again' : 'Run full audit'}</Button></article>
    </section>

    {hasLocal && local && <>
      <section>
        <h3 className="section-title">Local evidence</h3>
        <div className="audit-metrics"><div><strong>{local.scannedEntries.toLocaleString()}</strong><span>Entries</span></div><div><strong>{local.candidateFiles.toLocaleString()}</strong><span>Candidate files</span></div><div><strong>{local.executableFiles.toLocaleString()}</strong><span>Executables</span></div><div><strong>{local.suspiciousFiles.toLocaleString()}</strong><span>Flagged files</span></div></div>
        {local.truncated && <p className="audit-note">The bounded scan reached its safety limit. Review the saved candidates before relying on absence of a signal.</p>}
        {localSignals.length === 0 ? <div className="success-line"><Icon name="check" size={15}/>No deterministic signals found</div> : <div className="audit-signal-list">{localSignals.slice(0, 120).map((signal, index) => <article key={`${signal.path}-${signal.code}-${index}`}><Badge tone={auditFindingTone(signal.severity)}>{signal.severity}</Badge><div><strong>{signal.detail}</strong><code>{signal.path}</code><p>{signal.evidence}</p></div></article>)}</div>}
      </section>
      <section>
        <h3 className="section-title">Attack surface</h3>
        <p className="audit-note">Path patterns are compared with {surface?.libraryMods?.toLocaleString() ?? 0} mods already indexed in this library.</p>
        {surfaceEntries.length === 0 ? <p className="muted">No executable entrypoints mapped.</p> : <div className="audit-surface-list">{surfaceEntries.slice(0, 120).map(entry => <div key={entry.path}><Icon name={entry.novel ? 'warning' : 'code'} size={14}/><div><strong>{entry.type.replace(/-/g, ' ')}</strong><code>{entry.path}</code><span>{entry.reason} · seen in {entry.libraryOccurrences.toLocaleString()} library mods</span></div>{entry.novel && <Badge tone="warning">Uncommon</Badge>}</div>)}</div>}
      </section>
    </>}

    {hasPreScan && audit && <section>
      <h3 className="section-title">AI pre-scan artifacts</h3>
      <div className="audit-model-line"><Badge tone="cyan">{audit.preScan.model}</Badge><span>{audit.preScan.reasoning} reasoning · observations only, no verdict</span></div>
      {audit.preScan.summary && <p className="audit-summary">{audit.preScan.summary}</p>}
      <div className="audit-file-analysis">{preScanFiles.map(file => {
        const observations = file.observations ?? []
        const behaviors = file.behaviors ?? []
        const questions = file.followUp ?? []
        return <details key={file.path}><summary><code>{file.path}</code><span>{observations.length + behaviors.length}</span></summary><div>{observations.length > 0 && <><strong>Observations</strong><ul>{observations.map((value, index) => <li key={index}>{value}</li>)}</ul></>}{behaviors.length > 0 && <><strong>Behaviors</strong><ul>{behaviors.map((value, index) => <li key={index}>{value}</li>)}</ul></>}{questions.length > 0 && <><strong>Follow-up</strong><ul>{questions.map((value, index) => <li key={index}>{value}</li>)}</ul></>}</div></details>
      })}</div>
    </section>}

    {hasFinal && audit && <section>
      <h3 className="section-title">Final security review</h3>
      <div className="audit-verdict"><Badge tone={auditFindingTone(audit.final.overallRisk)}>{audit.final.overallRisk} risk</Badge><span>{audit.final.model} · {audit.final.reasoning} reasoning</span></div>
      <p className="audit-summary">{audit.final.summary || 'No structured summary returned.'}</p>
      {focusedPaths.length > 0 && <p className="audit-note">Focused inspection used {focusedPaths.length.toLocaleString()} requested file excerpts.</p>}
      <AuditFindings title="Technical findings" findings={audit.final.findings}/>
      <AuditFindings title="Harmful content signals" findings={audit.final.contentSignals}/>
      {auditWarnings.map((warning, index) => <div className="mod-audit__error" key={index}><Icon name="warning" size={14}/><span>{warning}</span></div>)}
      <div className="audit-follow-up">
        <strong>Focused follow-up</strong>
        <label><span>Files · comma separated</span><input value={followUpPaths} onChange={event => onPaths(event.target.value)} placeholder="lua/ge/extensions/example.lua"/></label>
        <label><span>Question</span><textarea value={followUpQuestion} onChange={event => onQuestion(event.target.value)} placeholder="What exact input reaches the process-launch call?"/></label>
        <Button icon="agent" disabled={busy !== '' || !followUpQuestion.trim()} onClick={onFollowUp}>{busy === 'follow-up' ? 'Inspecting' : 'Ask strong model'}</Button>
      </div>
      {followUps.length > 0 && <div className="audit-follow-up-history">{followUps.map((followUp, index) => <details key={`${followUp.createdAt}-${index}`}><summary><span>{followUp.question}</span><time>{formatDate(followUp.createdAt)}</time></summary><div><code>{(followUp.paths ?? []).join(', ')}</code><p>{followUp.response}</p></div></details>)}</div>}
    </section>}
  </div>
}

function AuditFindings({ title, findings }: { title: string; findings: ModAudit['final']['findings'] }) {
  const rows = findings ?? []
  return <div className="audit-findings"><strong>{title}</strong>{rows.length === 0 ? <p className="muted">None reported.</p> : rows.map((finding, index) => <article key={`${finding.path}-${finding.title}-${index}`}><Badge tone={auditFindingTone(finding.severity)}>{finding.severity}</Badge><div><strong>{finding.title}</strong>{finding.path && <code>{finding.path}</code>}<p>{finding.evidence}</p>{finding.impact && <span>Impact · {finding.impact}</span>}{finding.recommendation && <span>Action · {finding.recommendation}</span>}</div></article>)}</div>
}

function auditFindingTone(value: string): 'neutral' | 'warning' | 'danger' | 'cyan' {
  switch (value.toLowerCase()) {
  case 'critical':
  case 'high':
    return 'danger'
  case 'moderate':
  case 'medium':
    return 'warning'
  case 'low':
    return 'cyan'
  default:
    return 'neutral'
  }
}

function auditErrorText(error: unknown) {
  return error instanceof Error ? error.message : String(error)
}

function Fact({ label, value }: { label: string; value: string }) {
  return <div><dt>{label}</dt><dd>{value}</dd></div>
}

function MetricSection({ title, metrics }: { title: string; metrics: Record<string, number> }) {
  return <section><h3 className="section-title">{title}</h3><div className="metric-grid">{Object.entries(metrics).map(([label, value]) => <div key={label}><strong>{value.toLocaleString()}</strong><span>{label}</span></div>)}</div></section>
}
