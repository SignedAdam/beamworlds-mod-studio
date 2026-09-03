import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type CSSProperties,
  type KeyboardEvent as ReactKeyboardEvent,
  type PointerEvent as ReactPointerEvent,
} from 'react'
import type {
  ArchiveMemberPreview,
  EntityDetail,
  LibraryItem,
  LibraryItemDetailsUpdate,
  LibraryVariantUpdate,
  ModTag,
} from '../bindings/github.com/SignedAdam/beamng-mod-studio/models.js'
import type { Variant } from '../bindings/github.com/SignedAdam/beamworlds-modkit/models.js'
import { Icon } from './icons'
import { IndexCardTabs, type IndexCardTabItem } from './IndexCardTabs'
import InlineEditableField from './InlineEditableField'
import { MetaPanel, HistoryPanel, StructuralIssuesPanel } from './InspectorPanels'
import { TagEditor } from './TagEditor'
import { Button, Spinner, formatBytes, formatDate, kindIcon, kindLabel } from './ui'

type InspectorTab = 'overview' | 'variants' | 'issues' | 'meta' | 'history'
type DirtyReporter = (field: string, dirty: boolean) => void

interface InspectorProps {
  item: LibraryItem
  detail: EntityDetail | null
  tags: ModTag[]
  loading: boolean
  creatingWorkspace: boolean
  stale: boolean
  writeBlocked: boolean
  inspectorWidth: number
  inspectorMinWidth: number
  inspectorMaxWidth: number
  inspectorResizeDisabled?: boolean
  onResizeCommit: (width: number) => void
  onResetWidth: () => void
  onClose: () => void
  onSetTags: (tagIDs: string[]) => Promise<ModTag[]>
  onCreateTag: (name: string, color: string, icon: string) => Promise<ModTag | null>
  onUpdateTagVisual: (tagID: string, color: string, icon: string) => Promise<void>
  onDeleteTag: (tagID: string) => Promise<void>
  onCreateWorkspace: () => void
  onVirusScan: (item: LibraryItem) => void
  onSaveDetails: (update: LibraryItemDetailsUpdate) => Promise<void>
  onSaveVariant: (update: LibraryVariantUpdate) => Promise<void>
  onPreviewMember: (memberPath: string) => Promise<ArchiveMemberPreview | null>
  onError: (error: unknown) => void
  onDirtyChange?: (dirty: boolean) => void
}

export function Inspector({
  item,
  detail,
  tags,
  loading,
  creatingWorkspace,
  stale,
  writeBlocked,
  inspectorWidth,
  inspectorMinWidth,
  inspectorMaxWidth,
  inspectorResizeDisabled = false,
  onResizeCommit,
  onResetWidth,
  onClose,
  onSetTags,
  onCreateTag,
  onUpdateTagVisual,
  onDeleteTag,
  onCreateWorkspace,
  onVirusScan,
  onSaveDetails,
  onSaveVariant,
  onPreviewMember,
  onError,
  onDirtyChange,
}: InspectorProps) {
  const [tab, setTab] = useState<InspectorTab>('overview')
  const [selectedVariant, setSelectedVariant] = useState<Variant | null>(null)
  const dirtyFieldsRef = useRef(new Set<string>())
  const onDirtyChangeRef = useRef(onDirtyChange)
  onDirtyChangeRef.current = onDirtyChange
  const reportDirty = useCallback<DirtyReporter>((field, dirty) => {
    if (dirty) dirtyFieldsRef.current.add(field)
    else dirtyFieldsRef.current.delete(field)
    onDirtyChangeRef.current?.(dirtyFieldsRef.current.size > 0)
  }, [])

  useEffect(() => {
    setTab('overview')
    setSelectedVariant(null)
    dirtyFieldsRef.current.clear()
    onDirtyChangeRef.current?.(false)
  }, [item.entityId])

  const currentDetail = detail?.item.entityId === item.entityId ? detail : null
  const manifest = currentDetail?.item.manifest ?? item.manifest
  const variants = manifest?.variants ?? []
  const issueCount = (manifest?.issues ?? []).length
  const historyCount = historyTotal(currentDetail)
  const validTabs: InspectorTab[] = ['overview', 'issues', 'meta', 'history']
  if (String(item.kind) === 'vehicle' && variants.length > 0) validTabs.splice(1, 0, 'variants')

  useEffect(() => {
    if (!validTabs.includes(tab)) setTab('overview')
  }, [tab, variants.length, item.kind])

  const tabs: IndexCardTabItem[] = useMemo(() => {
    const items: IndexCardTabItem[] = [
      {
        id: 'overview',
        label: 'Overview',
        panel: <Overview
          key={item.entityId}
          item={item}
          manifest={manifest}
          tags={tags}
          stale={stale}
          writeBlocked={writeBlocked}
          onSetTags={onSetTags}
          onCreateTag={onCreateTag}
          onUpdateTagVisual={onUpdateTagVisual}
          onDeleteTag={onDeleteTag}
          onSaveDetails={onSaveDetails}
          reportDirty={reportDirty}
          onError={onError}
        />,
      },
    ]
    if (String(item.kind) === 'vehicle' && variants.length > 0) {
      items.push({
        id: 'variants',
        label: 'Variants',
        count: variants.length,
        panel: <Variants
          key={`${item.entityId}-variants`}
          variants={variants}
          selected={selectedVariant}
          stale={stale}
          writeBlocked={writeBlocked}
          onSelect={setSelectedVariant}
          onPreviewMember={onPreviewMember}
          onSaveVariant={onSaveVariant}
          reportDirty={reportDirty}
        />,
      })
    }
    items.push(
      {
        id: 'issues',
        label: 'Issues',
        count: issueCount,
        panel: <StructuralIssuesPanel issues={manifest?.issues ?? []} />,
      },
      {
        id: 'meta',
        label: 'Meta',
        panel: <MetaPanel kind={String(item.kind)} manifest={manifest} />,
      },
      {
        id: 'history',
        label: `History (${historyCount})`,
        panel: <HistoryTab history={currentDetail?.history ?? null} loading={loading} total={historyCount} />,
      },
    )
    return items
  }, [currentDetail?.history, historyCount, issueCount, item, loading, manifest, onCreateTag, onDeleteTag, onError, onPreviewMember, onSaveDetails, onSaveVariant, onSetTags, onUpdateTagVisual, reportDirty, selectedVariant, stale, tags, variants, writeBlocked])

  const scanState = securityScanState(item)
  const scanLabel = securityScanStatus(item, scanState)

  return <aside
    className={`inspector${stale ? ' is-stale' : ''}`}
    aria-label="Mod inspector"
    style={{ '--inspector-width': `${inspectorWidth}px` } as CSSProperties}
  >
    <InspectorResizer
      width={inspectorWidth}
      minWidth={inspectorMinWidth}
      maxWidth={inspectorMaxWidth}
      disabled={inspectorResizeDisabled}
      onCommit={onResizeCommit}
      onReset={onResetWidth}
    />
    <header className="inspector__header">
      <div className="inspector__identity">
        <Icon name={kindIcon(String(item.kind))} size={19}/>
        <div>
          <h2>{item.displayName}</h2>
          <span>{kindLabel(String(item.kind))}{!item.linked && ' · Source unavailable'}</span>
        </div>
      </div>
      <button type="button" className="inspector-button inspector-button--quiet icon-button" onClick={onClose} aria-label="Close inspector" title="Close inspector"><Icon name="close" size={16}/></button>
    </header>
    {stale && <div className="inspector-stale-warning" role="alert" aria-live="assertive"><Icon name="warning" size={18}/><strong>Changed. Please close and reopen this mod&apos;s details.</strong></div>}
    <div className="inspector__actions">
      <div className="inspector-action-region inspector-action-region--edit">
        <Button
          className="inspector-button inspector-button--primary"
          icon="workspace"
          disabled={stale || writeBlocked || !item.linked || creatingWorkspace}
          aria-busy={creatingWorkspace}
          onClick={onCreateWorkspace}
        >
          {creatingWorkspace ? <><Spinner small/>Creating…</> : 'Edit in Mod Maker'}
        </Button>
      </div>
      <div className="inspector-action-region inspector-action-region--security">
        <button
          type="button"
          className={`inspector-button inspector-button--secondary inspector-scan-button inspector-scan-button--${scanState}`}
          disabled={stale || writeBlocked || !item.linked}
          onClick={() => onVirusScan(item)}
        >
          <Icon name="shield" size={16}/><strong>Scan mod</strong>
        </button>
        <div className="inspector-scan-status" role="status" aria-live="polite"><Icon name={scanStatusIcon(scanState)} size={16}/><span title={scanLabel}>{scanLabel}</span></div>
      </div>
    </div>
    <div className="inspector__body">
      {loading && !currentDetail ? <div className="center-loader"><Spinner/><span>Loading artifact detail</span></div> : <IndexCardTabs
        items={tabs}
        value={tab}
        onValueChange={value => setTab(value as InspectorTab)}
        activationMode="manual"
        ariaLabel="Inspector sections"
        mountInactivePanels
      />}
    </div>
  </aside>
}
function previewInspectorWidth(element: HTMLElement, width: number) {
  const value = `${width}px`
  element.parentElement?.style.setProperty('--inspector-width', value)
  element.closest<HTMLElement>('.app-shell')?.style.setProperty('--inspector-width', value)
}

function InspectorResizer({
  width,
  minWidth,
  maxWidth,
  disabled,
  onCommit,
  onReset,
}: {
  width: number
  minWidth: number
  maxWidth: number
  disabled: boolean
  onCommit: (width: number) => void
  onReset: () => void
}) {
  const [liveWidth, setLiveWidth] = useState(width)
  const [dragging, setDragging] = useState(false)
  const dragRef = useRef<{
    pointerID: number
    startX: number
    startWidth: number
    lastWidth: number
  } | null>(null)

  useEffect(() => {
    if (!dragRef.current) setLiveWidth(width)
  }, [width])

  const handlePointerDown = (event: ReactPointerEvent<HTMLDivElement>) => {
    if (disabled) return
    event.preventDefault()
    event.currentTarget.setPointerCapture(event.pointerId)
    dragRef.current = {
      pointerID: event.pointerId,
      startX: event.clientX,
      startWidth: liveWidth,
      lastWidth: liveWidth,
    }
    setDragging(true)
  }
  const handlePointerMove = (event: ReactPointerEvent<HTMLDivElement>) => {
    const drag = dragRef.current
    if (!drag || drag.pointerID !== event.pointerId) return
    const next = Math.max(
      minWidth,
      Math.min(maxWidth, drag.startWidth - (event.clientX - drag.startX)),
    )
    drag.lastWidth = next
    setLiveWidth(next)
    previewInspectorWidth(event.currentTarget, next)
  }
  const finishPointer = (event: ReactPointerEvent<HTMLDivElement>) => {
    const drag = dragRef.current
    if (!drag || drag.pointerID !== event.pointerId) return
    dragRef.current = null
    setDragging(false)
    onCommit(drag.lastWidth)
    if (event.currentTarget.hasPointerCapture(event.pointerId))
      event.currentTarget.releasePointerCapture(event.pointerId)
  }
  const handleLostPointerCapture = () => {
    const drag = dragRef.current
    if (!drag) return
    dragRef.current = null
    setDragging(false)
    onCommit(drag.lastWidth)
  }
  const commitKeyboardResize = (element: HTMLElement, next: number) => {
    const clamped = Math.max(minWidth, Math.min(maxWidth, next))
    setLiveWidth(clamped)
    previewInspectorWidth(element, clamped)
    onCommit(clamped)
  }
  const handleKeyDown = (event: ReactKeyboardEvent<HTMLDivElement>) => {
    if (disabled) return
    if (event.key === 'Enter') {
      event.preventDefault()
      onReset()
      return
    }
    if (event.key === 'ArrowLeft' || event.key === 'ArrowRight') {
      event.preventDefault()
      const amount = event.shiftKey ? 64 : 16
      commitKeyboardResize(
        event.currentTarget,
        liveWidth + (event.key === 'ArrowLeft' ? amount : -amount),
      )
      return
    }
    if (event.key === 'Home') {
      event.preventDefault()
      commitKeyboardResize(event.currentTarget, minWidth)
      return
    }
    if (event.key === 'End') {
      event.preventDefault()
      commitKeyboardResize(event.currentTarget, maxWidth)
    }
  }
  return <div
    className={`inspector-resizer${dragging ? ' is-dragging' : ''}`}
    role="separator"
    aria-orientation="vertical"
    aria-label="Resize inspector"
    aria-valuemin={minWidth}
    aria-valuemax={maxWidth}
    aria-valuenow={liveWidth}
    aria-valuetext={`${liveWidth} pixels`}
    aria-disabled={disabled || undefined}
    tabIndex={disabled ? -1 : 0}
    onPointerDown={handlePointerDown}
    onPointerMove={handlePointerMove}
    onPointerUp={finishPointer}
    onPointerCancel={finishPointer}
    onLostPointerCapture={handleLostPointerCapture}
    onDoubleClick={() => { if (!disabled) onReset() }}
    onKeyDown={handleKeyDown}
    title={disabled ? 'Inspector resizing is unavailable at this width' : 'Drag to resize · Enter or double-click to reset'}
  />
}

function Overview({
  item,
  manifest,
  tags,
  stale,
  writeBlocked,
  onSetTags,
  onCreateTag,
  onUpdateTagVisual,
  onDeleteTag,
  onSaveDetails,
  reportDirty,
  onError,
}: {
  item: LibraryItem
  manifest: LibraryItem['manifest']
  tags: ModTag[]
  stale: boolean
  writeBlocked: boolean
  onSetTags: (tagIDs: string[]) => Promise<ModTag[]>
  onCreateTag: (name: string, color: string, icon: string) => Promise<ModTag | null>
  onUpdateTagVisual: (tagID: string, color: string, icon: string) => Promise<void>
  onDeleteTag: (tagID: string) => Promise<void>
  onSaveDetails: (update: LibraryItemDetailsUpdate) => Promise<void>
  reportDirty: DirtyReporter
  onError: (error: unknown) => void
}) {
  return <div className="inspector-section-stack">
    <Details item={item} manifest={manifest} stale={stale} writeBlocked={writeBlocked} onSave={onSaveDetails} reportDirty={reportDirty}/>
    <fieldset className="inspector-fieldset inspector-tags">
      <legend>Tags</legend>
      <TagEditor
        assigned={item.tags ?? []}
        tags={tags}
        selectionKey={item.entityId}
        locked={stale || writeBlocked}
        onSet={onSetTags}
        onCreate={onCreateTag}
        onUpdateVisual={onUpdateTagVisual}
        onDelete={onDeleteTag}
        onError={onError}
      />
    </fieldset>
  </div>
}

type DetailFieldKey = 'description' | 'author' | 'version'
const detailFields: { key: DetailFieldKey; label: string; multiline?: boolean }[] = [
  { key: 'description', label: 'Description', multiline: true },
  { key: 'author', label: 'Author' },
  { key: 'version', label: 'Version' },
]

function Details({
  item,
  manifest,
  stale,
  writeBlocked,
  onSave,
  reportDirty,
}: {
  item: LibraryItem
  manifest: LibraryItem['manifest']
  stale: boolean
  writeBlocked: boolean
  onSave: (update: LibraryItemDetailsUpdate) => Promise<void>
  reportDirty: DirtyReporter
}) {
  const values: Record<DetailFieldKey, string> = {
    description: manifest.description ?? '',
    author: manifest.author ?? '',
    version: manifest.version ?? '',
  }
  const saveField = (key: DetailFieldKey, value: string) => onSave({
    description: key === 'description' ? value : values.description,
    author: key === 'author' ? value : values.author,
    version: key === 'version' ? value : values.version,
  })

  return <fieldset className="inspector-fieldset inspector-details">
    <legend>Details</legend>
    <div className="inspector-details__fields">
      {detailFields.map(field => <InlineEditableField<string>
        key={`${item.entityId}-${field.key}`}
        id={`inspector-${item.entityId}-${field.key}`}
        className={`inspector-detail-field inspector-detail-field--${field.key}`}
        label={field.label}
        value={values[field.key]}
        multiline={field.multiline}
        locked={stale || writeBlocked}
        onDirtyChange={dirty => reportDirty(`details:${field.key}`, dirty)}
        onSave={value => saveField(field.key, value)}
        renderDisplay={value => <span className="inspector-inline-value">{displayDetailValue(field.key, value)}</span>}
        renderEditor={({ draft, onChange, id, labelId, describedBy, disabled, autoFocus, onKeyDown }) => field.multiline ? <textarea
          id={id}
          value={draft}
          maxLength={2000}
          rows={4}
          placeholder="Describe what this mod adds to BeamNG."
          aria-describedby={describedBy}
          aria-labelledby={labelId}
          disabled={disabled}
          autoFocus={autoFocus}
          onChange={event => onChange(event.target.value)}
          onKeyDown={onKeyDown}
        /> : <input
          id={id}
          value={draft}
          maxLength={field.key === 'author' ? 120 : 80}
          placeholder="Not declared"
          aria-describedby={describedBy}
          aria-labelledby={labelId}
          disabled={disabled}
          autoFocus={autoFocus}
          onChange={event => onChange(event.target.value)}
          onKeyDown={onKeyDown}
        />}
      />)}
    </div>
    <dl className="inspector-details__facts">
      <div><dt>Archive size</dt><dd>{formatBytes(item.sizeBytes)}</dd></div>
      <div><dt>Entries</dt><dd>{(manifest.entryCount ?? item.memberCount).toLocaleString()}</dd></div>
      <div><dt>Updated</dt><dd>{formatDate(manifest.analyzedAt)}</dd></div>
    </dl>
    {!item.linked && <p className="source-note"><Icon name="unlink" size={16}/><span>The source archive is unavailable. Analysis details remain available from the latest recorded artifact.</span></p>}
  </fieldset>
}

function displayDetailValue(key: DetailFieldKey, value: string): string {
  const trimmed = value.trim()
  if (trimmed) return trimmed
  if (key === 'description') return 'No description provided.'
  if (key === 'author') return 'Unknown'
  return 'Not declared'
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

type VariantFieldKey = Exclude<keyof VariantDraft, 'configuration'>
const variantFields: { key: VariantFieldKey; label: string; multiline?: boolean }[] = [
  { key: 'description', label: 'Description', multiline: true },
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
]

function Variants({
  variants,
  selected,
  stale,
  writeBlocked,
  onSelect,
  onPreviewMember,
  onSaveVariant,
  reportDirty,
}: {
  variants: Variant[]
  selected: Variant | null
  stale: boolean
  writeBlocked: boolean
  onSelect: (variant: Variant | null) => void
  onPreviewMember: (memberPath: string) => Promise<ArchiveMemberPreview | null>
  onSaveVariant: (update: LibraryVariantUpdate) => Promise<void>
  reportDirty: DirtyReporter
}) {
  const variantCardRefs = useRef(new Map<string, HTMLButtonElement>())
  const [overrides, setOverrides] = useState<Record<string, Partial<Variant>>>({})
  const dirtyFieldsRef = useRef(new Set<string>())
  const activeSource = selected ? variants.find(variant => variant.configPath === selected.configPath) ?? null : null
  const active = activeSource ? { ...activeSource, ...(overrides[activeSource.configPath] ?? {}) } : null
  const activeKey = active?.configPath ?? ''
  const activeDraft = active ? variantDraft(active) : null

  useEffect(() => {
    setOverrides(current => {
      const available = new Set(variants.map(variant => variant.configPath))
      const next = Object.fromEntries(Object.entries(current).filter(([key]) => available.has(key)))
      return Object.keys(next).length === Object.keys(current).length ? current : next
    })
  }, [variants])

  const discardDirty = () => {
    for (const field of dirtyFieldsRef.current) reportDirty(field, false)
    dirtyFieldsRef.current.clear()
  }
  const guardedSelect = (next: Variant | null) => {
    if (dirtyFieldsRef.current.size > 0 && !window.confirm('Discard unsaved variant changes?')) return
    const returnFocusKey = next === null ? activeKey : ''
    discardDirty()
    onSelect(next)
    if (returnFocusKey) {
      window.requestAnimationFrame(() => {
        variantCardRefs.current.get(returnFocusKey)?.focus({ preventScroll: true })
      })
    }
  }

  const saveVariantField = async (key: VariantFieldKey, value: string) => {
    if (!active || !activeDraft) return
    const draft = { ...activeDraft, [key]: value }
    await onSaveVariant(variantUpdate(active, draft))
    setOverrides(current => ({ ...current, [active.configPath]: variantOverride(draft) }))
  }

  const identitySave = async (value: string) => {
    if (!active || !activeDraft) return
    const draft = { ...activeDraft, configuration: value }
    await onSaveVariant(variantUpdate(active, draft))
    setOverrides(current => ({ ...current, [active.configPath]: variantOverride(draft) }))
  }

  return <div className="variant-layout">
    <div className="variant-gallery" aria-label="Vehicle variants">
      {variants.map(variant => <button
        key={variant.configPath}
        type="button"
        ref={element => {
          if (element) variantCardRefs.current.set(variant.configPath, element)
          else variantCardRefs.current.delete(variant.configPath)
        }}
        className={`variant-card${active?.configPath === variant.configPath ? ' is-selected' : ''}`}
        aria-pressed={active?.configPath === variant.configPath}
        onClick={() => guardedSelect(active?.configPath === variant.configPath ? null : variant)}
      >
        <VariantThumbnail variant={variant} onPreviewMember={onPreviewMember} className="variant-card__thumb"/>
        <span className="variant-card__name">{variant.configuration || variant.baseName || 'Unnamed variant'}</span>
        <span className="variant-card__parent">{variant.namespace || 'Vehicle configuration'}</span>
      </button>)}
    </div>

    <section className={`variant-selection-panel${active ? ' is-open' : ''}`} aria-hidden={!active}>
      {active && activeDraft && <div className="variant-selection-panel__inner">
        <div className="variant-identity-row">
          <VariantThumbnail variant={active} onPreviewMember={onPreviewMember} className="variant-identity-row__thumb"/>
          <div className="variant-identity-row__copy">
            <InlineEditableField<string>
              id={`variant-${active.configPath}-name`}
              className="variant-identity-row__name"
              label="Name"
              value={activeDraft.configuration}
              locked={stale || writeBlocked}
              onDirtyChange={dirty => {
                const key = `variant:${activeKey}:configuration`
                if (dirty) dirtyFieldsRef.current.add(key)
                else dirtyFieldsRef.current.delete(key)
                reportDirty(key, dirty)
              }}
              onSave={identitySave}
              renderDisplay={value => <h3>{value.trim() || 'Unnamed variant'}</h3>}
              renderEditor={({ draft, onChange, id, labelId, describedBy, disabled, autoFocus, onKeyDown }) => <input
                id={id}
                value={draft}
                maxLength={160}
                placeholder="Unnamed variant"
                aria-describedby={describedBy}
                aria-labelledby={labelId}
                disabled={disabled}
                autoFocus={autoFocus}
                onChange={event => onChange(event.target.value)}
                onKeyDown={onKeyDown}
              />}
            />
            <span className="variant-identity-row__parent">Parent: {active.namespace || active.baseName || 'Vehicle configuration'}</span>
          </div>
          <button type="button" className="inspector-button inspector-button--quiet icon-button" onClick={() => guardedSelect(null)} aria-label="Close variant details" title="Close variant details"><Icon name="close" size={16}/></button>
        </div>
        <div className="variant-fields">
          {variantFields.map(field => <InlineEditableField<string>
            key={`${activeKey}-${field.key}`}
            id={`variant-${activeKey}-${field.key}`}
            className={`variant-field variant-field--${field.key}`}
            label={field.label}
            value={activeDraft[field.key]}
            multiline={field.multiline}
            locked={stale || writeBlocked}
            onDirtyChange={dirty => {
              const key = `variant:${activeKey}:${field.key}`
              if (dirty) dirtyFieldsRef.current.add(key)
              else dirtyFieldsRef.current.delete(key)
              reportDirty(key, dirty)
            }}
            onSave={value => saveVariantField(field.key, value)}
            renderDisplay={value => <span className="inspector-inline-value">{displayVariantValue(field.key, value)}</span>}
            renderEditor={({ draft, onChange, id, labelId, describedBy, disabled, autoFocus, onKeyDown }) => field.multiline ? <textarea
              id={id}
              value={draft}
              rows={4}
              maxLength={2000}
              aria-describedby={describedBy}
              aria-labelledby={labelId}
              disabled={disabled}
              autoFocus={autoFocus}
              onChange={event => onChange(event.target.value)}
              onKeyDown={onKeyDown}
            /> : <input
              id={id}
              value={draft}
              maxLength={160}
              aria-describedby={describedBy}
              aria-labelledby={labelId}
              disabled={disabled}
              autoFocus={autoFocus}
              onChange={event => onChange(event.target.value)}
              onKeyDown={onKeyDown}
            />}
          />)}
        </div>
      </div>}
    </section>
  </div>
}

function VariantThumbnail({
  variant,
  onPreviewMember,
  className,
}: {
  variant: Variant
  onPreviewMember: (memberPath: string) => Promise<ArchiveMemberPreview | null>
  className: string
}) {
  const [thumbnail, setThumbnail] = useState('')
  const thumbRef = useRef<HTMLSpanElement>(null)
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
    const element = thumbRef.current
    if (!element || typeof IntersectionObserver === 'undefined') {
      load()
      return () => { cancelled = true }
    }
    const observer = new IntersectionObserver(entries => {
      if (!entries.some(entry => entry.isIntersecting)) return
      observer.disconnect()
      load()
    }, { root: element.closest('.inspector__body'), rootMargin: '180px 0px' })
    observer.observe(element)
    return () => {
      cancelled = true
      observer.disconnect()
    }
  }, [thumbnailPath])

  return <span ref={thumbRef} className={className}>
    {thumbnail ? <img src={thumbnail} alt="" loading="lazy"/> : <Icon name="vehicle" size={26}/>}
  </span>
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

function displayVariantValue(key: VariantFieldKey, value: string): string {
  const trimmed = value.trim()
  if (trimmed) return trimmed
  if (key === 'description') return 'No description provided.'
  return '—'
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

function HistoryTab({
  history,
  loading,
  total,
}: {
  history: EntityDetail['history'] | null
  loading: boolean
  total: number
}) {
  const visible = history?.length ?? 0
  const capped = !loading && history !== null && total > visible
  return <div className="inspector-history-tab">
    <HistoryPanel history={history} loading={loading} />
    {capped && <p className="inspector-history-tab__cap" role="note">
      Showing latest {visible.toLocaleString()} of {total.toLocaleString()} history events.
    </p>}
  </div>
}


function historyTotal(detail: EntityDetail | null): number {
  if (!detail) return 0
  const visible = detail.history?.length ?? 0
  return Number.isFinite(detail.historyTotal) && detail.historyTotal >= 0
    ? Math.max(detail.historyTotal, visible)
    : visible
}

function normalizedScanVerdict(verdict: string): string {
  const value = verdict.trim().toLowerCase()
  return value || 'unscanned'
}

type SecurityScanState = 'unscanned' | 'scanning' | 'safe' | 'review' | 'threat' | 'scan_failed'

function securityScanState(item: LibraryItem): SecurityScanState {
  if (normalizedScanVerdict(item.healthStatus) === 'scanning') return 'scanning'
  if (!item.lastSecurityScanAt.trim()) return 'unscanned'
  const verdict = normalizedScanVerdict(item.lastSecurityScanVerdict)
  if (verdict === 'safe') return 'safe'
  if (verdict === 'review') return 'review'
  if (verdict === 'threat') return 'threat'
  if (verdict === 'broken' || verdict === 'error' || verdict === 'failed' || verdict === 'scan_failed') return 'scan_failed'
  return 'unscanned'
}

function securityScanStatus(item: LibraryItem, state: SecurityScanState): string {
  let label: string
  if (state === 'scanning') label = 'Scanning…'
  else if (state === 'safe') label = 'No threats found'
  else if (state === 'review' || state === 'threat') label = 'Threats found'
  else if (state === 'scan_failed') label = 'Scan failed'
  else label = 'Not scanned'
  return item.securityScanChanged && state !== 'scanning' && state !== 'unscanned'
    ? `${label} · Archive changed`
    : label
}

function scanStatusIcon(status: SecurityScanState): 'shield' | 'check' | 'warning' | 'error' | 'scan' {
  if (status === 'safe') return 'check'
  if (status === 'scanning') return 'scan'
  if (status === 'review') return 'warning'
  if (status === 'threat' || status === 'scan_failed') return 'error'
  return 'shield'
}
