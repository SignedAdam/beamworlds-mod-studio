import { useEffect, useRef, useState, type CSSProperties, type MouseEvent as ReactMouseEvent } from 'react'
import type { ModTag } from '../bindings/github.com/SignedAdam/beamng-mod-studio/models.js'
import { Icon, type IconName } from './icons'

interface TagEditorProps {
  assigned: ModTag[]
  tags: ModTag[]
  onSet: (tagIDs: string[]) => Promise<void>
  onCreate: (name: string, color: string, icon: string) => Promise<ModTag | null>
  onUpdateVisual: (tagID: string, color: string, icon: string) => Promise<void>
  onRename: (tagID: string, name: string) => Promise<void>
  onDelete: (tagID: string) => Promise<void>
  onError: (error: unknown) => void
}

type TagIconName = Extract<IconName, 'tag' | 'vehicle' | 'map' | 'code' | 'files' | 'shield' | 'user'>

type TagEdit =
  | { kind: 'new'; value: string; original: string; color: string; icon: TagIconName }
  | { kind: 'rename'; tag: ModTag; value: string; original: string; color: string; icon: TagIconName }
  | { kind: 'visual'; tag: ModTag; value: string; original: string; color: string; icon: TagIconName }

interface TagContextMenu {
  tag: ModTag
  x: number
  y: number
}

const TAG_COLORS = ['#f26522', '#3f93c5', '#47bd75', '#ffb12c', '#a978e5', '#e85d8f', '#7a8791'] as const
const TAG_ICONS: TagIconName[] = ['tag', 'vehicle', 'map', 'code', 'files', 'shield', 'user']
const FALLBACK_TAG_COLOR = '#7a8791'

export function TagEditor({ assigned, tags, onSet, onCreate, onUpdateVisual, onRename, onDelete, onError }: TagEditorProps) {
  const [edit, setEdit] = useState<TagEdit | null>(null)
  const [contextMenu, setContextMenu] = useState<TagContextMenu | null>(null)
  const [busy, setBusy] = useState('')
  const editInput = useRef<HTMLInputElement>(null)
  const tagsByID = new Map(tags.map(tag => [tag.id, tag]))
  const assignedTags = assigned.map(tag => tagsByID.get(tag.id) ?? tag)
  const assignedIDs = new Set(assignedTags.map(tag => tag.id))
  const available = tags.filter(tag => !assignedIDs.has(tag.id))
  const editKey = edit ? edit.kind === 'new' ? 'new' : `${edit.kind}:${edit.tag.id}` : ''

  useEffect(() => {
    if (!editKey) return
    const input = editInput.current
    if (!input) return
    input.focus()
    if (edit?.kind === 'new') input.select()
    else input.setSelectionRange(input.value.length, input.value.length)
  }, [editKey])

  useEffect(() => {
    if (!contextMenu) return
    const close = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setContextMenu(null)
    }
    window.addEventListener('keydown', close)
    return () => window.removeEventListener('keydown', close)
  }, [contextMenu])

  const toggle = async (tagID: string) => {
    const next = assignedIDs.has(tagID)
      ? assignedTags.filter(tag => tag.id !== tagID).map(tag => tag.id)
      : [...assignedTags.map(tag => tag.id), tagID]
    setBusy(tagID)
    try {
      await onSet(next)
    } catch (error) {
      onError(error)
    } finally {
      setBusy('')
    }
  }

  const beginNew = () => {
    const names = new Set(tags.map(tag => tag.name.toLocaleLowerCase()))
    let index = 1
    while (names.has(`new_tag_${index}`)) index += 1
    const name = `new_tag_${index}`
    setContextMenu(null)
    setEdit({ kind: 'new', value: name, original: name, color: TAG_COLORS[1], icon: 'tag' })
  }

  const beginRename = (tag: ModTag) => {
    setContextMenu(null)
    setEdit({ kind: 'rename', tag, value: tag.name, original: tag.name, color: safeTagColor(tag.color), icon: safeTagIcon(tag.icon) })
  }

  const beginVisualEdit = (tag: ModTag) => {
    setContextMenu(null)
    setEdit({ kind: 'visual', tag, value: tag.name, original: tag.name, color: safeTagColor(tag.color), icon: safeTagIcon(tag.icon) })
  }

  const commitEdit = async () => {
    if (!edit || busy) return
    const name = edit.value.trim() || edit.original
    if (edit.kind === 'new') {
      setBusy('create')
      try {
        const tag = await onCreate(name, safeTagColor(edit.color), safeTagIcon(edit.icon))
        if (!tag) return
        setEdit(null)
        await onSet([...assignedTags.map(current => current.id), tag.id])
      } catch (error) {
        onError(error)
      } finally {
        setBusy('')
      }
      return
    }
    setBusy(edit.tag.id)
    try {
      if (name !== edit.original) await onRename(edit.tag.id, name)
      if (edit.kind === 'visual' && (safeTagColor(edit.color) !== safeTagColor(edit.tag.color) || safeTagIcon(edit.icon) !== safeTagIcon(edit.tag.icon))) {
        await onUpdateVisual(edit.tag.id, safeTagColor(edit.color), safeTagIcon(edit.icon))
      }
      setEdit(null)
    } catch (error) {
      onError(error)
    } finally {
      setBusy('')
    }
  }

  const remove = async (tag: ModTag) => {
    setContextMenu(null)
    if (!window.confirm(`Delete tag “${tag.name}”? It will be removed from every mod.`)) return
    setBusy(tag.id)
    try {
      await onDelete(tag.id)
      if (edit?.kind !== 'new' && edit?.tag.id === tag.id) setEdit(null)
    } catch (error) {
      onError(error)
    } finally {
      setBusy('')
    }
  }

  const openContextMenu = (event: ReactMouseEvent, tag: ModTag) => {
    event.preventDefault()
    setContextMenu({
      tag,
      x: Math.min(event.clientX, Math.max(8, window.innerWidth - 214)),
      y: Math.min(event.clientY, Math.max(8, window.innerHeight - 146)),
    })
  }


  const editor = edit && <form className="tag-editor__creator" onSubmit={event => { event.preventDefault(); void commitEdit() }}>
    <div className="tag-editor__creator-header">
      <div><strong>{edit.kind === 'new' ? 'Create new tag' : edit.kind === 'visual' ? 'Edit tag appearance' : 'Rename tag'}</strong><small>{edit.kind === 'visual' ? 'Choose a color and icon for this tag.' : 'Use a clear name that is easy to scan in the library.'}</small></div>
      <button type="button" className="icon-button" aria-label="Cancel tag editor" onClick={() => setEdit(null)} disabled={busy !== ''}><Icon name="close" size={14}/></button>
    </div>
    <label className="tag-editor__name-field"><span>Name</span><input ref={editInput} value={edit.value} maxLength={80} readOnly={edit.kind === 'visual'} aria-label={edit.kind === 'new' ? 'Name new tag' : 'Tag name'} disabled={busy !== ''} onChange={event => setEdit(current => current ? { ...current, value: event.target.value } : current)} onKeyDown={event => { if (event.key === 'Escape') { event.preventDefault(); setEdit(null) } }}/></label>
    {edit.kind !== 'rename' && <div className="tag-editor__creator-options">
      <div className="tag-editor__option-group"><span>Color</span><div className="tag-editor__color-options">{TAG_COLORS.map(color => <button type="button" key={color} className={`tag-editor__color${safeTagColor(edit.color) === color ? ' is-selected' : ''}`} style={{ '--tag-color': color } as CSSProperties} aria-label={`Use ${color}`} aria-pressed={safeTagColor(edit.color) === color} onClick={() => setEdit(current => current ? { ...current, color } : current)} disabled={busy !== ''}><i/></button>)}<label className="tag-editor__custom-color"><input type="color" value={safeTagColor(edit.color)} aria-label="Custom tag color" onChange={event => setEdit(current => current ? { ...current, color: event.target.value } : current)}/><span>Custom</span></label></div></div>
      <div className="tag-editor__option-group"><span>Icon</span><div className="tag-editor__icon-options">{TAG_ICONS.map(icon => <button type="button" key={icon} className={edit.icon === icon ? 'is-selected' : ''} aria-label={`Use ${icon} icon`} aria-pressed={edit.icon === icon} onClick={() => setEdit(current => current ? { ...current, icon } : current)} disabled={busy !== ''}><Icon name={icon} size={15}/></button>)}</div></div>
    </div>}
    <footer><button type="button" className="text-button" onClick={() => setEdit(null)} disabled={busy !== ''}>Cancel</button><button type="submit" className="button button--primary" disabled={busy !== ''}>{busy === 'create' ? 'Creating' : edit.kind === 'new' ? 'Create tag' : 'Save changes'}</button></footer>
  </form>

  const chip = (tag: ModTag, isAssigned: boolean) => {
    if (edit?.kind !== 'new' && edit?.tag.id === tag.id) return <span key={tag.id}>{editor}</span>
    const color = safeTagColor(tag.color)
    const icon = safeTagIcon(tag.icon)
    return <button
      type="button"
      key={tag.id}
      className={`tag-editor__chip${isAssigned ? ' is-assigned' : ''}`}
      style={{ '--tag-color': color } as CSSProperties}
      aria-pressed={isAssigned}
      title={`${isAssigned ? 'Remove' : 'Assign'} ${tag.name}. Right-click for tag actions.`}
      disabled={busy !== ''}
      onClick={() => void toggle(tag.id)}
      onContextMenu={event => openContextMenu(event, tag)}
    >
      <Icon name={icon} size={13}/>
      <span>{tag.name}</span>
      <small>{tag.modCount.toLocaleString()}</small>
    </button>
  }

  return <div className="tag-editor">
    <header>
      <div><small>Click a tag to assign or remove it. Right-click a tag for rename, appearance, and delete actions.</small></div>
    </header>

    <div className="tag-editor__group">
      <strong className="tag-editor__subsection-title">Assigned tags</strong>
      <div className="tag-editor__chips">
        {assignedTags.length === 0 ? <span className="tag-editor__empty">No tags assigned</span> : assignedTags.map(tag => chip(tag, true))}
      </div>
    </div>

    <div className="tag-editor__group">
      <div className="tag-editor__available-heading">
        <strong className="tag-editor__subsection-title">Available tags</strong>
        {!edit && <button type="button" className="tag-editor__create" disabled={busy !== ''} onClick={beginNew}><span aria-hidden="true">+</span> Create new tag…</button>}
      </div>
      {edit?.kind === 'new' && editor}
      <div className="tag-editor__chips">
        {available.map(tag => chip(tag, false))}
        {available.length === 0 && !edit && <span className="tag-editor__empty">All tags are assigned</span>}
      </div>
    </div>

    {contextMenu && <>
      <div className="tag-editor__context-backdrop" onPointerDown={() => setContextMenu(null)}/>
      <div className="context-menu tag-editor__context-menu" role="menu" style={{ left: contextMenu.x, top: contextMenu.y }} onContextMenu={event => event.preventDefault()}>
        <button type="button" role="menuitem" onClick={() => beginRename(contextMenu.tag)}><Icon name="edit" size={14}/><span>Rename tag</span></button>
        <button type="button" role="menuitem" onClick={() => beginVisualEdit(contextMenu.tag)}><Icon name="settings" size={14}/><span>Edit appearance</span></button>
        <span/>
        <button type="button" role="menuitem" className="context-menu__danger" onClick={() => void remove(contextMenu.tag)}><Icon name="trash" size={14}/><span>Delete tag</span></button>
      </div>
    </>}
  </div>
}

function safeTagColor(value: string | undefined): string {
  return typeof value === 'string' && /^#[0-9a-f]{6}$/i.test(value) ? value : FALLBACK_TAG_COLOR
}

function safeTagIcon(value: string | undefined): TagIconName {
  return TAG_ICONS.includes(value as TagIconName) ? value as TagIconName : 'tag'
}
