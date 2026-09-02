import { memo, useCallback, useEffect, useMemo, useRef, useState } from 'react'
import type { LibraryFolder, LibraryItem, ModTag, ScanProgress } from '../bindings/github.com/SignedAdam/beamng-mod-studio/models.js'
import { CollectionMenu } from './CollectionMenu'
import { Icon } from './icons'
import { LibrarySearch } from './LibrarySearch'
import { Badge, Button, EmptyState, formatBytes, formatDate, kindIcon, kindLabel, Spinner } from './ui'

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

type SortKey = 'name' | 'path' | 'kind' | 'status' | 'author' | 'tags' | 'files' | 'variants' | 'size' | 'modified' | 'issues'
type ColumnKey = 'thumbnail' | SortKey
type PageSize = 50 | 100 | 200 | 500 | 'all'
interface ColumnState { key: ColumnKey; visible: boolean; width: number }
interface ColumnDefinition { label: string; defaultWidth: number; minWidth?: number; align?: 'right' }

const columnDefinitions: Record<ColumnKey, ColumnDefinition> = {
  thumbnail: { label: 'Thumbnail', defaultWidth: 88, minWidth: 52 },
  name: { label: 'Name', defaultWidth: 270 },
  path: { label: 'Path', defaultWidth: 360 },
  kind: { label: 'Kind', defaultWidth: 110 },
  status: { label: 'Health', defaultWidth: 146 },
  author: { label: 'Author', defaultWidth: 180 },
  tags: { label: 'Tags', defaultWidth: 250 },
  files: { label: 'Files', defaultWidth: 78, align: 'right' },
  variants: { label: 'Variants', defaultWidth: 88, align: 'right' },
  size: { label: 'Size', defaultWidth: 92, align: 'right' },
  modified: { label: 'Modified', defaultWidth: 176 },
  issues: { label: 'Issues', defaultWidth: 76, align: 'right' },
}
const defaultColumnOrder = Object.keys(columnDefinitions) as ColumnKey[]
const columnStorageKey = 'beamworlds.library-columns.v2'
const pageSizeStorageKey = 'beamworlds.library-page-size.v1'

export function LibraryView(props: LibraryViewProps) {
  const [page, setPage] = useState(0)
  const [pageSizeChoice, setPageSizeChoice] = useState<PageSize>(readPageSize)
  const [sortKey, setSortKey] = useState<SortKey>('name')
  const [sortDirection, setSortDirection] = useState<1 | -1>(1)
  const [columns, setColumns] = useState<ColumnState[]>(readColumns)
  const [columnsOpen, setColumnsOpen] = useState(false)
  const [draggedColumn, setDraggedColumn] = useState<ColumnKey | null>(null)
  const [contextMenu, setContextMenu] = useState<{ item: LibraryItem; x: number; y: number } | null>(null)
  const lastResizePointer = useRef<{ key: ColumnKey; at: number } | null>(null)
  const onSelectRef = useRef(props.onSelect)
  onSelectRef.current = props.onSelect
  const selectItem = useCallback((item: LibraryItem) => onSelectRef.current(item), [])
  const sorted = useMemo(() => [...props.items].sort((left, right) => compareValues(sortValue(left, sortKey), sortValue(right, sortKey)) * sortDirection), [props.items, sortKey, sortDirection])
  const pageSize = pageSizeChoice === 'all' ? Math.max(1, sorted.length) : pageSizeChoice
  const pageCount = Math.max(1, Math.ceil(sorted.length / pageSize))
  const visible = useMemo(() => sorted.slice(page * pageSize, (page + 1) * pageSize), [sorted, page, pageSize])
  const visibleColumns = useMemo(() => columns.filter(column => column.visible), [columns])
  const tableWidth = useMemo(() => visibleColumns.reduce((total, column) => total + column.width, 0), [visibleColumns])
  const rangeStart = sorted.length === 0 ? 0 : page * pageSize + 1
  const rangeEnd = Math.min(sorted.length, (page + 1) * pageSize)

  useEffect(() => setPage(0), [props.query, props.folderID, sortKey, sortDirection, pageSizeChoice])
  useEffect(() => { if (page >= pageCount) setPage(pageCount - 1) }, [page, pageCount])
  useEffect(() => window.localStorage.setItem(columnStorageKey, JSON.stringify(columns)), [columns])
  useEffect(() => window.localStorage.setItem(pageSizeStorageKey, String(pageSizeChoice)), [pageSizeChoice])
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

  const changeSort = useCallback((key: SortKey) => {
    if (key === sortKey) setSortDirection(direction => direction === 1 ? -1 : 1)
    else { setSortKey(key); setSortDirection(1) }
  }, [sortKey])

  const moveColumn = useCallback((source: ColumnKey, target: ColumnKey) => {
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

  const resizeColumn = useCallback((event: React.PointerEvent, key: ColumnKey, width: number) => {
    event.preventDefault()
    event.stopPropagation()
    const startX = event.clientX
    const move = (pointer: PointerEvent) => {
      if (Math.abs(pointer.clientX - startX) > 2) lastResizePointer.current = null
      setColumns(current => current.map(column => column.key === key ? { ...column, width: Math.max(columnDefinitions[key].minWidth ?? 64, Math.min(720, width + pointer.clientX - startX)) } : column))
    }
    const finish = () => {
      window.removeEventListener('pointermove', move)
      window.removeEventListener('pointerup', finish)
    }
    window.addEventListener('pointermove', move)
    window.addEventListener('pointerup', finish)
  }, [])

  const autoSizeColumn = useCallback((event: React.PointerEvent<HTMLElement>, key: ColumnKey) => {
    event.preventDefault()
    event.stopPropagation()
    const header = event.currentTarget.closest('th') as HTMLTableCellElement | null
    const table = header?.closest('table') as HTMLTableElement | null
    const sampleCell = header && table ? table.tBodies[0]?.rows[0]?.cells[header.cellIndex] : null
    const headerContent = header?.querySelector<HTMLElement>(key === 'thumbnail' ? '.mod-table__column-label' : 'button')
    if (!header || !sampleCell || !headerContent) return
    const context = document.createElement('canvas').getContext('2d')
    if (!context) return

    const headerStyle = window.getComputedStyle(headerContent)
    context.font = `${headerStyle.fontStyle} ${headerStyle.fontWeight} ${headerStyle.fontSize} ${headerStyle.fontFamily}`
    const headerLabel = columnDefinitions[key].label.toUpperCase()
    const headerLetterSpacing = Number.parseFloat(headerStyle.letterSpacing) || 0
    const headerPadding = (Number.parseFloat(headerStyle.paddingLeft) || 0) + (Number.parseFloat(headerStyle.paddingRight) || 0)
    const sortMarkerWidth = key === 'thumbnail' ? 0 : context.measureText('↕').width + 12
    let optimalWidth = context.measureText(headerLabel).width + Math.max(0, headerLabel.length - 1) * headerLetterSpacing + headerPadding + sortMarkerWidth + 2

    const cellStyle = window.getComputedStyle(sampleCell)
    const cellPadding = (Number.parseFloat(cellStyle.paddingLeft) || 0) + (Number.parseFloat(cellStyle.paddingRight) || 0)
    if (key === 'thumbnail') {
      const thumbnail = sampleCell.querySelector<HTMLElement>('.mod-table__icon')
      optimalWidth = Math.max(optimalWidth, (thumbnail?.getBoundingClientRect().width ?? 30) + cellPadding + 2)
    } else {
      const textElement = key === 'name' ? sampleCell.querySelector<HTMLElement>('strong') : key === 'kind' ? sampleCell.querySelector<HTMLElement>('.badge') : sampleCell.firstElementChild as HTMLElement | null
      const textStyle = window.getComputedStyle(textElement ?? sampleCell)
      context.font = `${textStyle.fontStyle} ${textStyle.fontWeight} ${textStyle.fontSize} ${textStyle.fontFamily}`
      const letterSpacing = Number.parseFloat(textStyle.letterSpacing) || 0
      const contentPadding = key === 'kind'
        ? (Number.parseFloat(textStyle.paddingLeft) || 0) + (Number.parseFloat(textStyle.paddingRight) || 0) + (Number.parseFloat(textStyle.borderLeftWidth) || 0) + (Number.parseFloat(textStyle.borderRightWidth) || 0)
        : 0
      for (const item of props.items) {
        let text = ''
        switch (key) {
        case 'name': text = item.displayName; break
        case 'path': text = item.archivePath || 'Source archive missing'; break
        case 'kind': text = kindLabel(String(item.kind)).toUpperCase(); break
        case 'status': text = item.healthLabel || 'Not scanned'; break
        case 'author': text = item.manifest?.author || '—'; break
        case 'tags': text = (item.tags ?? []).map(tag => tag.name).join(', ') || '—'; break
        case 'files': text = item.memberCount.toLocaleString(); break
        case 'variants': text = item.variantCount.toLocaleString(); break
        case 'size': text = formatBytes(item.sizeBytes); break
        case 'modified': text = item.modifiedAt ? formatDate(item.modifiedAt) : '—'; break
        case 'issues': text = item.issueCount.toLocaleString(); break
        }
        const textWidth = context.measureText(text).width + Math.max(0, text.length - 1) * letterSpacing
        optimalWidth = Math.max(optimalWidth, textWidth + cellPadding + contentPadding + 2)
      }
    }
    const width = Math.ceil(Math.max(columnDefinitions[key].minWidth ?? 64, Math.min(720, optimalWidth)))
    setColumns(current => current.map(column => column.key === key ? { ...column, width } : column))
  }, [props.items])

  const beginColumnResize = useCallback((event: React.PointerEvent<HTMLElement>, key: ColumnKey, width: number) => {
    const previous = lastResizePointer.current
    if (previous?.key === key && event.timeStamp - previous.at <= 500) {
      lastResizePointer.current = null
      autoSizeColumn(event, key)
      return
    }
    lastResizePointer.current = { key, at: event.timeStamp }
    resizeColumn(event, key, width)
  }, [autoSizeColumn, resizeColumn])

  const toggleColumn = (key: ColumnKey) => setColumns(current => {
    const shown = current.filter(column => column.visible).length
    return current.map(column => column.key === key ? { ...column, visible: column.visible ? shown > 1 ? false : true : true } : column)
  })

  const activeCollection = props.folders.find(folder => folder.id === props.folderID)
  const appliedFilters: string[] = []
  const appliedQuery = props.query.trim()
  if (appliedQuery) appliedFilters.push(`search “${appliedQuery}”`)
  if (props.folderID === 'unfiled') appliedFilters.push('collection “Unfiled”')
  else if (props.folderID !== 'all') appliedFilters.push(`collection “${activeCollection?.name ?? 'Selected collection'}”`)
  const emptyFilterTitle = `No matching mods for these filters: ${appliedFilters.length > 0 ? appliedFilters.join(', ') : 'none'}.`

  return <>
  <section className="view library-view" aria-label="Mod library">
    <header className="view-header view-header--compact"><h1>Mod Library</h1><div className="view-header__actions">{props.scanning ? <Button icon="close" onClick={props.onCancelScan}>Cancel scan</Button> : <Button icon="scan" tone="primary" onClick={props.onScan} title="Scans configured folders for new or updated mods">Rescan mods</Button>}</div></header>
    {props.scan && (props.scanning || props.scan.done) && <div className={`scan-strip ${props.scan.error ? 'scan-strip--error' : ''}`}><div className="scan-strip__pulse"><Icon name={props.scan.error ? 'error' : props.scan.done ? 'check' : 'scan'} size={15}/></div><strong>{props.scan.error ? 'Scan stopped' : props.scan.done ? 'Scan complete' : props.scan.phase === 'discovering' ? 'Discovering' : 'Analyzing'}</strong><span title={props.scan.path}>{props.scan.error || props.scan.path || 'Finalizing index'}</span><div className="scan-strip__metrics"><span>{props.scan.discovered} found</span><span>{props.scan.analyzed} processed</span>{props.scan.cached > 0 && <span>{props.scan.cached} cached</span>}{props.scan.failed > 0 && <span>{props.scan.failed} failed</span>}</div></div>}
    <div className="library-toolbar">
      <CollectionMenu folders={props.folders} value={props.folderID} total={props.catalogItems.length} onChange={props.onFolderChange} onCreate={props.onCreateFolder} onRename={props.onRenameFolder} onDelete={props.onDeleteFolder}/>
      <LibrarySearch value={props.query} loading={props.loading} items={props.catalogItems} folders={props.folders} tags={props.tags} onChange={props.onQueryChange}/>
    </div>
    <div className="library-workarea">
      <div className="library-results" aria-busy={props.loading}>
        {visible.length === 0 ? (
          props.loading || props.scanning
            ? <div className="center-loader" role="status"><Spinner/><span>{props.scanning ? `Loading ${props.scan?.discovered ?? 0} mods` : 'Filtering mods'}</span></div>
            : <EmptyState icon="archive" title={emptyFilterTitle}/>
        ) : <div className="mod-table-wrap">
          <table className="mod-table" style={{ width: tableWidth }}>
            <colgroup>{visibleColumns.map(column => <col key={column.key} style={{ width: column.width }}/>)}</colgroup>
            <thead><tr>{visibleColumns.map(column => <SortableHead key={column.key} column={column} active={sortKey} direction={sortDirection} onSort={changeSort} onDragStart={setDraggedColumn} onDrop={target => { if (draggedColumn) moveColumn(draggedColumn, target); setDraggedColumn(null) }} onResize={beginColumnResize}/>)}</tr></thead>
            <tbody>{visible.map(item => <ModRow key={item.entityId} item={item} columns={visibleColumns} selected={item.entityId === props.selectedID} onSelect={selectItem} onContextMenu={(selected, event) => { selectItem(selected); setContextMenu({ item: selected, x: Math.min(event.clientX, window.innerWidth - 248), y: Math.min(event.clientY, window.innerHeight - 112) }) }}/>)}</tbody>
          </table>
        </div>}
        <footer className="pagination">
          <span className="pagination__summary" role="status" aria-live="polite">{props.loading ? 'Updating results…' : sorted.length === 0 ? '0 results' : `${rangeStart.toLocaleString()}–${rangeEnd.toLocaleString()} of ${sorted.length.toLocaleString()} results`}</span>
          <div className="pagination__pages"><Button tone="quiet" disabled={page === 0 || sorted.length === 0} onClick={() => setPage(value => value - 1)}>Previous</Button><span>{page + 1} / {pageCount}</span><Button tone="quiet" disabled={page + 1 >= pageCount || sorted.length === 0} onClick={() => setPage(value => value + 1)}>Next</Button></div>
          <div className="pagination__settings"><div className="page-size"><span>Per page</span>{([50, 100, 200, 500, 'all'] as PageSize[]).map(value => <button key={value} className={pageSizeChoice === value ? 'is-active' : ''} onClick={() => setPageSizeChoice(value)}>{value}</button>)}</div><div className="column-control"><Button icon="columns" tone="quiet" onClick={() => setColumnsOpen(open => !open)}>Columns</Button>{columnsOpen && <div className="column-menu" role="dialog" aria-label="Visible and ordered columns"><header><strong>Table columns</strong><button className="icon-button" onClick={() => setColumnsOpen(false)} aria-label="Close column settings"><Icon name="close" size={13}/></button></header>{columns.map(column => <label key={column.key} draggable onDragStart={() => setDraggedColumn(column.key)} onDragOver={event => event.preventDefault()} onDrop={() => { if (draggedColumn) moveColumn(draggedColumn, column.key); setDraggedColumn(null) }}><Icon name="more" size={14}/><input type="checkbox" checked={column.visible} disabled={column.visible && visibleColumns.length === 1} onChange={() => toggleColumn(column.key)}/><span>{columnDefinitions[column.key].label}</span></label>)}</div>}</div></div>
        </footer>
      </div>
    </div>
  </section>
    {contextMenu && <>
      <button className="mod-context-backdrop" aria-label="Close mod actions" onClick={() => setContextMenu(null)}/>
      <div className="mod-context-menu" role="menu" style={{ left: contextMenu.x, top: contextMenu.y }}>
        <button role="menuitem" disabled={!contextMenu.item.linked} onClick={() => { props.onVirusScan(contextMenu.item); setContextMenu(null) }}><Icon name="shield" size={17}/><span><strong>Scan for threats</strong><small>{contextMenu.item.linked ? 'Choose signature-based or full scan' : 'Source archive unavailable'}</small></span></button>
      </div>
    </>}
  </>
}

const SortableHead = memo(function SortableHead({ column, active, direction, onSort, onDragStart, onDrop, onResize }: { column: ColumnState; active: SortKey; direction: 1 | -1; onSort: (value: SortKey) => void; onDragStart: (value: ColumnKey) => void; onDrop: (value: ColumnKey) => void; onResize: (event: React.PointerEvent<HTMLElement>, key: ColumnKey, width: number) => void }) {
  const key = column.key
  if (key === 'thumbnail') {
    return <th draggable onDragStart={() => onDragStart(key)} onDragOver={event => event.preventDefault()} onDrop={() => onDrop(key)}><span className="mod-table__column-label">{columnDefinitions[key].label}</span><i className="column-resizer" title="Double-click to fit contents" onPointerDown={event => onResize(event, key, column.width)}/></th>
  }
  const selected = key === active
  return <th draggable onDragStart={() => onDragStart(key)} onDragOver={event => event.preventDefault()} onDrop={() => onDrop(key)} aria-sort={selected ? direction === 1 ? 'ascending' : 'descending' : 'none'}><button onClick={() => onSort(key)}>{columnDefinitions[key].label}<span>{selected ? direction === 1 ? '▲' : '▼' : '↕'}</span></button><i className="column-resizer" title="Double-click to fit contents" onPointerDown={event => onResize(event, key, column.width)}/></th>
})

const ModRow = memo(function ModRow({ item, columns, selected, onSelect, onContextMenu }: { item: LibraryItem; columns: ColumnState[]; selected: boolean; onSelect: (item: LibraryItem) => void; onContextMenu: (item: LibraryItem, event: React.MouseEvent<HTMLTableRowElement>) => void }) {
  return <tr className={selected ? 'is-selected' : ''} onClick={() => onSelect(item)} onDoubleClick={() => onSelect(item)} onContextMenu={event => { event.preventDefault(); onContextMenu(item, event) }} tabIndex={0} onKeyDown={event => { if (event.key === 'Enter' || event.key === ' ') onSelect(item) }}>{columns.map(column => <Cell key={column.key} item={item} column={column.key}/>)}</tr>
})

function Cell({ item, column }: { item: LibraryItem; column: ColumnKey }) {
  const tags = item.tags ?? []
  switch (column) {
  case 'thumbnail': return <td className="mod-table__thumbnail"><span className="mod-table__icon">{item.thumbnailUrl ? <img src={item.thumbnailUrl} alt="" loading="lazy" onError={event => { event.currentTarget.style.display = 'none' }}/> : <Icon name={kindIcon(String(item.kind))} size={16}/>}</span></td>
  case 'name': return <td className="mod-table__name"><strong>{item.displayName}</strong></td>
  case 'path': return <td className={`mod-table__path ${item.linked ? '' : 'is-unavailable'}`} title={item.archivePath || 'Source archive unavailable'}><span>{item.archivePath || 'Source unavailable'}</span></td>
  case 'kind': return <td><Badge tone={item.kind === 'unknown' ? 'warning' : 'neutral'}>{kindLabel(String(item.kind))}</Badge></td>
  case 'status': return <td title={healthDescription(item.healthStatus)}><span className={`health-pill health-pill--${item.healthStatus || 'unscanned'}`}><Icon name={healthIcon(item.healthStatus)} size={14}/>{item.healthLabel || 'Not scanned'}</span></td>
  case 'author': return <td title={item.manifest?.author || ''}>{item.manifest?.author || '—'}</td>
  case 'tags': return <td><div className="mod-table__tags">{tags.length === 0 ? <span>—</span> : <>{tags.slice(0, 3).map(tag => <span className="mod-tag" key={tag.id}>{tag.name}</span>)}{tags.length > 3 && <em>+{tags.length - 3}</em>}</>}</div></td>
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
    const known = new Set<ColumnKey>()
    const result = parsed.filter(column => column && column.key in columnDefinitions && !known.has(column.key) && known.add(column.key)).map(column => ({ key: column.key, visible: column.visible !== false, width: Math.max(columnDefinitions[column.key].minWidth ?? 64, Math.min(720, Number(column.width) || columnDefinitions[column.key].defaultWidth)) }))
    for (const key of defaultColumnOrder) {
      if (known.has(key)) continue
      const nextKnownKey = defaultColumnOrder.slice(defaultColumnOrder.indexOf(key) + 1).find(candidate => known.has(candidate))
      const nextIndex = nextKnownKey ? result.findIndex(column => column.key === nextKnownKey) : result.length
      result.splice(nextIndex < 0 ? result.length : nextIndex, 0, { key, visible: true, width: columnDefinitions[key].defaultWidth })
      known.add(key)
    }
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
  case 'status': return healthRank(item.healthStatus)
  case 'author': return (item.manifest?.author || '').toLowerCase()
  case 'tags': return (item.tags ?? []).map(tag => tag.name).join(' ').toLowerCase()
  case 'files': return item.memberCount
  case 'variants': return item.variantCount
  case 'size': return item.sizeBytes
  case 'modified': return item.modifiedAt ? new Date(item.modifiedAt).valueOf() : 0
  case 'issues': return item.issueCount
  }
}

function healthRank(status: string) {
  switch (status) {
  case 'threat': return 0
  case 'broken': return 1
  case 'review': return 2
  case 'scan_failed': return 3
  case 'scanning': return 4
  case 'unscanned': return 5
  case 'safe': return 6
  default: return 5
  }
}

function healthIcon(status: string): 'check' | 'warning' | 'error' | 'scan' | 'shield' {
  switch (status) {
  case 'safe': return 'check'
  case 'review': return 'warning'
  case 'threat':
  case 'broken':
  case 'scan_failed': return 'error'
  case 'scanning': return 'scan'
  default: return 'shield'
  }
}

function healthDescription(status: string) {
  switch (status) {
  case 'safe': return 'Latest scan found no threat signals'
  case 'review': return 'Latest scan found items that need review'
  case 'threat': return 'Latest scan found a high-risk threat'
  case 'broken': return 'The mod has structural errors'
  case 'scan_failed': return 'The latest scan failed'
  case 'scanning': return 'A virus scan is running'
  default: return 'No virus scan has run'
  }
}

function compareValues(left: string | number, right: string | number) { return typeof left === 'number' && typeof right === 'number' ? left - right : String(left).localeCompare(String(right), undefined, { numeric: true, sensitivity: 'base' }) }
