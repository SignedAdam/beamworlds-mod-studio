import { useState } from 'react'
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

export function TagEditor({ assigned, tags, onSet, onCreate, onRename, onDelete }: TagEditorProps) {
  const [open, setOpen] = useState(false)
  const [newName, setNewName] = useState('')
  const [busy, setBusy] = useState('')
  const assignedIDs = new Set(assigned.map(tag => tag.id))

  const toggle = async (tagID: string) => {
    const next = assignedIDs.has(tagID) ? assigned.filter(tag => tag.id !== tagID).map(tag => tag.id) : [...assigned.map(tag => tag.id), tagID]
    setBusy(tagID)
    try { await onSet(next) } finally { setBusy('') }
  }
  const create = async () => {
    const name = newName.trim()
    if (!name) return
    setBusy('create')
    try {
      const tag = await onCreate(name)
      if (tag) {
        await onSet([...assigned.map(current => current.id), tag.id])
        setNewName('')
      }
    } finally { setBusy('') }
  }
  const rename = async (tag: ModTag) => {
    const name = window.prompt('Rename tag', tag.name)?.trim()
    if (!name || name === tag.name) return
    setBusy(tag.id)
    try { await onRename(tag.id, name) } finally { setBusy('') }
  }
  const remove = async (tag: ModTag) => {
    if (!window.confirm(`Delete tag “${tag.name}”? It will be removed from every mod.`)) return
    setBusy(tag.id)
    try { await onDelete(tag.id) } finally { setBusy('') }
  }

  return <section className="tag-editor">
    <header><div><h3 className="section-title">Your tags</h3><small>Flexible labels can overlap across collections.</small></div><button type="button" onClick={() => setOpen(value => !value)} aria-expanded={open}><Icon name="tag" size={14}/>{open ? 'Done' : 'Edit tags'}</button></header>
    <div className="tag-editor__assigned">{assigned.length === 0 ? <span>No tags applied</span> : assigned.map(tag => <button type="button" className="mod-tag" title={`Remove ${tag.name}`} disabled={busy !== ''} onClick={() => void toggle(tag.id)} key={tag.id}>{tag.name}<Icon name="close" size={11}/></button>)}</div>
    {open && <div className="tag-editor__panel">
      <form onSubmit={event => { event.preventDefault(); void create() }}><input value={newName} onChange={event => setNewName(event.target.value)} placeholder="Create a custom tag" maxLength={80}/><button type="submit" disabled={!newName.trim() || busy !== ''}><Icon name="plus" size={13}/>Add</button></form>
      <div className="tag-editor__list">{tags.map(tag => <div key={tag.id} className={assignedIDs.has(tag.id) ? 'is-selected' : ''}>
        <label><input type="checkbox" checked={assignedIDs.has(tag.id)} disabled={busy !== ''} onChange={() => void toggle(tag.id)}/><span><strong>{tag.name}</strong><small>{tag.modCount.toLocaleString()} mod{tag.modCount === 1 ? '' : 's'}</small></span></label>
        <button type="button" className="icon-button" disabled={busy !== ''} onClick={() => void rename(tag)} aria-label={`Rename ${tag.name}`} title="Rename tag"><Icon name="edit" size={12}/></button>
        <button type="button" className="icon-button" disabled={busy !== ''} onClick={() => void remove(tag)} aria-label={`Delete ${tag.name}`} title="Delete tag"><Icon name="trash" size={12}/></button>
      </div>)}</div>
    </div>}
  </section>
}
