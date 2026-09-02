import { useEffect, useMemo, useRef, useState } from 'react'
import type {
  ArchiveMemberPreview,
  EntityDetail,
  EventRecord,
  LibraryFolder,
  LibraryItem,
  LibraryItemDetailsUpdate,
  LibraryVariantUpdate,
  ModTag,
} from '../bindings/github.com/SignedAdam/beamng-mod-studio/models.js'
import type { ArchiveMember, Variant } from '../bindings/github.com/SignedAdam/beamworlds-modkit/models.js'
import { Icon } from './icons'
import { TagEditor } from './TagEditor'
import { Badge, Button, EmptyState, Spinner, formatBytes, formatDate, issueTone, kindIcon, kindLabel } from './ui'

type InspectorTab = 'overview' | 'variants' | 'issues' | 'structure' | 'files' | 'history'

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
  onCreateTag: (name: string, color: string, icon: string) => Promise<ModTag | null>
  onUpdateTagVisual: (tagID: string, color: string, icon: string) => Promise<void>
  onRenameTag: (tagID: string, name: string) => Promise<void>
  onDeleteTag: (tagID: string) => Promise<void>
  onCreateWorkspace: () => void
  onVirusScan: (item: LibraryItem) => void
  onSaveDetails: (update: LibraryItemDetailsUpdate) => Promise<void>
  onSaveVariant: (update: LibraryVariantUpdate) => Promise<void>
  onPreviewMember: (memberPath: string) => Promise<ArchiveMemberPreview | null>
  onExtractMember: (memberPath: string) => Promise<void>
  onRevealArchive: () => Promise<void>
  onError: (error: unknown) => void
}

export function Inspector({
  item,
  detail,
  folders,
  tags,
  loading,
  creatingWorkspace,
  onClose,
  onMoveFolder,
  onSetTags,
  onCreateTag,
  onUpdateTagVisual,
  onRenameTag,
  onDeleteTag,
  onCreateWorkspace,
  onVirusScan,
  onSaveDetails,
  onSaveVariant,
  onPreviewMember,
  onExtractMember,
  onRevealArchive,
  onError,
}: InspectorProps) {
  const [tab, setTab] = useState<InspectorTab>('overview')
  const [fileFilter, setFileFilter] = useState('')
  const [selectedVariant, setSelectedVariant] = useState<Variant | null>(null)

  useEffect(() => {
    setTab('overview')
    setFileFilter('')
    setSelectedVariant(null)
  }, [item.entityId])

  const currentDetail = detail?.item.entityId === item.entityId ? detail : null
  const manifest = currentDetail?.item.manifest ?? item.manifest
  const variantCount = (manifest?.variants ?? []).length
  const tabs: { id: InspectorTab; label: string }[] = [
    { id: 'overview', label: 'Overview' },
    ...(String(item.kind) === 'vehicle' && variantCount > 0 ? [{ id: 'variants' as const, label: `Variants (${variantCount})` }] : []),
    { id: 'issues', label: 'Structural Issues' },
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
      <div className="inspector__identity">
        <Icon name={kindIcon(String(item.kind))} size={19}/>
        <div>
          <h2>{item.displayName}</h2>
          <span>{kindLabel(String(item.kind))}{!item.linked && ' · Source unavailable'}</span>
        </div>
      </div>
      <button className="icon-button" onClick={onClose} aria-label="Close inspector"><Icon name="close" size={16}/></button>
    </header>

    <div className="inspector__actions">
      <Button className="inspector__maker" icon="workspace" tone="quiet" disabled={!item.linked || creatingWorkspace} onClick={onCreateWorkspace}>
        {creatingWorkspace ? 'Opening project' : 'Open in ModMaker'}
      </Button>
      <button type="button" className={`inspector__scan-control inspector__scan-control--${item.healthStatus || 'unscanned'}`} disabled={!item.linked} onClick={() => onVirusScan(item)}>
        <span className="inspector__scan-action"><Icon name="shield" size={14}/><strong>Scan for threats</strong></span>
        <span className="inspector__scan-status" aria-live="polite">
          <Icon name={scanStatusIcon(item.healthStatus)} size={13}/>
          <span>{securityScanStatus(item)}</span>
          {item.securityScanChanged && <em>Scan again?</em>}
        </span>
      </button>
      <label className="inspector-folder">
        <span>Collection</span>
        <select value={item.folderId || ''} onChange={event => onMoveFolder(event.target.value)}>
          <option value="">Unfiled</option>
          {folders.map(folder => <option key={folder.id} value={folder.id}>{folder.name}</option>)}
        </select>
      </label>
    </div>

    <nav className="inspector-tabs" aria-label="Inspector sections">
      {tabs.map(value => <button key={value.id} className={tab === value.id ? 'is-active' : ''} onClick={() => setTab(value.id)}>{value.label}</button>)}
    </nav>

    <div className="inspector__body">
      {loading && !currentDetail ? <div className="center-loader"><Spinner/><span>Loading artifact detail</span></div> : <>
        {tab === 'overview' && <Overview item={item} detail={currentDetail} tags={tags} onSetTags={onSetTags} onCreateTag={onCreateTag} onUpdateTagVisual={onUpdateTagVisual} onRenameTag={onRenameTag} onDeleteTag={onDeleteTag} onSaveDetails={onSaveDetails} onError={onError}/>}
        {tab === 'variants' && <Variants key={item.entityId} variants={manifest?.variants ?? []} selected={selectedVariant} onSelect={setSelectedVariant} onPreviewMember={onPreviewMember} onSaveVariant={onSaveVariant} onError={onError}/>}
        {tab === 'issues' && <StructuralIssues item={item} detail={currentDetail}/>}
        {tab === 'structure' && <Structure item={item} detail={currentDetail}/>}
        {tab === 'files' && <Files key={item.entityId} item={item} members={members} query={fileFilter} onQuery={setFileFilter} onPreviewMember={onPreviewMember} onExtractMember={onExtractMember} onRevealArchive={onRevealArchive} onCreateWorkspace={onCreateWorkspace} creatingWorkspace={creatingWorkspace} onError={onError}/>}
        {tab === 'history' && <History detail={currentDetail}/>}
      </>}
    </div>
  </aside>
}

function Overview({
  item,
  detail,
  tags,
  onSetTags,
  onCreateTag,
  onUpdateTagVisual,
  onRenameTag,
  onDeleteTag,
  onSaveDetails,
  onError,
}: {
  item: LibraryItem
  detail: EntityDetail | null
  tags: ModTag[]
  onSetTags: (tagIDs: string[]) => Promise<void>
  onCreateTag: (name: string, color: string, icon: string) => Promise<ModTag | null>
  onUpdateTagVisual: (tagID: string, color: string, icon: string) => Promise<void>
  onRenameTag: (tagID: string, name: string) => Promise<void>
  onDeleteTag: (tagID: string) => Promise<void>
  onSaveDetails: (update: LibraryItemDetailsUpdate) => Promise<void>
  onError: (error: unknown) => void
}) {
  const manifest = detail?.item.manifest ?? item.manifest
  return <div className="inspector-section-stack">
    <Details item={item} manifest={manifest} onSave={onSaveDetails} onError={onError}/>
    <fieldset className="inspector-fieldset inspector-tags">
      <legend>Tags</legend>
      <TagEditor
        assigned={item.tags ?? []}
        tags={tags}
        onSet={onSetTags}
        onCreate={onCreateTag}
        onUpdateVisual={onUpdateTagVisual}
        onRename={onRenameTag}
        onDelete={onDeleteTag}
        onError={onError}
      />
    </fieldset>
  </div>
}

function Details({
  item,
  manifest,
  onSave,
  onError,
}: {
  item: LibraryItem
  manifest: LibraryItem['manifest']
  onSave: (update: LibraryItemDetailsUpdate) => Promise<void>
  onError: (error: unknown) => void
}) {
  const initialDraft = {
    description: manifest.description ?? '',
    author: manifest.author ?? '',
    version: manifest.version ?? '',
  }
  const [draft, setDraft] = useState(initialDraft)
  const [editing, setEditing] = useState(false)
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    setDraft(initialDraft)
    setEditing(false)
    setSaving(false)
  }, [item.entityId, manifest.description, manifest.author, manifest.version])

  const cancel = () => {
    setDraft(initialDraft)
    setEditing(false)
  }
  const save = async () => {
    if (saving) return
    setSaving(true)
    try {
      await onSave(draft)
      setEditing(false)
    } catch (error) {
      onError(error)
    } finally {
      setSaving(false)
    }
  }

  return <fieldset className="inspector-fieldset inspector-details">
    <legend>Details</legend>
    <div className="inspector-details__header">
      <span>Identity and presentation metadata for this library item.</span>
      {!editing && <Button icon="edit" tone="quiet" onClick={() => setEditing(true)}>Edit</Button>}
    </div>
    {editing ? <div className="inspector-edit-form">
      <label><span>Description</span><textarea value={draft.description} maxLength={2000} rows={4} onChange={event => setDraft(current => ({ ...current, description: event.target.value }))} placeholder="Describe what this mod adds to BeamNG."/></label>
      <label><span>Author</span><input value={draft.author} maxLength={120} onChange={event => setDraft(current => ({ ...current, author: event.target.value }))} placeholder="Not declared"/></label>
      <label><span>Version</span><input value={draft.version} maxLength={80} onChange={event => setDraft(current => ({ ...current, version: event.target.value }))} placeholder="Not declared"/></label>
      <div className="inspector-form-actions">
        <Button tone="quiet" disabled={saving} onClick={cancel}>Cancel</Button>
        <Button icon="save" tone="primary" disabled={saving} onClick={() => void save()}>{saving ? 'Saving' : 'Save'}</Button>
      </div>
    </div> : <dl className="inspector-details__list">
      <div className="inspector-details__description"><dt>Description</dt><dd>{draft.description.trim() || 'No description provided.'}</dd></div>
      <div><dt>Author</dt><dd>{draft.author.trim() || 'Unknown'}</dd></div>
      <div><dt>Version</dt><dd>{draft.version.trim() || 'Not declared'}</dd></div>
      <div><dt>Archive size</dt><dd>{formatBytes(item.sizeBytes)}</dd></div>
      <div><dt>Files</dt><dd>{(manifest.entryCount ?? item.memberCount).toLocaleString()}</dd></div>
      <div><dt>Updated</dt><dd>{formatDate(manifest.analyzedAt)}</dd></div>
    </dl>}
    {!item.linked && <p className="source-note"><Icon name="unlink" size={16}/><span>The source archive is unavailable. Analysis details remain available from the latest recorded artifact.</span></p>}
  </fieldset>
}

function StructuralIssues({ item, detail }: { item: LibraryItem; detail: EntityDetail | null }) {
  const issues = (detail?.item.manifest ?? item.manifest).issues ?? []
  return <div className="inspector-section-stack inspector-issues">
    {issues.length === 0 ? <EmptyState icon="check" title="No structural issues" detail="The analyzer did not find path, archive, or category integration problems."/> : <section className="inspector-issues__section">
      <header className="inspector-subsection-header"><div><h3>Structural Issues</h3><p>Problems that may keep this content from loading cleanly in BeamNG.</p></div><Badge tone={issues.some(issue => issue.severity === 'error') ? 'danger' : 'warning'}>{issues.length.toLocaleString()}</Badge></header>
      <div className="issue-list">
        {issues.map((issue, index) => <article className={`issue issue--${String(issue.severity)}`} key={`${issue.code}-${index}`}>
          <Icon name={issue.severity === 'error' ? 'error' : issue.severity === 'warning' ? 'warning' : 'activity'} size={17}/>
          <div className="issue__content">
            <div className="issue__heading"><Badge tone={issueTone(String(issue.severity))}>{String(issue.severity)}</Badge><strong>{issue.code}</strong></div>
            <p>{issue.message}</p>
            {issue.path && <code>{issue.path}</code>}
          </div>
        </article>)}
      </div>
    </section>}
  </div>
}

interface VariantDraft {
  configuration: string
  description: string
  configType: string
  bodyStyle: string
  drivetrain: string
  transmission: string
  fuelType: string
  propulsion: string
  power: string
  torque: string
  weight: string
  value: string
  topSpeed: string
}

const variantFields: { key: keyof VariantDraft; label: string; multiline?: boolean }[] = [
  { key: 'configuration', label: 'Name' },
  { key: 'configType', label: 'Config type' },
  { key: 'bodyStyle', label: 'Body style' },
  { key: 'drivetrain', label: 'Drivetrain' },
  { key: 'transmission', label: 'Transmission' },
  { key: 'fuelType', label: 'Fuel type' },
  { key: 'propulsion', label: 'Propulsion' },
  { key: 'power', label: 'Power' },
  { key: 'torque', label: 'Torque' },
  { key: 'weight', label: 'Weight' },
  { key: 'value', label: 'Value' },
  { key: 'topSpeed', label: 'Top speed' },
  { key: 'description', label: 'Description', multiline: true },
]

function Variants({
  variants,
  selected,
  onSelect,
  onPreviewMember,
  onSaveVariant,
  onError,
}: {
  variants: Variant[]
  selected: Variant | null
  onSelect: (variant: Variant | null) => void
  onPreviewMember: (memberPath: string) => Promise<ArchiveMemberPreview | null>
  onSaveVariant: (update: LibraryVariantUpdate) => Promise<void>
  onError: (error: unknown) => void
}) {
  const [overrides, setOverrides] = useState<Record<string, Partial<Variant>>>({})
  const [editing, setEditing] = useState(false)
  const [saving, setSaving] = useState(false)
  const [draft, setDraft] = useState<VariantDraft | null>(null)

  const activeSource = selected ? variants.find(variant => variant.configPath === selected.configPath) ?? null : null
  const active = activeSource ? { ...activeSource, ...(overrides[activeSource.configPath] ?? {}) } : null
  const activeKey = active?.configPath ?? ''
  useEffect(() => {
    setEditing(false)
    setSaving(false)
    setDraft(active ? variantDraft(active) : null)
  }, [activeKey])

  if (variants.length === 0) return <div className="inspector-section-stack"><EmptyState icon="vehicle" title="No vehicle variants" detail="This artifact does not expose matched .pc configuration records."/></div>

  const beginEdit = () => {
    if (!active) return
    setDraft(variantDraft(active))
    setEditing(true)
  }
  const save = async () => {
    if (!active || !draft || saving) return
    setSaving(true)
    try {
      await onSaveVariant(variantUpdate(active, draft))
      setOverrides(current => ({ ...current, [active.configPath]: variantOverride(draft) }))
      setEditing(false)
    } catch (error) {
      onError(error)
    } finally {
      setSaving(false)
    }
  }

  return <div className="variant-layout">
    <div className="variant-gallery" aria-label="Vehicle variants">
      {variants.map(variant => {
        const isSelected = active?.configPath === variant.configPath
        return <VariantCard key={variant.configPath} variant={variant} selected={isSelected} onSelect={() => onSelect(isSelected ? null : variant)} onPreviewMember={onPreviewMember}/>
      })}
    </div>

    <section className={`variant-selection-panel${active ? ' is-open' : ''}`} aria-hidden={!active}>
      {active && <div className="variant-selection-panel__inner">
        <header>
          <div><span className="variant-selection-panel__eyebrow">Selected variant</span><h3>Selected variant details</h3><p>{active.namespace || 'Vehicle configuration'}</p></div>
          <button type="button" className="icon-button" onClick={() => onSelect(null)} aria-label="Close selected variant details"><Icon name="close" size={15}/></button>
        </header>
        {editing && draft ? <div className="variant-edit-form">
          {variantFields.map(field => <label key={field.key}><span>{field.label}</span>{field.multiline ? <textarea rows={3} value={draft[field.key]} onChange={event => setDraft(current => current ? { ...current, [field.key]: event.target.value } : current)}/> : <input value={draft[field.key]} onChange={event => setDraft(current => current ? { ...current, [field.key]: event.target.value } : current)}/>}</label>)}
          <div className="inspector-form-actions">
            <Button tone="quiet" disabled={saving} onClick={() => { setDraft(active ? variantDraft(active) : null); setEditing(false) }}>Cancel</Button>
            <Button icon="save" tone="primary" disabled={saving} onClick={() => void save()}>{saving ? 'Saving' : 'Save'}</Button>
          </div>
        </div> : <div className="variant-detail-view">
          <div className="variant-detail-view__heading"><div><h4>{active.configuration || active.baseName || 'Unnamed variant'}</h4><span>{(active.configType ?? '').trim().toLowerCase() === 'factory' ? 'Factory' : 'Custom'}</span></div><Button icon="edit" tone="quiet" onClick={beginEdit}>Edit</Button></div>
          <p className="variant-description">{active.description?.trim() || 'No description provided.'}</p>
          <dl className="variant-facts">
            <div><dt>Body style</dt><dd>{active.bodyStyle || '—'}</dd></div>
            <div><dt>Drivetrain</dt><dd>{active.drivetrain || '—'}</dd></div>
            <div><dt>Transmission</dt><dd>{active.transmission || '—'}</dd></div>
            <div><dt>Fuel type</dt><dd>{active.fuelType || '—'}</dd></div>
            <div><dt>Propulsion</dt><dd>{active.propulsion || '—'}</dd></div>
            <div><dt>Power</dt><dd>{displayVariantNumber(active.power)}</dd></div>
            <div><dt>Torque</dt><dd>{displayVariantNumber(active.torque)}</dd></div>
            <div><dt>Weight</dt><dd>{displayVariantNumber(active.weight)}</dd></div>
            <div><dt>Value</dt><dd>{displayVariantNumber(active.value)}</dd></div>
            <div><dt>Top speed</dt><dd>{displayVariantNumber(active.topSpeed)}</dd></div>
          </dl>
        </div>}
      </div>}
    </section>
  </div>
}

function VariantCard({
  variant,
  selected,
  onSelect,
  onPreviewMember,
}: {
  variant: Variant
  selected: boolean
  onSelect: () => void
  onPreviewMember: (memberPath: string) => Promise<ArchiveMemberPreview | null>
}) {
  const [thumbnail, setThumbnail] = useState('')
  const cardRef = useRef<HTMLButtonElement>(null)
  const previewRef = useRef(onPreviewMember)
  previewRef.current = onPreviewMember
  const thumbnailPath = variant.thumbnailPath ?? ''

  useEffect(() => {
    setThumbnail('')
    if (!thumbnailPath) return
    let cancelled = false
    let requested = false
    const load = () => {
      if (requested) return
      requested = true
      void previewRef.current(thumbnailPath).then(preview => {
        if (!cancelled) setThumbnail(preview?.dataUrl ?? '')
      }).catch(() => {
        if (!cancelled) setThumbnail('')
      })
    }
    const card = cardRef.current
    if (!card || typeof IntersectionObserver === 'undefined') {
      load()
      return () => { cancelled = true }
    }
    const observer = new IntersectionObserver(entries => {
      if (!entries.some(entry => entry.isIntersecting)) return
      observer.disconnect()
      load()
    }, { root: card.closest('.inspector__body'), rootMargin: '180px 0px' })
    observer.observe(card)
    return () => {
      cancelled = true
      observer.disconnect()
    }
  }, [thumbnailPath])

  const isFactory = (variant.configType ?? '').trim().toLowerCase() === 'factory'
  return <button ref={cardRef} type="button" className={`variant-card${selected ? ' is-selected' : ''}`} aria-pressed={selected} onClick={onSelect}>
    <span className="variant-card__thumb">
      {thumbnail ? <img src={thumbnail} alt="" loading="lazy"/> : <Icon name="vehicle" size={26}/>}
    </span>
    <span className="variant-card__name">{variant.configuration || variant.baseName || variant.namespace}</span>
    <span className={`variant-card__type variant-card__type--${isFactory ? 'factory' : 'custom'}`}>{isFactory ? 'Factory' : 'Custom'}</span>
  </button>
}

function variantDraft(variant: Variant): VariantDraft {
  return {
    configuration: variant.configuration ?? variant.baseName ?? '',
    description: variant.description ?? '',
    configType: variant.configType ?? '',
    bodyStyle: variant.bodyStyle ?? '',
    drivetrain: variant.drivetrain ?? '',
    transmission: variant.transmission ?? '',
    fuelType: variant.fuelType ?? '',
    propulsion: variant.propulsion ?? '',
    power: displayVariantNumber(variant.power, false),
    torque: displayVariantNumber(variant.torque, false),
    weight: displayVariantNumber(variant.weight, false),
    value: displayVariantNumber(variant.value, false),
    topSpeed: displayVariantNumber(variant.topSpeed, false),
  }
}

function variantUpdate(variant: Variant, draft: VariantDraft): LibraryVariantUpdate {
  return {
    configPath: variant.configPath,
    configuration: draft.configuration,
    description: draft.description,
    configType: draft.configType,
    bodyStyle: draft.bodyStyle,
    drivetrain: draft.drivetrain,
    transmission: draft.transmission,
    fuelType: draft.fuelType,
    propulsion: draft.propulsion,
    power: draft.power,
    torque: draft.torque,
    weight: draft.weight,
    value: draft.value,
    topSpeed: draft.topSpeed,
  }
}

function variantOverride(draft: VariantDraft): Partial<Variant> {
  return {
    configuration: draft.configuration,
    description: draft.description,
    configType: draft.configType,
    bodyStyle: draft.bodyStyle,
    drivetrain: draft.drivetrain,
    transmission: draft.transmission,
    fuelType: draft.fuelType,
    propulsion: draft.propulsion,
    power: parseVariantNumber(draft.power),
    torque: parseVariantNumber(draft.torque),
    weight: parseVariantNumber(draft.weight),
    value: parseVariantNumber(draft.value),
    topSpeed: parseVariantNumber(draft.topSpeed),
  }
}

function displayVariantNumber(value?: number, dash = true): string {
  if (typeof value !== 'number' || !Number.isFinite(value)) return dash ? '—' : ''
  return String(value)
}

function parseVariantNumber(value: string): number | undefined {
  const trimmed = value.trim()
  if (!trimmed) return undefined
  const parsed = Number(trimmed)
  return Number.isFinite(parsed) ? parsed : undefined
}

function Structure({ item, detail }: { item: LibraryItem; detail: EntityDetail | null }) {
  const manifest = detail?.item.manifest ?? item.manifest
  const namespaceEntries = Object.entries(manifest.namespaces ?? {}).filter(([, values]) => (values ?? []).length > 0) as [string, string[]][]
  const jbeam = manifest.jbeam
  const map = manifest.map
  const ui = manifest.ui
  const levelIDs = map.levelIds ?? []
  const controllers = jbeam.controllers ?? []
  const metadataDocuments = manifest.metadataDocuments ?? []
  const hasVehicle = jbeam.files > 0 || controllers.length > 0
  const hasWorld = levelIDs.length > 0 || map.terrainFiles + map.levelObjectFiles + map.forestFiles + map.facilityFiles + map.spawnPoints > 0
  const hasAssets = map.materialFiles + map.modelFiles + map.textureFiles > 0
  const hasInterface = (ui.appRoots?.length ?? 0) > 0 || ui.htmlFiles + ui.cssFiles + ui.javaScriptFiles + ui.luaFiles + ui.settingsFiles + ui.scriptFiles > 0

  return <div className="inspector-section-stack inspector-structure">
    {namespaceEntries.length > 0 && <section className="structure-group">
      <header><Icon name="archive" size={17}/><div><h3>Package roots</h3><p>Namespaces show where BeamNG resolves this content inside the archive.</p></div></header>
      <div className="structure-roots">{namespaceEntries.map(([root, values]) => <div key={root}><strong>{root}</strong><span>{values.map(value => <code key={value}>{value}</code>)}</span></div>)}</div>
    </section>}
    {hasVehicle && <section className="structure-group">
      <header><Icon name="vehicle" size={17}/><div><h3>Vehicle integration</h3><p>JBeam definitions provide the parts and physics that BeamNG assembles into a vehicle.</p></div></header>
      <p className="structure-summary">{jbeam.files.toLocaleString()} JBeam files · {jbeam.parsedFiles.toLocaleString()} parsed · {jbeam.declaredNodes.toLocaleString()} nodes · {jbeam.declaredBeams.toLocaleString()} beams · {jbeam.declaredSlots.toLocaleString()} slots</p>
      {controllers.length > 0 && <div className="structure-reference"><span>Controller references</span><div>{controllers.map(value => <code key={value}>{value}</code>)}</div></div>}
    </section>}
    {hasWorld && <section className="structure-group">
      <header><Icon name="map" size={17}/><div><h3>World integration</h3><p>Levels, terrain, scene objects, facilities, and spawn points describe how BeamNG loads a world.</p></div></header>
      <p className="structure-summary">{levelIDs.length.toLocaleString()} level{levelIDs.length === 1 ? '' : 's'} · {map.terrainFiles.toLocaleString()} terrain · {map.levelObjectFiles.toLocaleString()} object groups · {map.forestFiles.toLocaleString()} forest · {map.facilityFiles.toLocaleString()} facilities · {map.spawnPoints.toLocaleString()} spawn points</p>
      {levelIDs.length > 0 && <div className="structure-reference"><span>Level IDs</span><div>{levelIDs.map(value => <code key={value}>{value}</code>)}</div></div>}
    </section>}
    {hasAssets && <section className="structure-group">
      <header><Icon name="files" size={17}/><div><h3>Shared assets</h3><p>Materials, models, and textures provide the reusable visual resources referenced by this package.</p></div></header>
      <p className="structure-summary">{map.materialFiles.toLocaleString()} material files · {map.modelFiles.toLocaleString()} model files · {map.textureFiles.toLocaleString()} texture files</p>
    </section>}
    {hasInterface && <section className="structure-group">
      <header><Icon name="code" size={17}/><div><h3>Interface and scripting</h3><p>UI roots and scripts are the entry points BeamNG can load for menus, settings, and behavior.</p></div></header>
      <p className="structure-summary">{(ui.appRoots?.length ?? 0).toLocaleString()} app roots · {ui.htmlFiles.toLocaleString()} HTML · {ui.cssFiles.toLocaleString()} CSS · {ui.javaScriptFiles.toLocaleString()} JavaScript · {ui.luaFiles.toLocaleString()} Lua scripts</p>
      {ui.appRoots && ui.appRoots.length > 0 && <div className="structure-reference"><span>App roots</span><div>{ui.appRoots.map(value => <code key={value}>{value}</code>)}</div></div>}
    </section>}
    {metadataDocuments.length > 0 && <section className="structure-group">
      <header><Icon name="files" size={17}/><div><h3>Manifest documents</h3><p>Analyzed metadata documents provide the titles, configurations, and integration hints used above.</p></div></header>
      <div className="structure-document-list">{metadataDocuments.map(document => <span key={document.path}><Icon name="files" size={13}/>{document.path}</span>)}</div>
    </section>}
    {namespaceEntries.length === 0 && !hasVehicle && !hasWorld && !hasAssets && !hasInterface && metadataDocuments.length === 0 && <EmptyState icon="archive" title="No integration groups detected" detail="The manifest does not expose recognized BeamNG content roots yet."/>}
  </div>
}

function Files({
  item,
  members,
  query,
  onQuery,
  onPreviewMember,
  onExtractMember,
  onRevealArchive,
  onCreateWorkspace,
  creatingWorkspace,
  onError,
}: {
  item: LibraryItem
  members: { total: number; visible: ArchiveMember[] }
  query: string
  onQuery: (value: string) => void
  onPreviewMember: (memberPath: string) => Promise<ArchiveMemberPreview | null>
  onExtractMember: (memberPath: string) => Promise<void>
  onRevealArchive: () => Promise<void>
  onCreateWorkspace: () => void
  creatingWorkspace: boolean
  onError: (error: unknown) => void
}) {
  const [selectedPath, setSelectedPath] = useState('')
  const [preview, setPreview] = useState<ArchiveMemberPreview | null>(null)
  const [previewLoading, setPreviewLoading] = useState(false)
  const [copied, setCopied] = useState(false)
  const [actionBusy, setActionBusy] = useState('')
  const previewRef = useRef(onPreviewMember)
  previewRef.current = onPreviewMember
  const selected = members.visible.find(member => member.path === selectedPath) ?? null

  useEffect(() => {
    if (selectedPath && !members.visible.some(member => member.path === selectedPath)) setSelectedPath('')
  }, [members.visible, selectedPath])

  useEffect(() => {
    let cancelled = false
    setCopied(false)
    setPreview(null)
    if (!selected || selected.directory) {
      setPreviewLoading(false)
      return () => { cancelled = true }
    }
    setPreviewLoading(true)
    void previewRef.current(selected.path).then(result => {
      if (!cancelled) setPreview(result)
    }).catch(error => {
      if (!cancelled) onError(error)
    }).finally(() => {
      if (!cancelled) setPreviewLoading(false)
    })
    return () => { cancelled = true }
  }, [selected?.path, selected?.directory, selected?.crc32])

  const copyPath = async () => {
    if (!selected) return
    try {
      if (navigator.clipboard?.writeText) {
        await navigator.clipboard.writeText(selected.path)
      } else {
        const fallback = document.createElement('textarea')
        fallback.value = selected.path
        fallback.setAttribute('readonly', '')
        fallback.style.position = 'fixed'
        fallback.style.opacity = '0'
        document.body.appendChild(fallback)
        fallback.select()
        const copiedByFallback = document.execCommand('copy')
        fallback.remove()
        if (!copiedByFallback) throw new Error('Clipboard is unavailable')
      }
      setCopied(true)
    } catch (error) {
      onError(error)
    }
  }
  const extract = async () => {
    if (!selected || selected.directory || actionBusy) return
    setActionBusy('extract')
    try {
      await onExtractMember(selected.path)
    } catch (error) {
      onError(error)
    } finally {
      setActionBusy('')
    }
  }
  const reveal = async () => {
    if (actionBusy) return
    setActionBusy('reveal')
    try {
      await onRevealArchive()
    } catch (error) {
      onError(error)
    } finally {
      setActionBusy('')
    }
  }

  return <div className="file-inventory">
    <label className="search-box search-box--wide"><Icon name="search" size={15}/><input value={query} onChange={event => onQuery(event.target.value)} placeholder="Filter archive members"/></label>
    <p className="inventory-count">Showing {members.visible.length.toLocaleString()} of {members.total.toLocaleString()} matching entries</p>
    <div className="inventory-table">
      <div className="inventory-table__head"><span>Path</span><span>Expanded</span><span>Method</span></div>
      <div className="inventory-table__rows" role="listbox" aria-label="Archive files">
        {members.visible.map(member => <button type="button" role="option" aria-selected={selectedPath === member.path} className={`inventory-table__row${selectedPath === member.path ? ' is-selected' : ''}`} key={member.path} onClick={() => setSelectedPath(member.path)}>
          <span title={member.path}><Icon name={member.directory ? 'folder' : 'files'} size={13}/>{member.path}</span>
          <span>{member.directory ? '—' : formatBytes(member.uncompressedBytes)}</span>
          <span>{member.directory ? 'Directory' : member.method === 0 ? 'Store' : member.method === 8 ? 'Deflate' : String(member.method)}</span>
        </button>)}
      </div>
      {selected && <section className="file-preview-panel" aria-label="Selected file preview">
        <header><div><span>Selected file</span><strong title={selected.path}>{selected.path}</strong></div><Badge tone={selected.directory ? 'neutral' : 'accent'}>{selected.directory ? 'Directory' : 'File'}</Badge></header>
        <div className="file-preview-panel__actions">
          <Button icon="copy" tone="quiet" onClick={() => void copyPath()}>{copied ? 'Copied' : 'Copy path'}</Button>
          <Button icon="export" tone="quiet" disabled={selected.directory || !item.linked || actionBusy !== ''} onClick={() => void extract()}>{actionBusy === 'extract' ? 'Extracting' : 'Extract'}</Button>
          <Button icon="archive" tone="quiet" disabled={!item.linked || actionBusy !== ''} onClick={() => void reveal()}>{actionBusy === 'reveal' ? 'Opening' : 'Reveal archive'}</Button>
          <Button icon="workspace" tone="quiet" disabled={!item.linked || creatingWorkspace} onClick={onCreateWorkspace}>{creatingWorkspace ? 'Opening project' : 'Open in ModMaker'}</Button>
        </div>
        {selected.directory ? <div className="file-preview-panel__metadata"><strong>Directory</strong><span>This entry groups files in the archive and has no file content to preview.</span></div> : previewLoading ? <div className="file-preview-panel__loading"><Spinner small/><span>Loading preview</span></div> : preview ? <FilePreview preview={preview} member={selected}/> : <div className="file-preview-panel__metadata"><strong>Preview unavailable</strong><span>This file can be selected and extracted, but its contents are not supported for inline preview.</span></div>}
      </section>}
    </div>
  </div>
}

function FilePreview({ preview, member }: { preview: ArchiveMemberPreview; member: ArchiveMember }) {
  const mime = (preview.mime ?? '').toLowerCase()
  const isImage = Boolean(preview.dataUrl && mime.startsWith('image/'))
  const text = typeof preview.text === 'string' ? preview.text : ''
  const isText = preview.kind === 'text' || mime.startsWith('text/') || /json|xml|javascript|css|yaml|toml/.test(mime)
  if (isImage) return <div className="file-preview-panel__content file-preview-panel__content--image"><img src={preview.dataUrl} alt={`Preview of ${member.path}`}/>{preview.truncated && <small>Preview image was shortened for display.</small>}</div>
  if (isText) return <div className="file-preview-panel__content file-preview-panel__content--text"><pre>{text || '(empty file)'}</pre>{preview.truncated && <small>Preview truncated for display. Extract the file for the complete content.</small>}</div>
  return <div className="file-preview-panel__metadata">
    <strong>Binary or unsupported preview</strong>
    <dl><div><dt>Kind</dt><dd>{preview.kind || 'Unknown'}</dd></div><div><dt>MIME</dt><dd>{preview.mime || 'Not identified'}</dd></div><div><dt>Size</dt><dd>{formatBytes(preview.sizeBytes || member.uncompressedBytes)}</dd></div></dl>
    <span>This file is not editable here. Extract it to inspect or change it with the appropriate tool.</span>
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

function securityScanStatus(item: LibraryItem): string {
  const verdict = formatVerdict(item.lastSecurityScanVerdict || item.healthLabel)
  const hasScan = Boolean(item.lastSecurityScanAt || item.lastSecurityScanVerdict)
  if (!hasScan) return 'Not scanned'
  return `${item.securityScanChanged ? 'Last scan' : 'Scanned'}: ${verdict}${!item.securityScanChanged && item.lastSecurityScanAt ? ` · ${formatDate(item.lastSecurityScanAt)}` : ''}`
}

function formatVerdict(value: string): string {
  const normalized = value.trim().replace(/_/g, ' ')
  if (!normalized) return 'Unknown verdict'
  return normalized.replace(/^\w/, character => character.toUpperCase())
}

function scanStatusIcon(status: string): 'shield' | 'check' | 'warning' | 'error' | 'scan' {
  if (status === 'safe') return 'check'
  if (status === 'review') return 'warning'
  if (status === 'threat' || status === 'broken' || status === 'scan_failed') return 'error'
  if (status === 'scanning') return 'scan'
  return 'shield'
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
  return titles[type] ?? type.replace(/_/g, ' ').replace(/^\w/, value => value.toUpperCase())
}

function historyEventDetail(event: EventRecord) {
  const data: Record<string, unknown> = event.data ?? {}
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

function firstString(data: Record<string, unknown>, keys: string[]): string {
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
