import { useEffect, useRef, useState, type MouseEvent as ReactMouseEvent } from 'react'
import type { ModTag } from '../bindings/github.com/SignedAdam/beamng-mod-studio/models.js'
import { Icon } from './icons'

interface TagEditorProps {
  assigned: ModTag[]
  tags: ModTag[]
  onSet: (tagIDs: string[]) => Promise<void>
  onCreate: (name: string) => Promise<ModTag | null>
  onRename: (tagID: string, name: string) => Promise<void>
  onDelete: (tagID: string) => Promise<void>
}

type TagEdit =
  | { kind: 'new'; value: string; original: string }
  | { kind: 'rename'; tag: ModTag; value: string; original: string }

interface TagContextMenu {
  tag: ModTag
  x: number
  y: number
}

export function TagEditor({ assigned, tags, onSet, onCreate, onRename, onDelete }: TagEditorProps) {
  const [edit, setEdit] = useState<TagEdit | null>(null)
  const [contextMenu, setContextMenu] = useState<TagContextMenu | null>(null)
  const [busy, setBusy] = useState('')
  const editInput = useRef<HTMLInputElement>(null)
  const assignedIDs = new Set(assigned.map(tag => tag.id))
  const available = tags.filter(tag => !assignedIDs.has(tag.id))
  const editKey = edit ? edit.kind === 'new' ? 'new' : edit.tag.id : ''

  useEffect(() => {
    if (!editKey) return
    const input = editInput.current
    if (!input) return
    input.focus()
    if (edit?.kind === 'new') input.select()
    else input.setSelectionRange(input.value.length, input.value.length)
  }, [editKey])

  const toggle = async (tagID: string) => {
    const next = assignedIDs.has(tagID)
      ? assigned.filter(tag => tag.id !== tagID).map(tag => tag.id)
      : [...assigned.map(tag => tag.id), tagID]
    setBusy(tagID)
    try { await onSet(next) } finally { setBusy('') }
  }

  const beginNew = () => {
    const names = new Set(tags.map(tag => tag.name.toLocaleLowerCase()))
    let index = 1
    while (names.has(`new_tag_${index}`)) index += 1
    const name = `new_tag_${index}`
    setContextMenu(null)
    setEdit({ kind: 'new', value: name, original: name })
  }

  const beginRename = (tag: ModTag) => {
    setContextMenu(null)
    setEdit({ kind: 'rename', tag, value: tag.name, original: tag.name })
  }

  const commitEdit = async (rawValue: string) => {
    if (!edit || busy) return
    const name = rawValue.trim() || edit.original
    if (edit.kind === 'new') {
      setBusy('create')
      try {
        const tag = await onCreate(name)
        if (!tag) return
        await onSet([...assigned.map(current => current.id), tag.id])
        setEdit(null)
      } finally { setBusy('') }
      return
    }
    if (name === edit.original) {
      setEdit(null)
      return
    }
    setBusy(edit.tag.id)
    try {
      await onRename(edit.tag.id, name)
      setEdit(null)
    } finally { setBusy('') }
  }

  const remove = async (tag: ModTag) => {
    setContextMenu(null)
    if (!window.confirm(`Delete tag “${tag.name}”? It will be removed from every mod.`)) return
    setBusy(tag.id)
    try {
      await onDelete(tag.id)
      if (edit?.kind === 'rename' && edit.tag.id === tag.id) setEdit(null)
    } finally { setBusy('') }
  }

  const openContextMenu = (event: ReactMouseEvent, tag: ModTag) => {
    event.preventDefault()
    setContextMenu({
      tag,
      x: Math.min(event.clientX, window.innerWidth - 196),
      y: Math.min(event.clientY, window.innerHeight - 86),
    })
  }

  const updateEdit = (value: string) => {
    setEdit(current => current ? { ...current, value } : current)
  }

  const editor = (label: string) => <span className="tag-editor__chip tag-editor__chip--editing">
    <input
      ref={editInput}
      value={edit?.value ?? ''}
      maxLength={80}
      aria-label={label}
      disabled={busy !== ''}
      onChange={event => updateEdit(event.target.value)}
      onBlur={event => void commitEdit(event.currentTarget.value)}
      onKeyDown={event => {
        if (event.key === 'Enter') {
          event.preventDefault()
          event.currentTarget.blur()
        }
      }}
    />
  </span>

  const chip = (tag: ModTag, isAssigned: boolean) => {
    if (edit?.kind === 'rename' && edit.tag.id === tag.id) return <span key={tag.id}>{editor(`Rename ${tag.name}`)}</span>
    return <button
      type="button"
      key={tag.id}
      className={`tag-editor__chip${isAssigned ? ' is-assigned' : ''}`}
      aria-pressed={isAssigned}
      title={`${isAssigned ? 'Remove' : 'Assign'} ${tag.name}. Right-click for tag actions.`}
      disabled={busy !== ''}
      onClick={() => void toggle(tag.id)}
      onContextMenu={event => openContextMenu(event, tag)}
    >
      <Icon name={isAssigned ? 'check' : 'tag'} size={12}/>
      <span>{tag.name}</span>
      <small>{tag.modCount.toLocaleString()}</small>
    </button>
  }

  return <section className="tag-editor">
    <header>
      <div>
        <h3 className="section-title">Your tags</h3>
        <small>Click a tag to assign or remove it. Right-click for tag actions.</small>
      </div>
    </header>

    <div className="tag-editor__group">
      <strong>Assigned</strong>
      <div className="tag-editor__chips">
        {assigned.length === 0 ? <span className="tag-editor__empty">No tags assigned</span> : assigned.map(tag => chip(tag, true))}
      </div>
    </div>

    <div className="tag-editor__group">
      <div className="tag-editor__available-heading">
        <strong>Available tags</strong>
        <button type="button" disabled={busy !== '' || edit?.kind === 'new'} onClick={beginNew}><Icon name="plus" size={12}/>New tag</button>
      </div>
      <div className="tag-editor__chips">
        {edit?.kind === 'new' && editor('Name new tag')}
        {available.map(tag => chip(tag, false))}
        {available.length === 0 && edit?.kind !== 'new' && <span className="tag-editor__empty">All tags are assigned</span>}
      </div>
    </div>

    {contextMenu && <>
      <div className="tag-editor__context-backdrop" onPointerDown={() => setContextMenu(null)}/>
      <div className="context-menu tag-editor__context-menu" role="menu" style={{ left: contextMenu.x, top: contextMenu.y }} onContextMenu={event => event.preventDefault()}>
        <button type="button" role="menuitem" onClick={() => beginRename(contextMenu.tag)}><Icon name="edit" size={14}/><span>Rename tag</span></button>
        <span/>
        <button type="button" role="menuitem" className="context-menu__danger" onClick={() => void remove(contextMenu.tag)}><Icon name="trash" size={14}/><span>Delete tag</span></button>
      </div>
    </>}
  </section>
}
