import { useEffect, useRef, useState } from 'react'
import type { LibraryFolder } from '../bindings/github.com/SignedAdam/beamng-mod-studio/models.js'
import { Icon } from './icons'

interface CollectionMenuProps {
  folders: LibraryFolder[]
  value: string
  total: number
  onChange: (value: string) => void
  onCreate: (name: string) => void
  onRename: (id: string, name: string) => void
  onDelete: (id: string) => void
}

export function CollectionMenu({ folders, value, total, onChange, onCreate, onRename, onDelete }: CollectionMenuProps) {
  const [open, setOpen] = useState(false)
  const rootRef = useRef<HTMLDivElement>(null)
  const active = folders.find(folder => folder.id === value)
  const label = value === 'all' ? 'All mods' : value === 'unfiled' ? 'Unfiled' : active?.name ?? 'All mods'

  useEffect(() => {
    if (!open) return
    const close = (event: PointerEvent) => {
      if (!rootRef.current?.contains(event.target as Node)) setOpen(false)
    }
    const escape = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setOpen(false)
    }
    document.addEventListener('pointerdown', close)
    document.addEventListener('keydown', escape)
    return () => {
      document.removeEventListener('pointerdown', close)
      document.removeEventListener('keydown', escape)
    }
  }, [open])

  const create = () => {
    const name = window.prompt('Collection name')?.trim()
    if (name) onCreate(name)
  }
  const rename = (folder: LibraryFolder) => {
    const name = window.prompt('Rename collection', folder.name)?.trim()
    if (name && name !== folder.name) onRename(folder.id, name)
  }
  const remove = (folder: LibraryFolder) => {
    if (window.confirm(`Delete collection “${folder.name}”? Its mods become Unfiled and are not deleted.`)) onDelete(folder.id)
  }
  const select = (next: string) => {
    onChange(next)
    setOpen(false)
  }

  return <div className="collection-control" ref={rootRef}>
    <button type="button" className={value !== 'all' ? 'collection-trigger is-filtered' : 'collection-trigger'} onClick={() => setOpen(current => !current)} aria-expanded={open} aria-haspopup="menu" title="Collections are optional single-home groups; tags can overlap across any number of mods.">
      <Icon name="folder" size={15}/><span>{label}</span><Icon name="chevron" size={13}/>
    </button>
    {open && <div className="collection-menu" role="menu" aria-label="Library collections">
      <header><div><strong>Collections</strong><small>Optional single-home groups. Use tags for labels that overlap.</small></div><button type="button" className="icon-button" onClick={create} aria-label="Create collection" title="Create collection"><Icon name="plus" size={15}/></button></header>
      <button type="button" role="menuitemradio" aria-checked={value === 'all'} className={value === 'all' ? 'is-active' : ''} onClick={() => select('all')}><Icon name="library" size={15}/><span><strong>All mods</strong><small>Entire indexed library</small></span><em>{total.toLocaleString()}</em></button>
      <button type="button" role="menuitemradio" aria-checked={value === 'unfiled'} className={value === 'unfiled' ? 'is-active' : ''} onClick={() => select('unfiled')}><Icon name="folder" size={15}/><span><strong>Unfiled</strong><small>Not assigned to a collection</small></span></button>
      {folders.map(folder => <div className={value === folder.id ? 'collection-menu__row is-active' : 'collection-menu__row'} key={folder.id}>
        <button type="button" role="menuitemradio" aria-checked={value === folder.id} onClick={() => select(folder.id)}><Icon name="folder" size={15}/><span><strong>{folder.name}</strong><small>{folder.modCount.toLocaleString()} mod{folder.modCount === 1 ? '' : 's'}</small></span></button>
        <button type="button" className="icon-button" onClick={() => rename(folder)} aria-label={`Rename ${folder.name}`} title="Rename collection"><Icon name="edit" size={13}/></button>
        <button type="button" className="icon-button" onClick={() => remove(folder)} aria-label={`Delete ${folder.name}`} title="Delete collection"><Icon name="trash" size={13}/></button>
      </div>)}
      {folders.length === 0 && <p>No collections yet. Most libraries only need tags.</p>}
    </div>}
  </div>
}
