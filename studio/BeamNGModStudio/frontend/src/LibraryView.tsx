import { useEffect, useMemo, useState } from 'react'
import type { Dashboard, LibraryFolder, LibraryItem, ScanProgress } from '../bindings/github.com/SignedAdam/beamng-mod-studio/models.js'
import { Icon } from './icons'
import { Badge, Button, EmptyState, formatBytes, formatDate, kindIcon, kindLabel } from './ui'

interface LibraryViewProps {
  items: LibraryItem[]
  folders: LibraryFolder[]
  dashboard: Dashboard | null
  scan: ScanProgress | null
  scanning: boolean
  status: string
  kind: string
  query: string
  folderID: string
  selectedID: string
  onStatusChange: (value: string) => void
  onKindChange: (value: string) => void
  onQueryChange: (value: string) => void
  onFolderChange: (value: string) => void
  onCreateFolder: (name: string) => void
  onRenameFolder: (id: string, name: string) => void
  onDeleteFolder: (id: string) => void
  onSelect: (item: LibraryItem) => void
  onScan: () => void
  onCancelScan: () => void
}

type SortKey = 'name' | 'kind' | 'status' | 'author' | 'files' | 'variants' | 'size' | 'modified' | 'issues'
const pageSize = 100

export function LibraryView(props: LibraryViewProps) {
  const [page, setPage] = useState(0)
  const [sortKey, setSortKey] = useState<SortKey>('name')
  const [sortDirection, setSortDirection] = useState<1 | -1>(1)
  const sorted = useMemo(() => [...props.items].sort((left, right) => compareValues(sortValue(left, sortKey), sortValue(right, sortKey)) * sortDirection), [props.items, sortKey, sortDirection])
  const pageCount = Math.max(1, Math.ceil(sorted.length / pageSize))
  const visible = useMemo(() => sorted.slice(page * pageSize, (page + 1) * pageSize), [sorted, page])

  useEffect(() => setPage(0), [props.query, props.kind, props.status, props.folderID, sortKey, sortDirection])
  useEffect(() => { if (page >= pageCount) setPage(pageCount - 1) }, [page, pageCount])

  const changeSort = (key: SortKey) => {
    if (key === sortKey) setSortDirection(direction => direction === 1 ? -1 : 1)
    else { setSortKey(key); setSortDirection(1) }
  }
  const createFolder = () => {
    const name = window.prompt('Library folder name')?.trim()
    if (name) props.onCreateFolder(name)
  }
  const activeFolder = props.folders.find(folder => folder.id === props.folderID)
  const renameFolder = () => {
    if (!activeFolder) return
    const name = window.prompt('Rename library folder', activeFolder.name)?.trim()
    if (name && name !== activeFolder.name) props.onRenameFolder(activeFolder.id, name)
  }
  const deleteFolder = () => {
    if (!activeFolder || !window.confirm(`Delete folder “${activeFolder.name}”? Mods return to Unfiled and are not deleted.`)) return
    props.onDeleteFolder(activeFolder.id)
  }

  return <section className="view library-view" aria-label="Mod library">
    <header className="view-header view-header--compact"><h1>Library</h1><div className="view-header__actions">{props.scanning ? <Button icon="close" onClick={props.onCancelScan}>Cancel scan</Button> : <Button icon="scan" tone="primary" onClick={props.onScan}>Scan</Button>}</div></header>
    <div className="library-summary" aria-label="Library summary"><Summary label="Linked" value={props.dashboard?.linked ?? 0}/><Summary label="Missing" value={props.dashboard?.unlinked ?? 0}/><Summary label="Vehicles" value={props.dashboard?.vehicles ?? 0}/><Summary label="Maps" value={props.dashboard?.maps ?? 0}/><Summary label="Projects" value={props.dashboard?.workspaces ?? 0}/></div>
    {props.scan && (props.scanning || props.scan.done) && <div className={`scan-strip ${props.scan.error ? 'scan-strip--error' : ''}`}><div className="scan-strip__pulse"><Icon name={props.scan.error ? 'error' : props.scan.done ? 'check' : 'scan'} size={15}/></div><strong>{props.scan.error ? 'Scan stopped' : props.scan.done ? 'Scan complete' : props.scan.phase === 'discovering' ? 'Discovering' : 'Analyzing'}</strong><span title={props.scan.path}>{props.scan.error || props.scan.path || 'Finalizing index'}</span><div className="scan-strip__metrics"><span>{props.scan.discovered} found</span><span>{props.scan.analyzed} processed</span>{props.scan.cached > 0 && <span>{props.scan.cached} cached</span>}{props.scan.failed > 0 && <span>{props.scan.failed} failed</span>}</div></div>}
    <div className="library-toolbar"><div className="segmented" aria-label="Link status filter">{['all', 'linked', 'unlinked'].map(value => <button key={value} className={props.status === value ? 'is-active' : ''} onClick={() => props.onStatusChange(value)}>{value === 'all' ? 'All' : value === 'unlinked' ? 'Missing' : 'Linked'}</button>)}</div><label className="search-box"><Icon name="search" size={14}/><input value={props.query} onChange={event => props.onQueryChange(event.target.value)} placeholder="Search name, author, path, namespace" aria-label="Search mods"/>{props.query && <button onClick={() => props.onQueryChange('')} aria-label="Clear search"><Icon name="close" size={13}/></button>}</label><label className="select-box"><span>Kind</span><select value={props.kind} onChange={event => props.onKindChange(event.target.value)}><option value="all">All kinds</option><option value="vehicle">Vehicle</option><option value="map">Map</option><option value="ui">UI app</option><option value="script">Script</option><option value="mixed">Mixed</option><option value="unknown">Unknown</option></select></label><span className="result-count">{props.items.length.toLocaleString()} mods</span></div>
    <div className="library-workarea">
      <aside className="library-folders"><header><strong>Folders</strong><button className="icon-button" onClick={createFolder} title="Create folder"><Icon name="plus" size={14}/></button></header><button className={props.folderID === 'all' ? 'is-active' : ''} onClick={() => props.onFolderChange('all')}><Icon name="library" size={14}/><span>All mods</span><small>{props.dashboard?.entities ?? 0}</small></button><button className={props.folderID === 'unfiled' ? 'is-active' : ''} onClick={() => props.onFolderChange('unfiled')}><Icon name="folder" size={14}/><span>Unfiled</span></button>{props.folders.map(folder => <button key={folder.id} className={props.folderID === folder.id ? 'is-active' : ''} onClick={() => props.onFolderChange(folder.id)}><Icon name="folder" size={14}/><span>{folder.name}</span><small>{folder.modCount}</small></button>)}{activeFolder && <footer><button onClick={renameFolder}><Icon name="edit" size={12}/>Rename</button><button onClick={deleteFolder}><Icon name="trash" size={12}/>Delete</button></footer>}</aside>
      <div className="library-results">
        {visible.length === 0 ? <EmptyState icon="archive" title={props.scanning ? `Loading ${props.scan?.discovered ?? 0} mods` : 'No matching mods'} detail={props.scanning ? 'This may take a few seconds.' : 'Change the filters, select another folder, or scan the configured roots.'} action={!props.scanning && <Button icon="scan" tone="primary" onClick={props.onScan}>Scan</Button>}/> : <div className="mod-table-wrap"><table className="mod-table"><thead><tr><SortableHead label="Name" value="name" active={sortKey} direction={sortDirection} onSort={changeSort}/><SortableHead label="Kind" value="kind" active={sortKey} direction={sortDirection} onSort={changeSort}/><SortableHead label="Status" value="status" active={sortKey} direction={sortDirection} onSort={changeSort}/><SortableHead label="Author" value="author" active={sortKey} direction={sortDirection} onSort={changeSort}/><SortableHead label="Files" value="files" active={sortKey} direction={sortDirection} onSort={changeSort}/><SortableHead label="Variants" value="variants" active={sortKey} direction={sortDirection} onSort={changeSort}/><SortableHead label="Size" value="size" active={sortKey} direction={sortDirection} onSort={changeSort}/><SortableHead label="Modified" value="modified" active={sortKey} direction={sortDirection} onSort={changeSort}/><SortableHead label="Issues" value="issues" active={sortKey} direction={sortDirection} onSort={changeSort}/></tr></thead><tbody>{visible.map(item => <ModRow key={item.entityId} item={item} selected={item.entityId === props.selectedID} onSelect={() => props.onSelect(item)}/>)}</tbody></table></div>}
        {pageCount > 1 && <footer className="pagination"><Button tone="quiet" disabled={page === 0} onClick={() => setPage(value => value - 1)}>Previous</Button><span>{page + 1} / {pageCount}</span><Button tone="quiet" disabled={page + 1 >= pageCount} onClick={() => setPage(value => value + 1)}>Next</Button></footer>}
      </div>
    </div>
  </section>
}

function Summary({ label, value }: { label: string; value: number }) { return <span><strong>{value.toLocaleString()}</strong>{label}</span> }
function SortableHead({ label, value, active, direction, onSort }: { label: string; value: SortKey; active: SortKey; direction: 1 | -1; onSort: (value: SortKey) => void }) { const selected = value === active; return <th aria-sort={selected ? direction === 1 ? 'ascending' : 'descending' : 'none'}><button onClick={() => onSort(value)}>{label}<span>{selected ? direction === 1 ? '▲' : '▼' : '↕'}</span></button></th> }
function ModRow({ item, selected, onSelect }: { item: LibraryItem; selected: boolean; onSelect: () => void }) { return <tr className={selected ? 'is-selected' : ''} onClick={onSelect} onDoubleClick={onSelect} tabIndex={0} onKeyDown={event => { if (event.key === 'Enter' || event.key === ' ') onSelect() }}><td className="mod-table__name"><span className="mod-table__icon">{item.thumbnailUrl ? <img src={item.thumbnailUrl} alt="" loading="lazy" onError={event => { event.currentTarget.style.display = 'none' }}/> : <Icon name={kindIcon(String(item.kind))} size={16}/>}</span><div><strong>{item.displayName}</strong><span title={item.archivePath}>{item.archivePath || 'Source archive missing'}</span></div></td><td><Badge tone={item.kind === 'unknown' ? 'warning' : 'neutral'}>{kindLabel(String(item.kind))}</Badge></td><td><span className={item.linked ? 'table-status table-status--linked' : 'table-status table-status--missing'}><i/>{item.linked ? 'Linked' : 'Missing'}</span></td><td title={item.manifest?.author || ''}>{item.manifest?.author || '—'}</td><td className="number-cell">{item.memberCount.toLocaleString()}</td><td className="number-cell">{item.variantCount.toLocaleString()}</td><td className="number-cell">{formatBytes(item.sizeBytes)}</td><td>{item.modifiedAt ? formatDate(item.modifiedAt) : '—'}</td><td className={item.issueCount > 0 ? 'number-cell issue-cell' : 'number-cell'}>{item.issueCount.toLocaleString()}</td></tr> }
function sortValue(item: LibraryItem, key: SortKey): string | number { switch (key) { case 'name': return item.displayName.toLowerCase(); case 'kind': return String(item.kind); case 'status': return item.linked ? 0 : 1; case 'author': return (item.manifest?.author || '').toLowerCase(); case 'files': return item.memberCount; case 'variants': return item.variantCount; case 'size': return item.sizeBytes; case 'modified': return item.modifiedAt ? new Date(item.modifiedAt).valueOf() : 0; case 'issues': return item.issueCount } }
function compareValues(left: string | number, right: string | number) { return typeof left === 'number' && typeof right === 'number' ? left - right : String(left).localeCompare(String(right), undefined, { numeric: true, sensitivity: 'base' }) }
