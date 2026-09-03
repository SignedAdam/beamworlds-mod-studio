import { useEffect, useState } from 'react'
import type { LibraryFolder, LibraryItem, ModTag, ScanProgress } from '../bindings/github.com/SignedAdam/beamng-mod-studio/models.js'
import { CollectionMenu } from './CollectionMenu'
import { Icon } from './icons'
import { LibrarySearch } from './LibrarySearch'
import { ModTable } from './ModTable'
import { Page } from './ui'

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
  const [contextMenu, setContextMenu] = useState<{ item: LibraryItem; x: number; y: number } | null>(null)

  useEffect(() => {
    if (!contextMenu) return
    const close = () => setContextMenu(null)
    window.addEventListener('resize', close)
    window.addEventListener('scroll', close, true)
    return () => {
      window.removeEventListener('resize', close)
      window.removeEventListener('scroll', close, true)
    }
  }, [contextMenu])

  const activeCollection = props.folders.find(folder => folder.id === props.folderID)
  const appliedFilters: string[] = []
  const appliedQuery = props.query.trim()
  if (appliedQuery) appliedFilters.push(`search “${appliedQuery}”`)
  if (props.folderID === 'unfiled') appliedFilters.push('collection “Unfiled”')
  else if (props.folderID !== 'all') appliedFilters.push(`collection “${activeCollection?.name ?? 'Selected collection'}”`)
  const emptyFilterTitle = `No matching mods for these filters: ${appliedFilters.length > 0 ? appliedFilters.join(', ') : 'none'}.`

  return <Page title="Mod Library" className="library-view" ariaLabel="Mod library" actions={[props.scanning ? { key: 'cancel-scan', label: 'Cancel scan', icon: 'close', role: 'secondary', onClick: props.onCancelScan } : { key: 'rescan-mods', label: 'Rescan mods', icon: 'scan', role: 'primary', onClick: props.onScan, title: 'Scans configured folders for new or updated mods' }]}>
    {props.scan && (props.scanning || props.scan.done) && <div className={`scan-strip ${props.scan.error ? 'scan-strip--error' : ''}`}><div className="scan-strip__pulse"><Icon name={props.scan.error ? 'error' : props.scan.done ? 'check' : 'scan'} size={15}/></div><strong>{props.scan.error ? 'Scan stopped' : props.scan.done ? 'Scan complete' : props.scan.phase === 'discovering' ? 'Discovering' : 'Analyzing'}</strong><span title={props.scan.path}>{props.scan.error || props.scan.path || 'Finalizing index'}</span><div className="scan-strip__metrics"><span>{props.scan.discovered} found</span><span>{props.scan.analyzed} processed</span>{props.scan.cached > 0 && <span>{props.scan.cached} cached</span>}{props.scan.failed > 0 && <span>{props.scan.failed} failed</span>}</div></div>}
    <div className="library-toolbar">
      <CollectionMenu folders={props.folders} value={props.folderID} total={props.catalogItems.length} onChange={props.onFolderChange} onCreate={props.onCreateFolder} onRename={props.onRenameFolder} onDelete={props.onDeleteFolder}/>
      <LibrarySearch value={props.query} loading={props.loading} items={props.catalogItems} folders={props.folders} tags={props.tags} onChange={props.onQueryChange}/>
    </div>
    <div className="library-workarea">
      <ModTable
        className="library-results"
        surface="library"
        ariaLabel="Mod library results"
        items={props.items}
        interaction={{
          kind: 'browse',
          selectedID: props.selectedID,
          onActivate: props.onSelect,
          onContextMenu: (selected, event) => {
            props.onSelect(selected)
            setContextMenu({
              item: selected,
              x: Math.min(event.clientX, window.innerWidth - 248),
              y: Math.min(event.clientY, window.innerHeight - 112),
            })
          },
        }}
        loading={props.loading || props.scanning}
        loadingLabel={props.scanning ? `Loading ${props.scan?.discovered ?? 0} mods` : 'Filtering mods'}
        emptyTitle={emptyFilterTitle}
        resetKey={`${props.query}\u0000${props.folderID}`}
      />
    </div>
    {contextMenu && <>
      <button className="mod-context-backdrop" aria-label="Close mod actions" onClick={() => setContextMenu(null)}/>
      <div className="mod-context-menu" role="menu" style={{ left: contextMenu.x, top: contextMenu.y }}>
        <button role="menuitem" disabled={!contextMenu.item.linked} onClick={() => { props.onVirusScan(contextMenu.item); setContextMenu(null) }}><Icon name="shield" size={17}/><span><strong>Scan for threats</strong><small>{contextMenu.item.linked ? 'Choose signature-based or full scan' : 'Source archive unavailable'}</small></span></button>
      </div>
    </>}
  </Page>
}

