import { createPortal } from 'react-dom'
import { useEffect, useId, useLayoutEffect, useRef, useState, type CSSProperties, type KeyboardEvent as ReactKeyboardEvent, type MouseEvent as ReactMouseEvent } from 'react'
import type { ModTag } from '../bindings/github.com/SignedAdam/beamng-mod-studio/models.js'
import { Icon, type IconName } from './icons'
import './TagEditor.css'

export interface TagEditorProps {
  assigned: ModTag[]
  tags: ModTag[]
  /** Stable identity of the selected mod. A changed value invalidates old persistence responses. */
  selectionKey?: string
  /** Persist one desired assignment and return the authoritative assigned tags for this mod. */
  onSet: (tagIDs: string[]) => Promise<ModTag[]>
  onCreate: (name: string, color: string, icon: string) => Promise<ModTag | null>
  onUpdateVisual: (tagID: string, color: string, icon: string) => Promise<void>
  onDelete: (tagID: string) => Promise<void>
  onError: (error: unknown) => void
  locked?: boolean
}

type TagIconName = Extract<IconName, 'tag' | 'vehicle' | 'map' | 'code' | 'files' | 'shield' | 'user'>
type MenuBusy = '' | 'save' | 'delete'

type NewTagEdit = {
  kind: 'new'
  value: string
  original: string
  color: string
  icon: TagIconName
}

type MenuState = {
  tag: ModTag
  x: number
  y: number
  color: string
  icon: TagIconName
  error: string
  busy: MenuBusy
}

type AssignmentStatus =
  | { kind: 'pending' }
  | { kind: 'error'; message: string }

type AssignmentRequest = {
  epoch: number
  targetIDs: string[]
  targetTags: ModTag[]
}

type FailedAssignment = {
  targetIDs: string[]
  targetTags: ModTag[]
}

interface AssignmentRuntime {
  selectionKey: string
  confirmed: ModTag[]
  optimistic: ModTag[]
  desiredIDs: string[] | null
  inFlight: AssignmentRequest | null
  statuses: Map<string, AssignmentStatus>
  pendingIDs: Set<string>
  failed: FailedAssignment | null
  /** Ignore a stale parent snapshot until it catches up with the direct response. */
  awaitingParentSignature: string | null
  lastPropSignature: string
  epoch: number
}

const TAG_COLORS = ['#f26522', '#3f93c5', '#47bd75', '#ffb12c', '#a978e5', '#e85d8f', '#7a8791'] as const
const TAG_ICONS: TagIconName[] = ['tag', 'vehicle', 'map', 'code', 'files', 'shield', 'user']
const FALLBACK_TAG_COLOR = '#7a8791'
const DEFAULT_SELECTION_KEY = '__tag-editor-selection__'

export function TagEditor({ assigned, tags, selectionKey, onSet, onCreate, onUpdateVisual, onDelete, onError, locked = false }: TagEditorProps) {
  const stableSelectionKey = selectionKey ?? DEFAULT_SELECTION_KEY
  const incomingSignature = tagIDsSignature(assigned)
  const runtimeRef = useRef<AssignmentRuntime | null>(null)
  if (!runtimeRef.current || runtimeRef.current.selectionKey !== stableSelectionKey) {
    runtimeRef.current = createRuntime(stableSelectionKey, assigned, incomingSignature)
  }
  const runtime = runtimeRef.current
  const [, setRevision] = useState(0)
  const [edit, setEdit] = useState<NewTagEdit | null>(null)
  const [editError, setEditError] = useState('')
  const [createBusy, setCreateBusy] = useState(false)
  const [contextMenu, setContextMenu] = useState<MenuState | null>(null)
  const [menuPosition, setMenuPosition] = useState({ left: 8, top: 8 })
  const helperId = useId()

  const lockedRef = useRef(locked)
  const onSetRef = useRef(onSet)
  const onCreateRef = useRef(onCreate)
  const onUpdateVisualRef = useRef(onUpdateVisual)
  const onDeleteRef = useRef(onDelete)
  const onErrorRef = useRef(onError)
  const createButtonRef = useRef<HTMLButtonElement>(null)
  const editInputRef = useRef<HTMLInputElement>(null)
  const menuRef = useRef<HTMLDivElement>(null)
  const menuOriginRef = useRef<HTMLElement | null>(null)
  const contextMenuRef = useRef<MenuState | null>(null)
  const previousEditKindRef = useRef<NewTagEdit['kind'] | null>(null)

  lockedRef.current = locked
  onSetRef.current = onSet
  onCreateRef.current = onCreate
  onUpdateVisualRef.current = onUpdateVisual
  onDeleteRef.current = onDelete
  onErrorRef.current = onError
  contextMenuRef.current = contextMenu

  const rerender = () => setRevision(value => value + 1)
  const isCurrentRuntime = (candidate: AssignmentRuntime) => runtimeRef.current === candidate && candidate.selectionKey === stableSelectionKey

  useEffect(() => {
    if (!isCurrentRuntime(runtime)) return
    if (runtime.inFlight || runtime.desiredIDs) return

    if (runtime.awaitingParentSignature) {
      if (incomingSignature === runtime.awaitingParentSignature) {
        runtime.awaitingParentSignature = null
        runtime.lastPropSignature = incomingSignature
        return
      }
      if (incomingSignature === runtime.lastPropSignature) return
      runtime.awaitingParentSignature = null
    }

    if (incomingSignature === runtime.lastPropSignature) return
    runtime.confirmed = cloneTags(assigned)
    runtime.optimistic = cloneTags(assigned)
    runtime.lastPropSignature = incomingSignature
    runtime.failed = null
    runtime.pendingIDs.clear()
    for (const [tagID, status] of runtime.statuses) {
      if (status.kind === 'pending') runtime.statuses.delete(tagID)
    }
    rerender()
  }, [assigned, incomingSignature, runtime, stableSelectionKey])
  useEffect(() => {
    setEdit(null)
    setEditError('')
    if (contextMenuRef.current) {
      menuOriginRef.current = null
      setContextMenu(null)
    }
  }, [stableSelectionKey])


  useEffect(() => {
    if (locked || !isCurrentRuntime(runtime) || runtime.inFlight || !runtime.desiredIDs) return
    scheduleAssignment(runtime)
  }, [locked, runtime, stableSelectionKey])

  useEffect(() => {
    const nextKind = edit?.kind ?? null
    const wasNew = previousEditKindRef.current === 'new'
    previousEditKindRef.current = nextKind
    if (nextKind === 'new') {
      const input = editInputRef.current
      if (!input) return
      input.focus()
      input.select()
      return
    }
    if (wasNew) createButtonRef.current?.focus()
  }, [edit])

  useLayoutEffect(() => {
    if (!contextMenu || !menuRef.current) return
    const menu = menuRef.current
    const rect = menu.getBoundingClientRect()
    const margin = 8
    let left = contextMenu.x
    let top = contextMenu.y
    if (left + rect.width > window.innerWidth - margin) left = contextMenu.x - rect.width
    if (top + rect.height > window.innerHeight - margin) top = contextMenu.y - rect.height
    left = clamp(left, margin, Math.max(margin, window.innerWidth - rect.width - margin))
    top = clamp(top, margin, Math.max(margin, window.innerHeight - rect.height - margin))
    setMenuPosition({ left, top })
  }, [contextMenu?.tag.id, contextMenu?.x, contextMenu?.y, contextMenu?.error, contextMenu?.busy])

  useEffect(() => {
    if (!contextMenu) return
    const menu = menuRef.current
    if (menu) focusInitialMenuControl(menu)
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        event.preventDefault()
        closeContextMenu()
        return
      }
      if (event.key !== 'Tab' || !menuRef.current) return
      const controls = menuControls(menuRef.current)
      if (controls.length === 0) return
      const current = document.activeElement
      const index = controls.indexOf(current as HTMLElement)
      const nextIndex = event.shiftKey
        ? (index <= 0 ? controls.length - 1 : index - 1)
        : (index === controls.length - 1 ? 0 : index + 1)
      event.preventDefault()
      controls[nextIndex]?.focus()
    }
    document.addEventListener('keydown', onKeyDown)
    return () => document.removeEventListener('keydown', onKeyDown)
  }, [contextMenu?.tag.id])

  const scheduleAssignment = (candidate: AssignmentRuntime) => {
    if (!isCurrentRuntime(candidate) || candidate.inFlight || !candidate.desiredIDs || lockedRef.current) return
    const targetIDs = uniqueIDs(candidate.desiredIDs)
    candidate.desiredIDs = null
    const request: AssignmentRequest = {
      epoch: candidate.epoch + 1,
      targetIDs,
      targetTags: materializeTags(targetIDs, candidate.optimistic, candidate.confirmed, tags),
    }
    candidate.epoch = request.epoch
    candidate.inFlight = request
    markPending(candidate, unionIDs(diffIDs(idsOf(candidate.confirmed), targetIDs), diffIDs(idsOf(candidate.confirmed), idsOf(candidate.optimistic))))
    rerender()

    void (async () => {
      try {
        const authoritative = await onSetRef.current(targetIDs)
        if (!Array.isArray(authoritative)) throw new Error('Tag assignment did not return authoritative tags')
        if (!isCurrentRuntime(candidate) || candidate.inFlight !== request || candidate.epoch !== request.epoch) return

        candidate.inFlight = null
        candidate.confirmed = cloneTags(authoritative)
        candidate.awaitingParentSignature = tagIDsSignature(authoritative)
        const queuedIDs = candidate.desiredIDs
        if (queuedIDs) {
          const normalizedQueued = uniqueIDs(queuedIDs)
          candidate.optimistic = materializeTags(normalizedQueued, candidate.optimistic, candidate.confirmed, tags)
          if (sameIDs(normalizedQueued, idsOf(authoritative))) {
            candidate.desiredIDs = null
            clearPending(candidate)
            candidate.optimistic = cloneTags(authoritative)
          } else {
            markPending(candidate, unionIDs(diffIDs(idsOf(candidate.confirmed), request.targetIDs), diffIDs(idsOf(candidate.confirmed), normalizedQueued)))
            if (!lockedRef.current) scheduleAssignment(candidate)
          }
        } else {
          candidate.optimistic = cloneTags(authoritative)
          clearPending(candidate)
          candidate.failed = null
        }
        rerender()
      } catch (error) {
        if (!isCurrentRuntime(candidate) || candidate.inFlight !== request || candidate.epoch !== request.epoch) return
        const queuedIDs = candidate.desiredIDs ?? []
        const affectedIDs = unionIDs(
          diffIDs(idsOf(candidate.confirmed), request.targetIDs),
          diffIDs(idsOf(candidate.confirmed), queuedIDs),
        )
        candidate.inFlight = null
        candidate.desiredIDs = null
        candidate.optimistic = cloneTags(candidate.confirmed)
        candidate.pendingIDs.clear()
        candidate.failed = { targetIDs: request.targetIDs, targetTags: request.targetTags }
        for (const tagID of affectedIDs) candidate.statuses.set(tagID, { kind: 'error', message: errorMessage(error) })
        rerender()
        onErrorRef.current(error)
      }
    })()
  }

  const setDesiredAssignment = (next: ModTag[]) => {
    if (!isCurrentRuntime(runtime)) return
    runtime.optimistic = cloneTags(next)
    runtime.desiredIDs = uniqueIDs(idsOf(next))
    runtime.failed = null
    const activeDiff = runtime.inFlight ? diffIDs(idsOf(runtime.confirmed), runtime.inFlight.targetIDs) : []
    markPending(runtime, unionIDs(activeDiff, diffIDs(idsOf(runtime.confirmed), runtime.desiredIDs)))
    if (!runtime.inFlight) scheduleAssignment(runtime)
    rerender()
  }

  const toggle = (tagID: string) => {
    if (locked || !isCurrentRuntime(runtime)) return
    const current = runtime.optimistic
    const next = current.some(tag => tag.id === tagID)
      ? current.filter(tag => tag.id !== tagID)
      : [...current, findTag(tagID, tags, current) ?? { id: tagID, name: tagID, color: FALLBACK_TAG_COLOR, icon: 'tag', modCount: 0 }]
    runtime.statuses.delete(tagID)
    setDesiredAssignment(next)
  }

  const beginNew = () => {
    if (locked || createBusy) return
    const names = new Set(tags.map(tag => tag.name.toLocaleLowerCase()))
    let index = 1
    while (names.has(`new_tag_${index}`)) index += 1
    const name = `new_tag_${index}`
    menuOriginRef.current = createButtonRef.current
    setContextMenu(null)
    setEditError('')
    setEdit({ kind: 'new', value: name, original: name, color: TAG_COLORS[1], icon: 'tag' })
  }

  const cancelNew = () => {
    if (createBusy) return
    setEdit(null)
    setEditError('')
  }

  const commitNew = async () => {
    if (!edit || lockedRef.current || createBusy) return
    const draft = edit
    const name = draft.value.trim() || draft.original
    setCreateBusy(true)
    setEditError('')
    try {
      const tag = await onCreateRef.current(name, safeTagColor(draft.color), safeTagIcon(draft.icon))
      if (!tag) {
        throw new Error('Tag could not be created')
      }
      if (!isCurrentRuntime(runtime) || lockedRef.current) return
      const next = runtime.optimistic.some(current => current.id === tag.id)
        ? runtime.optimistic
        : [...runtime.optimistic, tag]
      setDesiredAssignment(next)
      setEdit(null)
    } catch (error) {
      if (!isCurrentRuntime(runtime)) return
      setEditError(errorMessage(error))
      onErrorRef.current(error)
    } finally {
      setCreateBusy(false)
    }
  }

  const openContextMenu = (event: ReactMouseEvent<HTMLButtonElement>, tag: ModTag) => {
    event.preventDefault()
    menuOriginRef.current = event.currentTarget
    setMenuPosition({ left: event.clientX, top: event.clientY })
    setContextMenu({
      tag,
      x: event.clientX,
      y: event.clientY,
      color: safeTagColor(tag.color),
      icon: safeTagIcon(tag.icon),
      error: '',
      busy: '',
    })
  }

  const openKeyboardMenu = (event: ReactKeyboardEvent<HTMLButtonElement>, tag: ModTag) => {
    if (!(event.key === 'ContextMenu' || (event.key === 'F10' && event.shiftKey))) return
    event.preventDefault()
    const rect = event.currentTarget.getBoundingClientRect()
    menuOriginRef.current = event.currentTarget
    setMenuPosition({ left: rect.left, top: rect.bottom })
    setContextMenu({
      tag,
      x: rect.left,
      y: rect.bottom,
      color: safeTagColor(tag.color),
      icon: safeTagIcon(tag.icon),
      error: '',
      busy: '',
    })
  }

  const closeContextMenu = () => {
    const origin = menuOriginRef.current
    menuOriginRef.current = null
    setContextMenu(null)
    if (origin?.isConnected) {
      origin.focus()
    } else {
      createButtonRef.current?.focus()
    }
  }

  const updateMenu = (update: Partial<MenuState>) => {
    setContextMenu(current => current ? { ...current, ...update } : current)
  }

  const saveAppearance = async () => {
    const menu = contextMenuRef.current
    if (!menu || lockedRef.current || menu.busy) return
    const tagID = menu.tag.id
    const color = safeTagColor(menu.color)
    const icon = safeTagIcon(menu.icon)
    updateMenu({ busy: 'save', error: '' })
    try {
      await onUpdateVisualRef.current(tagID, color, icon)
      const current = contextMenuRef.current
      if (!current || current.tag.id !== tagID) return
      if (lockedRef.current) {
        updateMenu({ busy: '', error: '' })
        return
      }
      closeContextMenu()
    } catch (error) {
      if (contextMenuRef.current?.tag.id !== tagID) return
      updateMenu({ busy: '', error: errorMessage(error) })
      onErrorRef.current(error)
    }
  }

  const deleteTag = async () => {
    const menu = contextMenuRef.current
    if (!menu || lockedRef.current || menu.busy) return
    const count = Math.max(0, Number.isFinite(menu.tag.modCount) ? menu.tag.modCount : 0)
    const message = count === 0
      ? 'Delete this tag?'
      : count === 1
        ? 'Are you sure? This tag is used on 1 mod. It will be removed from that mod.'
        : `Are you sure? This tag is used on ${String(count)} mods. It will be removed from each one.`
    if (!window.confirm(message)) return

    const tagID = menu.tag.id
    updateMenu({ busy: 'delete', error: '' })
    try {
      await onDeleteRef.current(tagID)
      if (contextMenuRef.current?.tag.id === tagID) closeContextMenu()
    } catch (error) {
      if (contextMenuRef.current?.tag.id !== tagID) return
      updateMenu({ busy: '', error: errorMessage(error) })
      onErrorRef.current(error)
    }
  }

  const retryAssignment = (tagID: string) => {
    if (locked || !isCurrentRuntime(runtime) || runtime.inFlight || !runtime.failed) return
    if (runtime.statuses.get(tagID)?.kind !== 'error') return
    const failed = runtime.failed
    runtime.statuses.delete(tagID)
    runtime.optimistic = cloneTags(failed.targetTags)
    runtime.desiredIDs = uniqueIDs(failed.targetIDs)
    runtime.failed = null
    markPending(runtime, diffIDs(idsOf(runtime.confirmed), runtime.desiredIDs))
    scheduleAssignment(runtime)
    rerender()
  }

  const allTags = mergeTagLists(tags, runtime.optimistic)
  const assignedIDs = new Set(runtime.optimistic.map(tag => tag.id))
  const editor = edit && <form className="tag-editor__creator" onSubmit={event => { event.preventDefault(); void commitNew() }}>
    <div className="tag-editor__creator-header">
      <div>
        <strong>Create new tag</strong>
        <small>Use a clear name that is easy to scan in the library.</small>
      </div>
    </div>
    <label className="tag-editor__name-field">
      <span>Name</span>
      <input
        ref={editInputRef}
        value={edit.value}
        maxLength={80}
        aria-label="Name new tag"
        disabled={createBusy}
        onChange={event => setEdit(current => current ? { ...current, value: event.target.value } : current)}
        onKeyDown={event => { if (event.key === 'Escape') { event.preventDefault(); cancelNew() } }}
      />
    </label>
    <div className="tag-editor__creator-options">
      <div className="tag-editor__option-group">
        <span>Color</span>
        <div className="tag-editor__color-options">
          {TAG_COLORS.map(color => <button type="button" key={color} className={`tag-editor__color${safeTagColor(edit.color) === color ? ' is-selected' : ''}`} style={{ '--tag-color': color } as CSSProperties} aria-label={`Use ${color}`} aria-pressed={safeTagColor(edit.color) === color} onClick={() => setEdit(current => current ? { ...current, color } : current)} disabled={createBusy}><i /></button>)}
          <label className="tag-editor__custom-color">
            <input type="color" value={safeTagColor(edit.color)} aria-label="Custom tag color" disabled={createBusy} onChange={event => setEdit(current => current ? { ...current, color: event.target.value } : current)} />
            <span>Custom</span>
          </label>
        </div>
      </div>
      <div className="tag-editor__option-group">
        <span>Icon</span>
        <div className="tag-editor__icon-options">
          {TAG_ICONS.map(icon => <button type="button" key={icon} className={edit.icon === icon ? 'is-selected' : ''} aria-label={`Use ${icon} icon`} aria-pressed={edit.icon === icon} onClick={() => setEdit(current => current ? { ...current, icon } : current)} disabled={createBusy}><Icon name={icon} size={15} /></button>)}
        </div>
      </div>
    </div>
    {editError && <div className="tag-editor__creator-error" role="alert"><Icon name="error" size={13} /><span>{editError}</span></div>}
    <footer>
      <button type="button" className="text-button" onClick={cancelNew} disabled={createBusy}>Cancel</button>
      <button type="submit" className="button button--primary" disabled={locked || createBusy}>{createBusy ? 'Creating' : 'Create tag'}</button>
    </footer>
  </form>

  const chip = (tag: ModTag) => {
    const isAssigned = assignedIDs.has(tag.id)
    const status = runtime.statuses.get(tag.id)
    const color = safeTagColor(tag.color)
    const icon = safeTagIcon(tag.icon)
    return <span className="tag-editor__chip-wrap" key={tag.id}>
      <button
        type="button"
        className={`tag-editor__chip${isAssigned ? ' is-assigned' : ''}${status?.kind === 'pending' ? ' is-pending' : ''}${status?.kind === 'error' ? ' has-error' : ''}`}
        style={{ '--tag-color': color } as CSSProperties}
        aria-pressed={isAssigned}
        aria-busy={status?.kind === 'pending' || undefined}
        aria-label={`${tag.name}, ${isAssigned ? 'assigned' : 'not assigned'}`}
        title={`${isAssigned ? 'Remove' : 'Assign'} ${tag.name}`}
        disabled={locked}
        onClick={() => toggle(tag.id)}
        onContextMenu={event => openContextMenu(event, tag)}
        onKeyDown={event => openKeyboardMenu(event, tag)}
      >
        <Icon name={icon} size={13} />
        <span>{tag.name}</span>
        <small>{tag.modCount.toLocaleString()}</small>
        <span className={`tag-editor__chip-status${status?.kind === 'pending' ? ' is-pending' : ''}${status?.kind === 'error' ? ' is-error' : ''}`} title={status?.kind === 'error' ? status.message : undefined}>
          {status?.kind === 'pending' && <span className="tag-editor__pending-dot" aria-hidden="true" />}
          {status?.kind === 'error' && <><Icon name="error" size={12} /><span className="tag-editor__visually-hidden">{status.message}</span></>}
        </span>
      </button>
      <span className="tag-editor__retry-slot">
        {status?.kind === 'error' && <button type="button" className="tag-editor__retry" aria-label={`Retry saving ${tag.name} assignment`} title={`Retry saving ${tag.name} assignment`} disabled={locked} onClick={() => retryAssignment(tag.id)}><Icon name="refresh" size={12} /></button>}
      </span>
    </span>
  }

  const customColors = uniqueColors(allTags.map(tag => safeTagColor(tag.color)).filter(color => !TAG_COLORS.includes(color as typeof TAG_COLORS[number])))
  if (contextMenu && !customColors.includes(safeTagColor(contextMenu.color)) && !TAG_COLORS.includes(safeTagColor(contextMenu.color) as typeof TAG_COLORS[number])) customColors.push(safeTagColor(contextMenu.color))
  const saveDisabled = !contextMenu || locked || contextMenu.busy !== '' || (safeTagColor(contextMenu.color) === safeTagColor(contextMenu.tag.color) && safeTagIcon(contextMenu.icon) === safeTagIcon(contextMenu.tag.icon))

  return <div className="tag-editor">
    <header>
      <small id={helperId}>Click to assign or remove tags</small>
    </header>

    <div className="tag-editor__group">
      <div className="tag-editor__chips" role="group" aria-label="Tags" aria-describedby={helperId}>
        <button ref={createButtonRef} type="button" className="tag-editor__chip tag-editor__new-chip" aria-label="Create a new tag" title="Create a new tag" disabled={locked || createBusy} onClick={beginNew}>
          <span>+ New tag</span>
          <span className="tag-editor__chip-status" aria-hidden="true" />
        </button>
        {editor}
        {allTags.map(chip)}
        {allTags.length === 0 && <span className="tag-editor__empty">No tags yet</span>}
      </div>
    </div>
    {contextMenu && typeof document !== 'undefined' && createPortal(
      <div className="tag-editor__menu-layer" onPointerDown={event => { if (event.target === event.currentTarget) closeContextMenu() }}>
        <div ref={menuRef} className="tag-editor__menu" role="dialog" aria-modal="true" aria-label="Tag appearance" style={{ left: menuPosition.left, top: menuPosition.top }} onContextMenu={event => event.preventDefault()} onPointerDown={event => event.stopPropagation()}>
          <div className="tag-editor__menu-actions">
            <button type="button" className="tag-editor__delete" aria-label="Delete tag" title="Delete tag" disabled={locked || contextMenu.busy !== ''} onClick={() => void deleteTag()}><Icon name="trash" size={14} /></button>
          </div>

          <div className="tag-editor__icon-grid" role="radiogroup" aria-label="Tag icons">
            {TAG_ICONS.map(icon => <button key={icon} type="button" role="radio" className={contextMenu.icon === icon ? 'is-selected' : ''} aria-label={`Use ${icon} icon`} aria-checked={contextMenu.icon === icon} onClick={() => updateMenu({ icon: icon, error: '' })} disabled={contextMenu.busy !== ''}><Icon name={icon} size={15} /></button>)}
          </div>

          <div className="tag-editor__swatch-row tag-editor__swatch-row--built-in" role="radiogroup" aria-label="Built-in tag colors">
            {TAG_COLORS.map(color => <button key={color} type="button" role="radio" className={safeTagColor(contextMenu.color) === color ? 'is-selected' : ''} style={{ '--tag-color': color } as CSSProperties} aria-label={`Use ${color} tag color`} aria-checked={safeTagColor(contextMenu.color) === color} onClick={() => updateMenu({ color, error: '' })} disabled={contextMenu.busy !== ''}><i /></button>)}
          </div>

          {customColors.length > 0 && <div className="tag-editor__swatch-row tag-editor__swatch-row--custom" role="radiogroup" aria-label="Custom tag colors">
            {customColors.map(color => <button key={color} type="button" role="radio" className={safeTagColor(contextMenu.color) === color ? 'is-selected' : ''} style={{ '--tag-color': color } as CSSProperties} aria-label={`Use ${color} tag color`} aria-checked={safeTagColor(contextMenu.color) === color} onClick={() => updateMenu({ color, error: '' })} disabled={contextMenu.busy !== ''}><i /></button>)}
          </div>}

          <div className="tag-editor__custom-row">
            <label className="tag-editor__custom-picker">
              <input type="color" aria-label="Custom color…" value={safeTagColor(contextMenu.color)} disabled={contextMenu.busy !== ''} onChange={event => updateMenu({ color: event.target.value, error: '' })} />
              <span>Custom color…</span>
            </label>
          </div>

          {contextMenu.error && <div className="tag-editor__menu-error" role="alert" aria-live="polite"><Icon name="error" size={13} /><span>{contextMenu.error}</span><button type="button" onClick={() => void saveAppearance()} disabled={locked || contextMenu.busy !== ''}>Retry</button></div>}

          <footer className="tag-editor__menu-footer">
            <button type="button" className="text-button" onClick={closeContextMenu}>Cancel</button>
            <button type="button" className="button button--primary" onClick={() => void saveAppearance()} disabled={saveDisabled}>Save</button>
          </footer>
        </div>
      </div>,
      document.body,
    )}
  </div>
}

function createRuntime(selectionKey: string, assigned: ModTag[], incomingSignature: string): AssignmentRuntime {
  return {
    selectionKey,
    confirmed: cloneTags(assigned),
    optimistic: cloneTags(assigned),
    desiredIDs: null,
    inFlight: null,
    statuses: new Map(),
    pendingIDs: new Set(),
    failed: null,
    awaitingParentSignature: null,
    lastPropSignature: incomingSignature,
    epoch: 0,
  }
}

function mergeTagLists(tags: ModTag[], assigned: ModTag[]): ModTag[] {
  const result: ModTag[] = []
  const seen = new Set<string>()
  for (const tag of tags) {
    if (seen.has(tag.id)) continue
    seen.add(tag.id)
    result.push(tag)
  }
  for (const tag of assigned) {
    if (seen.has(tag.id)) continue
    seen.add(tag.id)
    result.push(tag)
  }
  return result
}

function materializeTags(ids: string[], preferred: ModTag[], fallback: ModTag[], available: ModTag[]): ModTag[] {
  const byID = new Map<string, ModTag>()
  for (const tag of available) byID.set(tag.id, tag)
  for (const tag of fallback) byID.set(tag.id, tag)
  for (const tag of preferred) byID.set(tag.id, tag)
  return uniqueIDs(ids).map(id => byID.get(id)).filter((tag): tag is ModTag => Boolean(tag)).map(tag => ({ ...tag }))
}

function findTag(id: string, ...sources: ModTag[][]): ModTag | undefined {
  for (const source of sources) {
    const found = source.find(tag => tag.id === id)
    if (found) return found
  }
  return undefined
}

function idsOf(tags: ModTag[]): string[] {
  return uniqueIDs(tags.map(tag => tag.id))
}

function uniqueIDs(ids: string[]): string[] {
  return [...new Set(ids)]
}

function tagIDsSignature(tags: ModTag[]): string {
  return JSON.stringify([...new Set(tags.map(tag => tag.id))].sort())
}

function sameIDs(left: string[], right: string[]): boolean {
  return JSON.stringify([...new Set(left)].sort()) === JSON.stringify([...new Set(right)].sort())
}

function diffIDs(left: string[], right: string[]): string[] {
  const leftIDs = new Set(left)
  const rightIDs = new Set(right)
  const result = new Set<string>()
  for (const id of leftIDs) if (!rightIDs.has(id)) result.add(id)
  for (const id of rightIDs) if (!leftIDs.has(id)) result.add(id)
  return [...result]
}

function unionIDs(...groups: string[][]): string[] {
  const result = new Set<string>()
  for (const group of groups) for (const id of group) result.add(id)
  return [...result]
}

function markPending(runtime: AssignmentRuntime, ids: string[]) {
  runtime.pendingIDs = new Set(ids)
  for (const id of ids) runtime.statuses.set(id, { kind: 'pending' })
}

function clearPending(runtime: AssignmentRuntime) {
  const pending = runtime.pendingIDs
  runtime.pendingIDs = new Set()
  for (const id of pending) {
    if (runtime.statuses.get(id)?.kind === 'pending') runtime.statuses.delete(id)
  }
}

function cloneTags(tags: ModTag[]): ModTag[] {
  return tags.map(tag => ({ ...tag }))
}

function uniqueColors(colors: string[]): string[] {
  return [...new Set(colors)]
}

function clamp(value: number, minimum: number, maximum: number): number {
  return Math.min(Math.max(value, minimum), maximum)
}

function safeTagColor(value: string | undefined): string {
  return typeof value === 'string' && /^#[0-9a-f]{6}$/i.test(value) ? value.toLowerCase() : FALLBACK_TAG_COLOR
}

function safeTagIcon(value: string | undefined): TagIconName {
  return TAG_ICONS.includes(value as TagIconName) ? value as TagIconName : 'tag'
}

function errorMessage(error: unknown): string {
  const message = error instanceof Error ? error.message : typeof error === 'string' ? error : ''
  const normalized = message.trim()
  if (!normalized) return 'Could not save tag assignment'
  return normalized.length > 140 ? `${normalized.slice(0, 137)}…` : normalized
}

function menuControls(menu: HTMLElement): HTMLElement[] {
  return [...menu.querySelectorAll<HTMLElement>('button:not(:disabled), input:not(:disabled), [tabindex]:not([tabindex="-1"]):not([aria-disabled="true"])')]
}

function focusInitialMenuControl(menu: HTMLElement) {
  const selected = menu.querySelector<HTMLElement>('[aria-checked="true"]:not(:disabled)')
  const cancel = menu.querySelector<HTMLElement>('.tag-editor__menu-footer .text-button:not(:disabled)')
  const controls = menuControls(menu)
  const initial = selected ?? cancel ?? controls[0]
  if (initial) initial.focus()
}
