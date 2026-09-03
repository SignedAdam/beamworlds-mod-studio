import { useEffect, useRef, useState } from 'react'
import type { KeyboardEvent as ReactKeyboardEvent } from 'react'
import type { LibraryFolder } from '../bindings/github.com/SignedAdam/beamng-mod-studio/models.js'
import { Icon, type IconName } from './icons'
import './CollectionMenu.css'

interface CollectionMenuProps {
  folders: LibraryFolder[]
  value: string
  total: number
  onChange: (value: string) => void
  onCreate: (name: string) => void
  onRename: (id: string, name: string) => void
  onDelete: (id: string) => void
}

interface CollectionChoice {
  id: string
  label: string
  detail: string
  icon: IconName
  count?: number
  folder?: LibraryFolder
}

const menuID = 'library-collection-menu'

export function CollectionMenu({ folders, value, total, onChange, onCreate, onRename, onDelete }: CollectionMenuProps) {
  const [open, setOpen] = useState(false)
  const rootRef = useRef<HTMLDivElement>(null)
  const triggerRef = useRef<HTMLButtonElement>(null)
  const choiceRefs = useRef<Array<HTMLButtonElement | null>>([])
  const active = folders.find(folder => folder.id === value)
  const label = value === 'all' ? 'All mods' : value === 'unfiled' ? 'Unfiled' : active?.name ?? 'All mods'
  const choices: CollectionChoice[] = [
    { id: 'all', label: 'All mods', detail: 'Entire indexed library', icon: 'library', count: total },
    { id: 'unfiled', label: 'Unfiled', detail: 'Not assigned to a collection', icon: 'folder' },
    ...folders.map(folder => ({
      id: folder.id,
      label: folder.name,
      detail: `${folder.modCount.toLocaleString()} mod${folder.modCount === 1 ? '' : 's'}`,
      icon: 'folder' as const,
      folder,
    })),
  ]
  const selectedIndex = Math.max(0, choices.findIndex(choice => choice.id === value))

  const focusChoice = (index: number) => {
    if (choices.length === 0) return
    const wrappedIndex = (index + choices.length) % choices.length
    choiceRefs.current[wrappedIndex]?.focus()
  }
  const focusTrigger = () => {
    window.setTimeout(() => triggerRef.current?.focus(), 0)
  }
  const closeMenu = () => {
    setOpen(false)
    focusTrigger()
  }
  const select = (next: string) => {
    onChange(next)
    closeMenu()
  }
  const handleMenuKeyDown = (event: ReactKeyboardEvent<HTMLDivElement>) => {
    const target = event.target instanceof Element
      ? event.target.closest<HTMLButtonElement>('[data-collection-choice]')
      : null
    if (!target) return
    const activeIndex = Number(target.dataset.collectionChoice)
    if (!Number.isInteger(activeIndex) || activeIndex < 0 || activeIndex >= choices.length) return
    if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
      event.preventDefault()
      focusChoice(activeIndex + (event.key === 'ArrowDown' ? 1 : -1))
    } else if (event.key === 'Home' || event.key === 'End') {
      event.preventDefault()
      focusChoice(event.key === 'Home' ? 0 : choices.length - 1)
    } else if (event.key === 'Enter' || event.key === ' ' || event.key === 'Spacebar') {
      event.preventDefault()
      select(choices[activeIndex].id)
    }
  }

  useEffect(() => {
    if (!open) return
    choiceRefs.current[selectedIndex]?.focus()
    const closeOutside = (event: PointerEvent) => {
      if (!rootRef.current?.contains(event.target as Node)) closeMenu()
    }
    const escape = (event: KeyboardEvent) => {
      if (event.key !== 'Escape') return
      event.preventDefault()
      event.stopPropagation()
      closeMenu()
    }
    document.addEventListener('pointerdown', closeOutside)
    document.addEventListener('keydown', escape)
    return () => {
      document.removeEventListener('pointerdown', closeOutside)
      document.removeEventListener('keydown', escape)
    }
  }, [open, selectedIndex])

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

  return <div className="collection-control" ref={rootRef}>
    <button
      ref={triggerRef}
      type="button"
      className={`collection-trigger${value !== 'all' ? ' is-filtered' : ''}${open ? ' is-open' : ''}`}
      onClick={() => { if (open) closeMenu(); else setOpen(true) }}
      onKeyDown={event => {
        if (!open && (event.key === 'ArrowDown' || event.key === 'ArrowUp')) {
          event.preventDefault()
          setOpen(true)
        }
      }}
      aria-expanded={open}
      aria-haspopup="menu"
      aria-controls={menuID}
      aria-label={label}
      title={`Filter by collection: ${label}`}
    >
      <span>{label}</span>
      <Icon name="chevron" size={13}/>
    </button>
    {open && <div className="collection-menu" id={menuID} role="menu" aria-label="Library collections" aria-orientation="vertical" onKeyDown={handleMenuKeyDown}>
      <header className="collection-menu__header">
        <div><strong>Collections</strong></div>
        <button type="button" role="menuitem" className="icon-button collection-menu__create" onClick={create} aria-label="Create collection" title="Create collection"><Icon name="plus" size={15}/></button>
      </header>
      {choices.map((choice, index) => {
        const activeChoice = value === choice.id
        const choiceContent = <>
          <Icon name={choice.icon} size={15}/>
          <span className="collection-menu__choice-label"><strong>{choice.label}</strong><small>{choice.detail}</small></span>
          {choice.count !== undefined && <em className="collection-menu__choice-count">{choice.count.toLocaleString()}</em>}
        </>
        const choiceButton = <button
          key={choice.id}
          type="button"
          role="menuitemradio"
          aria-checked={activeChoice}
          className={`collection-menu__choice${activeChoice ? ' is-active' : ''}`}
          data-collection-choice={index}
          ref={element => { choiceRefs.current[index] = element }}
          tabIndex={index === selectedIndex ? 0 : -1}
          onClick={() => select(choice.id)}
        >
          {choiceContent}
        </button>
        if (!choice.folder) return choiceButton
        return <div className={`collection-menu__row${activeChoice ? ' is-active' : ''}`} role="none" key={choice.id}>
          {choiceButton}
          <button type="button" role="menuitem" className="icon-button collection-menu__action" onClick={() => rename(choice.folder!)} aria-label={`Rename ${choice.folder.name}`} title="Rename collection"><Icon name="edit" size={13}/></button>
          <button type="button" role="menuitem" className="icon-button collection-menu__action" onClick={() => remove(choice.folder!)} aria-label={`Delete ${choice.folder.name}`} title="Delete collection"><Icon name="trash" size={13}/></button>
        </div>
      })}
      {folders.length === 0 && <p className="collection-menu__empty">No collections yet.</p>}
    </div>}
  </div>
}
