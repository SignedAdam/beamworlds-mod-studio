import { useMemo, useState } from 'react'
import type { FormEvent, MouseEvent as ReactMouseEvent } from 'react'
import { AppService as API } from '../bindings/github.com/SignedAdam/beamng-mod-studio/index.js'
import type { LibraryItem, ModCollection, ModTag, OrganizationState, ScanProgress } from '../bindings/github.com/SignedAdam/beamng-mod-studio/models.js'
import { CollectionDialog, CollectionMenuPopup } from './CollectionUI'
import { CollectionMenu } from './CollectionMenu'
import { Icon } from './icons'
import { LibraryPreviewGrid, type LibraryPreviewSize } from './LibraryPreviewGrid'
import { LibrarySearch } from './LibrarySearch'
import { ModTable, sortLibraryItems, type ModTableSort } from './ModTable'
import { Button, Page, type PageActionSpec } from './ui'

type LibraryViewMode = 'table' | 'preview'

const previewSizes: LibraryPreviewSize[] = ['small', 'medium', 'large']

function errorMessage(error: unknown): string {
  if (error instanceof Error && error.message.trim()) return error.message
  if (typeof error === 'string' && error.trim()) return error
  if (error && typeof error === 'object' && 'message' in error && typeof error.message === 'string' && error.message.trim()) return error.message
  return 'The collection membership could not be saved.'
}

const DEFAULT_TARGET = '__default__'
const NEW_TARGET = '__new__'

// Collections are named for the user, so adding mods never requires typing.
function nextCollectionName(collections: readonly ModCollection[]): string {
  const taken = new Set(collections.map(collection => collection.name.trim().toLowerCase()))
  for (let index = 1; ; index++) {
    const candidate = `Collection ${index}`
    if (!taken.has(candidate.toLowerCase())) return candidate
  }
}

export interface LibraryViewProps {
  items: LibraryItem[]
  catalogItems: LibraryItem[]
  collections: ModCollection[]
  tags: ModTag[]
  scan: ScanProgress | null
  scanning: boolean
  loading: boolean
  query: string
  collectionID: string
  selectedID: string
  onQueryChange: (value: string) => void
  onCollectionChange: (value: string) => void
  onManageCollections: () => void
  onOrganization: (state: OrganizationState) => void
  onError: (error: unknown) => void
  onSelect: (item: LibraryItem) => void
  onVirusScan: (item: LibraryItem) => void
  onScan: () => void
  onCancelScan: () => void
}

interface ContextMenuState {
  item: LibraryItem
  entityIDs: string[]
  x: number
  y: number
}

interface AddDialogState {
  entityIDs: string[]
}

export function LibraryView(props: LibraryViewProps) {
  const [viewMode, setViewMode] = useState<LibraryViewMode>('table')
  const [previewSize, setPreviewSize] = useState<LibraryPreviewSize>('medium')
  const [librarySort, setLibrarySort] = useState<ModTableSort>({ key: 'name', direction: 1 })
  const [selectedEntityIDs, setSelectedEntityIDs] = useState<Set<string>>(() => new Set())
  const [contextMenu, setContextMenu] = useState<ContextMenuState | null>(null)
  const [addDialog, setAddDialog] = useState<AddDialogState | null>(null)
  const [addTargetID, setAddTargetID] = useState('')
  const [nameDraft, setNameDraft] = useState('')
  const [namePromptOpen, setNamePromptOpen] = useState(false)
  const [addError, setAddError] = useState('')
  const [addBusy, setAddBusy] = useState(false)

  const toggleSelection = (item: LibraryItem) => {
    setSelectedEntityIDs(current => {
      const next = new Set(current)
      if (next.has(item.entityId)) next.delete(item.entityId)
      else next.add(item.entityId)
      return next
    })
  }

  const toggleAllMatching = () => {
    const matchingIDs = props.items.map(item => item.entityId)
    if (matchingIDs.length === 0) return
    setSelectedEntityIDs(current => {
      const next = new Set(current)
      const allSelected = matchingIDs.every(entityID => next.has(entityID))
      for (const entityID of matchingIDs) {
        if (allSelected) next.delete(entityID)
        else next.add(entityID)
      }
      return next
    })
  }

  const openAddDialog = (entityIDs: readonly string[]) => {
    const uniqueIDs = [...new Set(entityIDs.filter(Boolean))]
    if (uniqueIDs.length === 0) return
    setAddError('')
    setNamePromptOpen(false)
    setAddTargetID(props.collections[0]?.id ?? DEFAULT_TARGET)
    setContextMenu(null)
    setAddDialog({ entityIDs: uniqueIDs })
  }

  const closeAddDialog = () => {
    if (addBusy) return
    setAddError('')
    setAddDialog(null)
  }

  const addMods = async () => {
    if (!addDialog || addBusy || !addTargetID) return
    setAddBusy(true)
    setAddError('')
    let createdID = ''
    try {
      let target = addTargetID
      if (target === DEFAULT_TARGET) {
        const detail = await API.CreateCollection('Default collection', '', '')
        createdID = detail.collection.id
        target = createdID
      }
      await API.SetCollectionMods(target, addDialog.entityIDs, true)
      props.onOrganization(await API.Organization())
      setAddDialog(null)
    } catch (error) {
      if (createdID) {
        try {
          await API.DeleteCollections([createdID])
        } catch {
          // Keep the original membership error visible.
        }
      }
      setAddError(errorMessage(error))
      props.onError(error)
    } finally {
      setAddBusy(false)
    }
  }

  const createCollection = async (event?: FormEvent<HTMLFormElement>) => {
    event?.preventDefault()
    const name = nameDraft.trim()
    if (!name || addBusy) return
    setAddBusy(true)
    setAddError('')
    try {
      const detail = await API.CreateCollection(name, '', '')
      props.onOrganization(await API.Organization())
      setAddTargetID(detail.collection.id)
      setNamePromptOpen(false)
    } catch (error) {
      setAddError(errorMessage(error))
      props.onError(error)
    } finally {
      setAddBusy(false)
    }
  }

  const chooseTarget = (value: string) => {
    if (value !== NEW_TARGET) {
      setAddTargetID(value)
      return
    }
    setNameDraft(nextCollectionName(props.collections))
    setNamePromptOpen(true)
  }

  const openContextMenu = (selected: LibraryItem, event: ReactMouseEvent<HTMLElement>) => {
    event.preventDefault()
    props.onSelect(selected)
    const target = event.currentTarget
    const rect = target instanceof HTMLElement ? target.getBoundingClientRect() : null
    const x = event.clientX || rect?.right || 0
    const y = event.clientY || rect?.bottom || 0
    const entityIDs = selectedEntityIDs.has(selected.entityId)
      ? [...selectedEntityIDs]
      : [selected.entityId]
    if (!selectedEntityIDs.has(selected.entityId)) setSelectedEntityIDs(new Set(entityIDs))
    setContextMenu({
      item: selected,
      entityIDs,
      x: Math.max(8, Math.min(x, window.innerWidth - 16)),
      y: Math.max(8, Math.min(y, window.innerHeight - 16)),
    })
  }

  const pageActions: PageActionSpec[] = []
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
          title: 'Scans configured locations for new or updated mods',
        },
  )
  pageActions.push({
    key: 'open-mods-folder',
    label: 'Open BeamNG mods folder',
    icon: 'folder',
    role: 'secondary',
    onClick: () => { API.OpenGameDirectory().catch(props.onError) },
    title: 'Opens the folder BeamNG loads mods from',
  })

  const activeCollection = props.collections.find(collection => collection.id === props.collectionID)
  const appliedFilters: string[] = []
  const appliedQuery = props.query.trim()
  if (appliedQuery) appliedFilters.push(`search “${appliedQuery}”`)
  if (props.collectionID === 'unfiled') appliedFilters.push('collection “No collection”')
  else if (props.collectionID !== 'all') appliedFilters.push(`collection “${activeCollection?.name ?? 'Selected collection'}”`)
  const emptyFilterTitle = `No matching mods for these filters: ${appliedFilters.length > 0 ? appliedFilters.join(', ') : 'none'}.`
  const sortedItems = useMemo(
    () => (viewMode === 'preview' ? sortLibraryItems(props.items, librarySort) : []),
    [librarySort, props.items, viewMode],
  )
  const allMatchingSelected = props.items.length > 0 && props.items.every(item => selectedEntityIDs.has(item.entityId))
  const selectedCountLabel = `${selectedEntityIDs.size.toLocaleString()} mod${selectedEntityIDs.size === 1 ? '' : 's'} selected`

  return <Page title="Mod Library" className="library-view" ariaLabel="Mod library" actions={pageActions}>
    {props.scan && (props.scanning || Boolean(props.scan.error)) && <div className={`scan-strip ${props.scan.error && !props.scanning ? 'scan-strip--error' : ''}`}><div className="scan-strip__pulse"><Icon name={props.scan.error && !props.scanning ? 'error' : 'scan'} size={15}/></div><strong>{props.scan.error && !props.scanning ? 'Scan stopped' : props.scan.phase === 'discovering' ? 'Discovering' : 'Analyzing'}</strong><span title={props.scan.path}>{props.scan.error || props.scan.path || 'Finalizing index'}</span><div className="scan-strip__metrics"><span>{props.scan.discovered} found</span><span>{props.scan.analyzed} processed</span>{props.scan.cached > 0 && <span>{props.scan.cached} cached</span>}{props.scan.failed > 0 && <span>{props.scan.failed} failed</span>}</div></div>}
    <div className="page-toolbar">
      <LibrarySearch value={props.query} loading={props.loading} items={props.catalogItems} collections={props.collections} tags={props.tags} onChange={props.onQueryChange}/>
      <CollectionMenu collections={props.collections} value={props.collectionID} total={props.catalogItems.length} onChange={props.onCollectionChange} onManageCollections={props.onManageCollections}/>
      <div className="segmented" role="group" aria-label="Library layout">
        <button type="button" aria-pressed={viewMode === 'table'} className={viewMode === 'table' ? 'is-active' : ''} title="Table view" onClick={() => setViewMode('table')}><Icon name="columns" size={15}/><span>Table</span></button>
        <button type="button" aria-pressed={viewMode === 'preview'} className={viewMode === 'preview' ? 'is-active' : ''} title="Preview view" onClick={() => setViewMode('preview')}><Icon name="mixed" size={15}/><span>Preview</span></button>
      </div>
      {viewMode === 'preview' && <div className="segmented segmented--compact" role="group" aria-label="Preview size">
        {previewSizes.map(size => <button
          key={size}
          type="button"
          aria-pressed={previewSize === size}
          aria-label={`${size} previews`}
          title={`${size.charAt(0).toUpperCase()}${size.slice(1)} previews`}
          className={previewSize === size ? 'is-active' : ''}
          onClick={() => setPreviewSize(size)}
        >{size.charAt(0).toUpperCase()}</button>)}
      </div>}
    </div>
    {selectedEntityIDs.size > 0 && <div className="library-bulk-toolbar" role="toolbar" aria-label="Bulk mod actions">
      <span role="status" aria-live="polite">{selectedCountLabel}</span>
      <Button type="button" icon="folderPlus" onClick={() => openAddDialog([...selectedEntityIDs])}>Add to collection</Button>
      <Button type="button" tone="quiet" onClick={toggleAllMatching} disabled={props.items.length === 0}>{allMatchingSelected ? 'Clear matching selection' : 'Select all matching mods'}</Button>
      <Button type="button" tone="quiet" onClick={() => setSelectedEntityIDs(new Set())}>Clear selection</Button>
    </div>}
    <div className="library-workarea">
      {viewMode === 'preview' ? (
        <LibraryPreviewGrid
          items={sortedItems}
          selectedID={props.selectedID}
          selectedIDs={selectedEntityIDs}
          previewSize={previewSize}
          loading={props.loading}
          loadingLabel="Filtering mods"
          emptyTitle={emptyFilterTitle}
          ariaLabel="Mod library preview results"
          onSelect={props.onSelect}
          onToggle={toggleSelection}
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
            selectedIDs: selectedEntityIDs,
            onToggle: toggleSelection,
            onToggleAll: toggleAllMatching,
            selectAllLabel: 'Select all matching mods',
            onActivate: props.onSelect,
            onContextMenu: openContextMenu,
          }}
          loading={props.loading}
          loadingLabel="Filtering mods"
          emptyTitle={emptyFilterTitle}
          resetKey={`${props.query}\u0000${props.collectionID}`}
        />
      )}
    </div>
    {contextMenu && <CollectionMenuPopup
      label={`Actions for ${contextMenu.item.displayName}`}
      x={contextMenu.x}
      y={contextMenu.y}
      actions={[
        {
          label: contextMenu.entityIDs.length === 1 ? 'Add to collection' : `Add ${contextMenu.entityIDs.length.toLocaleString()} mods to collection`,
          icon: 'folderPlus',
          onClick: () => openAddDialog(contextMenu.entityIDs),
        },
        {
          label: 'Scan for threats',
          icon: 'shield',
          disabled: !contextMenu.item.linked,
          onClick: () => props.onVirusScan(contextMenu.item),
        },
      ]}
      onClose={() => setContextMenu(null)}
    />}
    {addDialog && <CollectionDialog
      title={`Add ${addDialog.entityIDs.length === 1 ? 'mod' : `${addDialog.entityIDs.length.toLocaleString()} mods`} to collection?`}
      onClose={closeAddDialog}
      footer={<><Button type="button" onClick={closeAddDialog} disabled={addBusy}>Cancel</Button><Button type="button" tone="primary" disabled={addBusy || !addTargetID} onClick={() => void addMods()}>Add</Button></>}
    >
      <ul className="collection-add__mods">
        {addDialog.entityIDs.map(entityID => {
          const item = props.catalogItems.find(candidate => candidate.entityId === entityID)
          return <li key={entityID}>{item?.displayName || entityID}</li>
        })}
      </ul>
      <label className="collection-add__field">
        <span>Collection</span>
        <select value={addTargetID} onChange={event => chooseTarget(event.target.value)} disabled={addBusy}>
          {props.collections.length === 0 && <option value={DEFAULT_TARGET}>Default collection</option>}
          {props.collections.map(collection => <option key={collection.id} value={collection.id}>{collection.name}</option>)}
          <option value={NEW_TARGET}>New collection…</option>
        </select>
      </label>
      {addError && <p className="collection-add__error" role="alert">{addError}</p>}
    </CollectionDialog>}
    {namePromptOpen && <CollectionDialog
      title="New collection"
      onClose={() => { if (!addBusy) setNamePromptOpen(false) }}
      footer={<><Button type="button" onClick={() => setNamePromptOpen(false)} disabled={addBusy}>Cancel</Button><Button type="button" tone="primary" disabled={addBusy || !nameDraft.trim()} onClick={() => void createCollection()}>Create</Button></>}
    >
      <form className="collection-add__field" onSubmit={createCollection}>
        <span>Name</span>
        <input autoFocus value={nameDraft} onChange={event => { setNameDraft(event.target.value); setAddError('') }} disabled={addBusy} aria-label="Collection name" />
      </form>
      {addError && <p className="collection-add__error" role="alert">{addError}</p>}
    </CollectionDialog>}
  </Page>
}

