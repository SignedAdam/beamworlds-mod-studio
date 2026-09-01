import { memo, useCallback, useEffect, useMemo, useRef, useState } from 'react'
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

type SortKey = 'name' | 'path' | 'kind' | 'status' | 'author' | 'files' | 'variants' | 'size' | 'modified' | 'issues'
type PageSize = 50 | 100 | 200 | 500 | 'all'
interface ColumnState { key: SortKey; visible: boolean; width: number }
interface ColumnDefinition { label: string; defaultWidth: number; align?: 'right' }

const columnDefinitions: Record<SortKey, ColumnDefinition> = {
  name: { label: 'Name', defaultWidth: 270 },
  path: { label: 'Path', defaultWidth: 360 },
  kind: { label: 'Kind', defaultWidth: 110 },
  status: { label: 'Status', defaultWidth: 110 },
  author: { label: 'Author', defaultWidth: 180 },
  files: { label: 'Files', defaultWidth: 78, align: 'right' },
  variants: { label: 'Variants', defaultWidth: 88, align: 'right' },
  size: { label: 'Size', defaultWidth: 92, align: 'right' },
  modified: { label: 'Modified', defaultWidth: 176 },
  issues: { label: 'Issues', defaultWidth: 76, align: 'right' },
}
const defaultColumnOrder = Object.keys(columnDefinitions) as SortKey[]
const columnStorageKey = 'beamworlds.library-columns.v1'
const pageSizeStorageKey = 'beamworlds.library-page-size.v1'

export function LibraryView(props: LibraryViewProps) {
  const [page, setPage] = useState(0)
  const [pageSizeChoice, setPageSizeChoice] = useState<PageSize>(readPageSize)
  const [sortKey, setSortKey] = useState<SortKey>('name')
  const [sortDirection, setSortDirection] = useState<1 | -1>(1)
  const [columns, setColumns] = useState<ColumnState[]>(readColumns)
  const [columnsOpen, setColumnsOpen] = useState(false)
  const [draggedColumn, setDraggedColumn] = useState<SortKey | null>(null)
  const onSelectRef = useRef(props.onSelect)
  onSelectRef.current = props.onSelect
  const selectItem = useCallback((item: LibraryItem) => onSelectRef.current(item), [])
  const sorted = useMemo(() => [...props.items].sort((left, right) => compareValues(sortValue(left, sortKey), sortValue(right, sortKey)) * sortDirection), [props.items, sortKey, sortDirection])
  const pageSize = pageSizeChoice === 'all' ? Math.max(1, sorted.length) : pageSizeChoice
  const pageCount = Math.max(1, Math.ceil(sorted.length / pageSize))
  const visible = useMemo(() => sorted.slice(page * pageSize, (page + 1) * pageSize), [sorted, page, pageSize])
  const visibleColumns = useMemo(() => columns.filter(column => column.visible), [columns])
  const tableWidth = useMemo(() => visibleColumns.reduce((total, column) => total + column.width, 0), [visibleColumns])

  useEffect(() => setPage(0), [props.query, props.kind, props.status, props.folderID, sortKey, sortDirection, pageSizeChoice])
  useEffect(() => { if (page >= pageCount) setPage(pageCount - 1) }, [page, pageCount])
  useEffect(() => window.localStorage.setItem(columnStorageKey, JSON.stringify(columns)), [columns])
  useEffect(() => window.localStorage.setItem(pageSizeStorageKey, String(pageSizeChoice)), [pageSizeChoice])

  const changeSort = useCallback((key: SortKey) => {
    if (key === sortKey) setSortDirection(direction => direction === 1 ? -1 : 1)
    else { setSortKey(key); setSortDirection(1) }
  }, [sortKey])

  const moveColumn = useCallback((source: SortKey, target: SortKey) => {
    if (source === target) return
    setColumns(current => {
      const next = [...current]
      const sourceIndex = next.findIndex(column => column.key === source)
      const targetIndex = next.findIndex(column => column.key === target)
      if (sourceIndex < 0 || targetIndex < 0) return current
      const [moved] = next.splice(sourceIndex, 1)
      next.splice(targetIndex, 0, moved)
      return next
    })
  }, [])

  const resizeColumn = useCallback((event: React.PointerEvent, key: SortKey, width: number) => {
    event.preventDefault()
    event.stopPropagation()
    const startX = event.clientX
    const move = (pointer: PointerEvent) => setColumns(current => current.map(column => column.key === key ? { ...column, width: Math.max(64, Math.min(720, width + pointer.clientX - startX)) } : column))
    const finish = () => {
      window.removeEventListener('pointermove', move)
      window.removeEventListener('pointerup', finish)
    }
    window.addEventListener('pointermove', move)
    window.addEventListener('pointerup', finish)
  }, [])

  const toggleColumn = (key: SortKey) => setColumns(current => {
    const shown = current.filter(column => column.visible).length
    return current.map(column => column.key === key ? { ...column, visible: column.visible ? shown > 1 ? false : true : true } : column)
  })

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
    <header className="view-header view-header--compact"><h1>Mod Library</h1><div className="view-header__actions">{props.scanning ? <Button icon="close" onClick={props.onCancelScan}>Cancel scan</Button> : <Button icon="scan" tone="primary" onClick={props.onScan}>Scan configured folders</Button>}</div></header>
    <div className="library-summary" aria-label="Library summary"><Summary label="Linked" value={props.dashboard?.linked ?? 0}/><Summary label="Missing" value={props.dashboard?.unlinked ?? 0}/><Summary label="Vehicles" value={props.dashboard?.vehicles ?? 0}/><Summary label="Maps" value={props.dashboard?.maps ?? 0}/><Summary label="Projects" value={props.dashboard?.workspaces ?? 0}/></div>
    {props.scan && (props.scanning || props.scan.done) && <div className={`scan-strip ${props.scan.error ? 'scan-strip--error' : ''}`}><div className="scan-strip__pulse"><Icon name={props.scan.error ? 'error' : props.scan.done ? 'check' : 'scan'} size={15}/></div><strong>{props.scan.error ? 'Scan stopped' : props.scan.done ? 'Scan complete' : props.scan.phase === 'discovering' ? 'Discovering' : 'Analyzing'}</strong><span title={props.scan.path}>{props.scan.error || props.scan.path || 'Finalizing index'}</span><div className="scan-strip__metrics"><span>{props.scan.discovered} found</span><span>{props.scan.analyzed} processed</span>{props.scan.cached > 0 && <span>{props.scan.cached} cached</span>}{props.scan.failed > 0 && <span>{props.scan.failed} failed</span>}</div></div>}
    <div className="library-toolbar">
      <div className="segmented" aria-label="Link status filter">{['all', 'linked', 'unlinked'].map(value => <button key={value} className={props.status === value ? 'is-active' : ''} onClick={() => props.onStatusChange(value)}>{value === 'all' ? 'All' : value === 'unlinked' ? 'Missing' : 'Linked'}</button>)}</div>
      <label className="search-box"><Icon name="search" size={15}/><input value={props.query} onChange={event => props.onQueryChange(event.target.value)} placeholder="Search name, author, path, namespace" aria-label="Search mods"/>{props.query && <button onClick={() => props.onQueryChange('')} aria-label="Clear search"><Icon name="close" size={13}/></button>}</label>
      <label className="select-box"><span>Kind</span><select value={props.kind} onChange={event => props.onKindChange(event.target.value)}><option value="all">All kinds</option><option value="vehicle">Vehicle</option><option value="map">Map</option><option value="ui">UI app</option><option value="script">Script</option><option value="mixed">Mixed</option><option value="unknown">Unknown</option></select></label>
      <div className="column-control"><Button icon="columns" tone="quiet" onClick={() => setColumnsOpen(open => !open)}>Columns</Button>{columnsOpen && <div className="column-menu" role="dialog" aria-label="Visible and ordered columns"><header><strong>Table columns</strong><button className="icon-button" onClick={() => setColumnsOpen(false)} aria-label="Close column settings"><Icon name="close" size={13}/></button></header>{columns.map(column => <label key={column.key} draggable onDragStart={() => setDraggedColumn(column.key)} onDragOver={event => event.preventDefault()} onDrop={() => { if (draggedColumn) moveColumn(draggedColumn, column.key); setDraggedColumn(null) }}><Icon name="more" size={14}/><input type="checkbox" checked={column.visible} disabled={column.visible && visibleColumns.length === 1} onChange={() => toggleColumn(column.key)}/><span>{columnDefinitions[column.key].label}</span></label>)}</div>}</div>
      <span className="result-count">{props.items.length.toLocaleString()} mods</span>
    </div>
    <div className="library-workarea">
      <aside className="library-folders"><header><strong>Folders</strong><button className="icon-button" onClick={createFolder} title="Create folder"><Icon name="plus" size={14}/></button></header><button className={props.folderID === 'all' ? 'is-active' : ''} onClick={() => props.onFolderChange('all')}><Icon name="library" size={14}/><span>All mods</span><small>{props.dashboard?.entities ?? 0}</small></button><button className={props.folderID === 'unfiled' ? 'is-active' : ''} onClick={() => props.onFolderChange('unfiled')}><Icon name="folder" size={14}/><span>Unfiled</span></button>{props.folders.map(folder => <button key={folder.id} className={props.folderID === folder.id ? 'is-active' : ''} onClick={() => props.onFolderChange(folder.id)}><Icon name="folder" size={14}/><span>{folder.name}</span><small>{folder.modCount}</small></button>)}{activeFolder && <footer><button onClick={renameFolder}><Icon name="edit" size={12}/>Rename</button><button onClick={deleteFolder}><Icon name="trash" size={12}/>Delete</button></footer>}</aside>
      <div className="library-results">
        {visible.length === 0 ? <EmptyState icon="archive" title={props.scanning ? `Loading ${props.scan?.discovered ?? 0} mods` : 'No matching mods'} detail={props.scanning ? 'This may take a few seconds.' : 'Change the filters, select another folder, or scan the configured folders.'} action={!props.scanning && <Button icon="scan" tone="primary" onClick={props.onScan}>Scan configured folders</Button>}/> : <div className="mod-table-wrap"><table className="mod-table" style={{ width: tableWidth }}><colgroup>{visibleColumns.map(column => <col key={column.key} style={{ width: column.width }}/>)}</colgroup><thead><tr>{visibleColumns.map(column => <SortableHead key={column.key} column={column} active={sortKey} direction={sortDirection} onSort={changeSort} onDragStart={setDraggedColumn} onDrop={target => { if (draggedColumn) moveColumn(draggedColumn, target); setDraggedColumn(null) }} onResize={resizeColumn}/>)}</tr></thead><tbody>{visible.map(item => <ModRow key={item.entityId} item={item} columns={visibleColumns} selected={item.entityId === props.selectedID} onSelect={selectItem}/>)}</tbody></table></div>}
        {sorted.length > 0 && <footer className="pagination"><span/><div className="pagination__pages"><Button tone="quiet" disabled={page === 0} onClick={() => setPage(value => value - 1)}>Previous</Button><span>{page + 1} / {pageCount}</span><Button tone="quiet" disabled={page + 1 >= pageCount} onClick={() => setPage(value => value + 1)}>Next</Button></div><div className="page-size"><span>Per page</span>{([50, 100, 200, 500, 'all'] as PageSize[]).map(value => <button key={value} className={pageSizeChoice === value ? 'is-active' : ''} onClick={() => setPageSizeChoice(value)}>{value}</button>)}</div></footer>}
      </div>
    </div>
  </section>
}

function Summary({ label, value }: { label: string; value: number }) { return <span><strong>{value.toLocaleString()}</strong>{label}</span> }

const SortableHead = memo(function SortableHead({ column, active, direction, onSort, onDragStart, onDrop, onResize }: { column: ColumnState; active: SortKey; direction: 1 | -1; onSort: (value: SortKey) => void; onDragStart: (value: SortKey) => void; onDrop: (value: SortKey) => void; onResize: (event: React.PointerEvent, key: SortKey, width: number) => void }) {
  const selected = column.key === active
  return <th draggable onDragStart={() => onDragStart(column.key)} onDragOver={event => event.preventDefault()} onDrop={() => onDrop(column.key)} aria-sort={selected ? direction === 1 ? 'ascending' : 'descending' : 'none'}><button onClick={() => onSort(column.key)}>{columnDefinitions[column.key].label}<span>{selected ? direction === 1 ? '▲' : '▼' : '↕'}</span></button><i className="column-resizer" onPointerDown={event => onResize(event, column.key, column.width)}/></th>
})

const ModRow = memo(function ModRow({ item, columns, selected, onSelect }: { item: LibraryItem; columns: ColumnState[]; selected: boolean; onSelect: (item: LibraryItem) => void }) {
  return <tr className={selected ? 'is-selected' : ''} onClick={() => onSelect(item)} onDoubleClick={() => onSelect(item)} tabIndex={0} onKeyDown={event => { if (event.key === 'Enter' || event.key === ' ') onSelect(item) }}>{columns.map(column => <Cell key={column.key} item={item} column={column.key}/>)}</tr>
})

function Cell({ item, column }: { item: LibraryItem; column: SortKey }) {
  switch (column) {
  case 'name': return <td className="mod-table__name"><span className="mod-table__icon">{item.thumbnailUrl ? <img src={item.thumbnailUrl} alt="" loading="lazy" onError={event => { event.currentTarget.style.display = 'none' }}/> : <Icon name={kindIcon(String(item.kind))} size={16}/>}</span><strong>{item.displayName}</strong></td>
  case 'path': return <td className="mod-table__path" title={item.archivePath}><code>{item.archivePath || 'Source archive missing'}</code></td>
  case 'kind': return <td><Badge tone={item.kind === 'unknown' ? 'warning' : 'neutral'}>{kindLabel(String(item.kind))}</Badge></td>
  case 'status': return <td><span className={item.linked ? 'table-status table-status--linked' : 'table-status table-status--missing'}><i/>{item.linked ? 'Linked' : 'Missing'}</span></td>
  case 'author': return <td title={item.manifest?.author || ''}>{item.manifest?.author || '—'}</td>
  case 'files': return <td className="number-cell">{item.memberCount.toLocaleString()}</td>
  case 'variants': return <td className="number-cell">{item.variantCount.toLocaleString()}</td>
  case 'size': return <td className="number-cell">{formatBytes(item.sizeBytes)}</td>
  case 'modified': return <td>{item.modifiedAt ? formatDate(item.modifiedAt) : '—'}</td>
  case 'issues': return <td className={item.issueCount > 0 ? 'number-cell issue-cell' : 'number-cell'}>{item.issueCount.toLocaleString()}</td>
  }
}

function readColumns(): ColumnState[] {
  try {
    const parsed = JSON.parse(window.localStorage.getItem(columnStorageKey) ?? '[]') as ColumnState[]
    const known = new Set<SortKey>()
    const result = parsed.filter(column => column && column.key in columnDefinitions && !known.has(column.key) && known.add(column.key)).map(column => ({ key: column.key, visible: column.visible !== false, width: Math.max(64, Math.min(720, Number(column.width) || columnDefinitions[column.key].defaultWidth)) }))
    for (const key of defaultColumnOrder) if (!known.has(key)) result.push({ key, visible: true, width: columnDefinitions[key].defaultWidth })
    if (result.some(column => column.visible)) return result
  } catch {
    // Invalid local UI state falls back to the complete default table.
  }
  return defaultColumnOrder.map(key => ({ key, visible: true, width: columnDefinitions[key].defaultWidth }))
}

function readPageSize(): PageSize {
  const stored = window.localStorage.getItem(pageSizeStorageKey)
  if (stored === 'all') return 'all'
  const value = Number(stored)
  return value === 50 || value === 100 || value === 200 || value === 500 ? value : 100
}

function sortValue(item: LibraryItem, key: SortKey): string | number {
  switch (key) {
  case 'name': return item.displayName.toLowerCase()
  case 'path': return item.archivePath.toLowerCase()
  case 'kind': return String(item.kind)
  case 'status': return item.linked ? 0 : 1
  case 'author': return (item.manifest?.author || '').toLowerCase()
  case 'files': return item.memberCount
  case 'variants': return item.variantCount
  case 'size': return item.sizeBytes
  case 'modified': return item.modifiedAt ? new Date(item.modifiedAt).valueOf() : 0
  case 'issues': return item.issueCount
  }
}

function compareValues(left: string | number, right: string | number) { return typeof left === 'number' && typeof right === 'number' ? left - right : String(left).localeCompare(String(right), undefined, { numeric: true, sensitivity: 'base' }) }
