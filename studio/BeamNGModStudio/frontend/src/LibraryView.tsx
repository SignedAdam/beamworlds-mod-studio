import { useEffect, useMemo, useRef, useState } from 'react'
import type { MouseEvent as ReactMouseEvent } from 'react'
import type { LibraryFolder, LibraryItem, ModTag, ScanProgress } from '../bindings/github.com/SignedAdam/beamng-mod-studio/models.js'
import { CollectionMenu } from './CollectionMenu'
import { Icon } from './icons'
import { LibraryPreviewGrid, type LibraryPreviewSize } from './LibraryPreviewGrid'
import { LibrarySearch } from './LibrarySearch'
import { ModTable, sortLibraryItems, type ModTableSort } from './ModTable'
import { Page, type PageActionSpec } from './ui'

type LibraryViewMode = 'table' | 'preview'

const previewSizes: LibraryPreviewSize[] = ['small', 'medium', 'large']

function stepPreviewSize(current: LibraryPreviewSize, amount: -1 | 1): LibraryPreviewSize {
  const currentIndex = previewSizes.indexOf(current)
  const nextIndex = Math.max(0, Math.min(previewSizes.length - 1, currentIndex + amount))
  return previewSizes[nextIndex]
}

interface LibraryViewProps {
  items: LibraryItem[]
  catalogItems: LibraryItem[]
  folders: LibraryFolder[]
  tags: ModTag[]
  scan: ScanProgress | null
  scanning: boolean
  loading: boolean
  query: string
  folderID: string
  selectedID: string
  onQueryChange: (value: string) => void
  onFolderChange: (value: string) => void
  onCreateFolder: (name: string) => void
  onRenameFolder: (id: string, name: string) => void
  onDeleteFolder: (id: string) => void
  onSelect: (item: LibraryItem) => void
  onVirusScan: (item: LibraryItem) => void
  onScan: () => void
  onCancelScan: () => void
}

export function LibraryView(props: LibraryViewProps) {
  const [viewMode, setViewMode] = useState<LibraryViewMode>('table')
  const [previewSize, setPreviewSize] = useState<LibraryPreviewSize>('medium')
  const [librarySort, setLibrarySort] = useState<ModTableSort>({ key: 'name', direction: 1 })
  const [contextMenu, setContextMenu] = useState<{ item: LibraryItem; x: number; y: number } | null>(null)
  const contextActionRef = useRef<HTMLButtonElement>(null)
  const contextMenuRef = useRef<HTMLDivElement>(null)


  useEffect(() => {
    if (!contextMenu) return
    const close = () => setContextMenu(null)
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key !== 'Escape') return
      event.preventDefault()
      close()
    }
    window.addEventListener('resize', close)
    window.addEventListener('scroll', close, true)
    window.addEventListener('keydown', onKeyDown)
    if (contextActionRef.current && !contextActionRef.current.disabled) contextActionRef.current.focus()
    else contextMenuRef.current?.focus()
    return () => {
      window.removeEventListener('resize', close)
      window.removeEventListener('scroll', close, true)
      window.removeEventListener('keydown', onKeyDown)
    }
  }, [contextMenu])

  const openContextMenu = (selected: LibraryItem, event: ReactMouseEvent<HTMLElement>) => {
    event.preventDefault()
    props.onSelect(selected)
    const target = event.currentTarget
    const rect = target instanceof HTMLElement ? target.getBoundingClientRect() : null
    const x = event.clientX || rect?.right || 0
    const y = event.clientY || rect?.bottom || 0
    const maxX = Math.max(8, window.innerWidth - 248)
    const maxY = Math.max(8, window.innerHeight - 112)
    setContextMenu({
      item: selected,
      x: Math.max(8, Math.min(x, maxX)),
      y: Math.max(8, Math.min(y, maxY)),
    })
  }

  const pageActions: PageActionSpec[] = [
    {
      key: 'library-view-table',
      label: 'Table',
      icon: 'columns',
      role: 'secondary',
      className: `library-view-mode library-view-mode--table${viewMode === 'table' ? ' is-active' : ''}`,
      'aria-label': 'Show library table view',
      'aria-pressed': viewMode === 'table',
      onClick: () => setViewMode('table'),
    },
    {
      key: 'library-view-preview',
      label: 'Preview',
      icon: 'mixed',
      role: 'secondary',
      className: `library-view-mode library-view-mode--preview${viewMode === 'preview' ? ' is-active' : ''}`,
      'aria-label': 'Show library preview view',
      'aria-pressed': viewMode === 'preview',
      onClick: () => setViewMode('preview'),
    },
  ]
  if (viewMode === 'preview') {
    pageActions.push(
      {
        key: 'library-preview-smaller',
        label: 'Smaller previews',
        icon: 'collapse',
        role: 'secondary',
        className: 'library-preview-size',
        'aria-label': previewSize === 'small' ? 'Smaller previews (smallest)' : 'Smaller previews',
        title: previewSize === 'small' ? 'Smallest preview size' : 'Make previews smaller',
        disabled: previewSize === 'small',
        onClick: () => setPreviewSize((current) => stepPreviewSize(current, -1)),
      },
      {
        key: 'library-preview-larger',
        label: 'Larger previews',
        icon: 'plus',
        role: 'secondary',
        className: 'library-preview-size',
        'aria-label': previewSize === 'large' ? 'Larger previews (largest)' : 'Larger previews',
        title: previewSize === 'large' ? 'Largest preview size' : 'Make previews larger',
        disabled: previewSize === 'large',
        onClick: () => setPreviewSize((current) => stepPreviewSize(current, 1)),
      },
    )
  }
  pageActions.push(
    props.scanning
      ? {
          key: 'cancel-scan',
          label: 'Cancel scan',
          icon: 'close',
          role: 'secondary',
          className: 'library-scan-action',
          onClick: props.onCancelScan,
        }
      : {
          key: 'rescan-mods',
          label: 'Rescan mods',
          icon: 'scan',
          role: 'primary',
          className: 'library-scan-action',
          onClick: props.onScan,
          title: 'Scans configured folders for new or updated mods',
        },
  )

  const activeCollection = props.folders.find(folder => folder.id === props.folderID)
  const appliedFilters: string[] = []
  const appliedQuery = props.query.trim()
  if (appliedQuery) appliedFilters.push(`search “${appliedQuery}”`)
  if (props.folderID === 'unfiled') appliedFilters.push('collection “Unfiled”')
  else if (props.folderID !== 'all') appliedFilters.push(`collection “${activeCollection?.name ?? 'Selected collection'}”`)
  const emptyFilterTitle = `No matching mods for these filters: ${appliedFilters.length > 0 ? appliedFilters.join(', ') : 'none'}.`
  const sortedItems = useMemo(
    () => (viewMode === 'preview' ? sortLibraryItems(props.items, librarySort) : []),
    [librarySort, props.items, viewMode],
  )

  return <Page title="Mod Library" className="library-view" ariaLabel="Mod library" actions={pageActions}>
    {props.scan && (props.scanning || Boolean(props.scan.error)) && <div className={`scan-strip ${props.scan.error && !props.scanning ? 'scan-strip--error' : ''}`}><div className="scan-strip__pulse"><Icon name={props.scan.error && !props.scanning ? 'error' : 'scan'} size={15}/></div><strong>{props.scan.error && !props.scanning ? 'Scan stopped' : props.scan.phase === 'discovering' ? 'Discovering' : 'Analyzing'}</strong><span title={props.scan.path}>{props.scan.error || props.scan.path || 'Finalizing index'}</span><div className="scan-strip__metrics"><span>{props.scan.discovered} found</span><span>{props.scan.analyzed} processed</span>{props.scan.cached > 0 && <span>{props.scan.cached} cached</span>}{props.scan.failed > 0 && <span>{props.scan.failed} failed</span>}</div></div>}
    <div className="library-toolbar">
      <CollectionMenu folders={props.folders} value={props.folderID} total={props.catalogItems.length} onChange={props.onFolderChange} onCreate={props.onCreateFolder} onRename={props.onRenameFolder} onDelete={props.onDeleteFolder}/>
      <LibrarySearch value={props.query} loading={props.loading} items={props.catalogItems} folders={props.folders} tags={props.tags} onChange={props.onQueryChange}/>
    </div>
    <div className="library-workarea">
      {viewMode === 'preview' ? (
        <LibraryPreviewGrid
          items={sortedItems}
          selectedID={props.selectedID}
          previewSize={previewSize}
          loading={props.loading}
          loadingLabel="Filtering mods"
          emptyTitle={emptyFilterTitle}
          ariaLabel="Mod library preview results"
          onSelect={props.onSelect}
          onContextMenu={openContextMenu}
        />
      ) : (
        <ModTable
          className="library-results"
          surface="library"
          ariaLabel="Mod library results"
          items={props.items}
          sort={librarySort}
          onSortChange={setLibrarySort}
          interaction={{
            kind: 'browse',
            selectedID: props.selectedID,
            onActivate: props.onSelect,
            onContextMenu: openContextMenu,
          }}
          loading={props.loading}
          loadingLabel="Filtering mods"
          emptyTitle={emptyFilterTitle}
          resetKey={`${props.query}\u0000${props.folderID}`}
        />
      )}
    </div>
    {contextMenu && <>
      <button className="mod-context-backdrop" aria-label="Close mod actions" onClick={() => setContextMenu(null)}/>
      <div ref={contextMenuRef} className="mod-context-menu" role="menu" aria-label={`Actions for ${contextMenu.item.displayName}`} tabIndex={-1} style={{ left: contextMenu.x, top: contextMenu.y }}>
        <button ref={contextActionRef} role="menuitem" disabled={!contextMenu.item.linked} onClick={() => { props.onVirusScan(contextMenu.item); setContextMenu(null) }}><Icon name="shield" size={17}/><span><strong>Scan for threats</strong><small>{contextMenu.item.linked ? 'Choose signature-based or full scan' : 'Source archive unavailable'}</small></span></button>
      </div>
    </>}
  </Page>
}

