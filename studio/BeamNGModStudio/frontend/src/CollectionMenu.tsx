import { useEffect, useRef, useState } from 'react'
import type { KeyboardEvent as ReactKeyboardEvent } from 'react'
import type { ModCollection } from '../bindings/github.com/SignedAdam/beamng-mod-studio/models.js'
import { Icon, type IconName } from './icons'
import './CollectionMenu.css'

interface CollectionMenuProps {
  collections: ModCollection[]
  value: string
  total: number
  onChange: (value: string) => void
  onManageCollections: () => void
}

interface CollectionChoice {
  id: string
  label: string
  icon: IconName
  count?: number
}

const menuID = 'library-collection-menu'

export function CollectionMenu({
  collections,
  value,
  total,
  onChange,
  onManageCollections,
}: CollectionMenuProps) {
  const [open, setOpen] = useState(false)
  const rootRef = useRef<HTMLDivElement>(null)
  const triggerRef = useRef<HTMLButtonElement>(null)
  const choiceRefs = useRef<Array<HTMLButtonElement | null>>([])
  const active = collections.find(collection => collection.id === value)
  const label = value === 'all' ? 'All mods' : value === 'unfiled' ? 'No collection' : active?.name ?? 'All mods'
  const choices: CollectionChoice[] = [
    { id: 'all', label: 'All mods', icon: 'library', count: total },
    { id: 'unfiled', label: 'No collection', icon: 'folder' },
    ...collections.map(collection => ({
      id: collection.id,
      label: collection.name,
      icon: 'folder' as const,
      count: collection.modCount,
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
  const manage = () => {
    closeMenu()
    onManageCollections()
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
      {choices.map((choice, index) => {
        const activeChoice = value === choice.id
        return <button
          key={choice.id}
          type="button"
          role="menuitemradio"
          aria-checked={activeChoice}
          className={`collection-menu__choice${activeChoice ? ' is-active' : ''}${index === 1 ? ' collection-menu__choice--last-scope' : ''}`}
          data-collection-choice={index}
          ref={element => { choiceRefs.current[index] = element }}
          tabIndex={index === selectedIndex ? 0 : -1}
          onClick={() => select(choice.id)}
        >
          <Icon name={choice.icon} size={15}/>
          <span className="collection-menu__choice-label">{choice.label}</span>
          {choice.count !== undefined && <em className="collection-menu__choice-count">{choice.count.toLocaleString()}</em>}
        </button>
      })}
      {collections.length === 0 && <p className="collection-menu__choice collection-menu__choice--empty" aria-disabled="true">
        <Icon name="folder" size={15}/>
        <span className="collection-menu__choice-label">No collections yet</span>
      </p>}
      <button type="button" role="menuitem" className="collection-menu__choice collection-menu__manage" onClick={manage}><Icon name="settings" size={15}/><span className="collection-menu__choice-label">Manage collections</span></button>
    </div>}
  </div>
}
