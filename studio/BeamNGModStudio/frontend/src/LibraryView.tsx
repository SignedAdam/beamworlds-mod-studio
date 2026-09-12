import { useMemo, useState } from 'react'
import type { FormEvent, MouseEvent as ReactMouseEvent } from 'react'
import { AppService as API } from '../bindings/github.com/SignedAdam/beamng-mod-studio/index.js'
import type { LibraryItem, ModCollection, ModRemovalImpact, ModTag, OrganizationState, ScanProgress } from '../bindings/github.com/SignedAdam/beamng-mod-studio/models.js'
import { CollectionDialog, CollectionMenuPopup, type CollectionMenuAction } from './CollectionUI'
import { CollectionMenu } from './CollectionMenu'
import { Icon } from './icons'
import { LibraryPreviewGrid, type LibraryPreviewSize } from './LibraryPreviewGrid'
import { LibrarySearch } from './LibrarySearch'
import { ModTable, sortLibraryItems, type ModTableSort } from './ModTable'
import { Button, Page, formatBytes, type PageActionSpec } from './ui'

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
  onRemoved: () => void
  onNotify: (message: string, tone?: 'success' | 'error' | 'info') => void
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
  // Anchor for the selection toolbar's menu, so the same actions are reachable
  // without pointing at a selected row.
  const [selectionMenu, setSelectionMenu] = useState<{ x: number; y: number } | null>(null)
  const [addDialog, setAddDialog] = useState<AddDialogState | null>(null)
  const [addTargetID, setAddTargetID] = useState('')
  const [nameDraft, setNameDraft] = useState('')
  const [namePromptOpen, setNamePromptOpen] = useState(false)
  const [addError, setAddError] = useState('')
  const [addBusy, setAddBusy] = useState(false)
  // Removal is two operations: forgetting drops the index entry, deleting also
  // sends the archive to the Recycle Bin. The dialog states which one it is.
  const [removal, setRemoval] = useState<{ mode: 'forget' | 'delete'; impact: ModRemovalImpact } | null>(null)
  const [removalBusy, setRemovalBusy] = useState(false)
  const [removalError, setRemovalError] = useState('')

  const removalMods = removal?.impact.mods ?? []

  const openRemoval = async (mode: 'forget' | 'delete', entityIDs: string[]) => {
    setContextMenu(null)
    setRemovalError('')
    try {
      const impact = await API.PlanModRemoval(entityIDs)
      setRemoval({ mode, impact })
    } catch (error) {
      props.onError(error)
    }
  }

  const confirmRemoval = async () => {
    if (!removal) return
    setRemovalBusy(true)
    setRemovalError('')
    try {
      const entityIDs = removalMods.map(mod => mod.entityId)
      const result = removal.mode === 'delete'
        ? await API.DeleteModArchives(entityIDs)
        : await API.ForgetMods(entityIDs)
      const failures = result.failures ?? []
      if (failures.length > 0) {
        // Anything still on disk stays in the library, so the dialog remains
        // open with the reason rather than reporting a clean success.
        setRemovalError(failures.join(' · '))
        setRemovalBusy(false)
        props.onRemoved()
        return
      }
      setSelectedEntityIDs(current => {
        const next = new Set(current)
        for (const entityID of entityIDs) next.delete(entityID)
        return next
      })
      setRemoval(null)
      props.onNotify(
        removal.mode === 'delete'
          ? `Deleted ${result.recycled.toLocaleString()} archive${result.recycled === 1 ? '' : 's'} to the Recycle Bin`
          : `Forgot ${result.forgotten.toLocaleString()} mod${result.forgotten === 1 ? '' : 's'}`,
        'success',
      )
      props.onRemoved()
    } catch (error) {
      setRemovalError(errorMessage(error))
    } finally {
      setRemovalBusy(false)
    }
  }

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

  // One definition for both entry points: the right-click menu on a row and
  // the selection toolbar. A selection can scroll far out of view, so its
  // actions must not depend on pointing at a selected row.
  const modActions = (entityIDs: string[], scanTarget: LibraryItem | null): CollectionMenuAction[] => {
    const count = entityIDs.length
    const actions: CollectionMenuAction[] = [{
      label: count === 1 ? 'Add to collection' : `Add ${count.toLocaleString()} mods to collection`,
      icon: 'folderPlus',
      onClick: () => openAddDialog(entityIDs),
    }]
    if (scanTarget) {
      actions.push({
        label: 'Scan for threats',
        icon: 'shield',
        disabled: !scanTarget.linked,
        detail: 'The archive is no longer on disk',
        onClick: () => props.onVirusScan(scanTarget),
      })
    }
    actions.push({
      label: count === 1 ? 'Forget mod' : `Forget ${count.toLocaleString()} mods`,
      icon: 'unlink',
      detail: 'Removes it from the library and leaves the file alone',
      onClick: () => void openRemoval('forget', entityIDs),
    }, {
      label: count === 1 ? 'Delete archive…' : `Delete ${count.toLocaleString()} archives…`,
      icon: 'trash',
      danger: true,
      detail: 'Sends the file to the Recycle Bin',
      onClick: () => void openRemoval('delete', entityIDs),
    })
    return actions
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
  // The toolbar chip has to stay narrow; the long form is for screen readers.
  const selectedChipLabel = `${selectedEntityIDs.size.toLocaleString()} selected`
  // Threat scanning takes one archive, so the toolbar menu only offers it when
  // the selection is a single mod. That row may be filtered out of view, so
  // resolve it against the whole catalog.
  const soleSelectedItem = useMemo(() => {
    if (selectedEntityIDs.size !== 1) return null
    const [entityID] = [...selectedEntityIDs]
    return props.catalogItems.find(item => item.entityId === entityID) ?? null
  }, [props.catalogItems, selectedEntityIDs])

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
      {selectedEntityIDs.size > 0 && <div className="library-selection" role="group" aria-label="Selected mods">
        <span className="library-selection__count" role="status" aria-live="polite" aria-label={selectedCountLabel}>{selectedChipLabel}</span>
        <Button
          type="button"
          icon="folderPlus"
          className="library-selection__add"
          title="Add the selected mods to a collection"
          aria-label={`Add ${selectedCountLabel} to collection`}
          onClick={() => openAddDialog([...selectedEntityIDs])}
        >Add to collection</Button>
        <Button
          type="button"
          icon="more"
          className="library-selection__more"
          aria-haspopup="menu"
          aria-expanded={Boolean(selectionMenu)}
          aria-label={`Actions for ${selectedCountLabel}`}
          title="More actions for the selected mods"
          onClick={event => {
            const rect = event.currentTarget.getBoundingClientRect()
            setSelectionMenu({ x: rect.right, y: rect.bottom + 4 })
          }}
        >Actions</Button>
        <button
          type="button"
          className="icon-button library-selection__clear"
          aria-label="Clear selection"
          title="Clear selection"
          onClick={() => setSelectedEntityIDs(new Set())}
        ><Icon name="close" size={15}/></button>
      </div>}
    </div>
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
      actions={modActions(contextMenu.entityIDs, contextMenu.item)}
      onClose={() => setContextMenu(null)}
    />}
    {selectionMenu && selectedEntityIDs.size > 0 && <CollectionMenuPopup
      label={`Actions for ${selectedCountLabel}`}
      x={selectionMenu.x}
      y={selectionMenu.y}
      actions={[
        {
          label: allMatchingSelected ? 'Clear matching selection' : 'Select all matching mods',
          icon: 'check',
          disabled: props.items.length === 0,
          onClick: toggleAllMatching,
        },
        ...modActions([...selectedEntityIDs], soleSelectedItem),
      ]}
      onClose={() => setSelectionMenu(null)}
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
    {removal && <CollectionDialog
      title={removal.mode === 'delete'
        ? `Delete ${removalMods.length === 1 ? 'this archive' : `${removalMods.length.toLocaleString()} archives`}?`
        : `Forget ${removalMods.length === 1 ? 'this mod' : `${removalMods.length.toLocaleString()} mods`}?`}
      onClose={() => { if (!removalBusy) setRemoval(null) }}
      footer={<>
        <Button type="button" onClick={() => setRemoval(null)} disabled={removalBusy}>Cancel</Button>
        <Button type="button" tone={removal.mode === 'delete' ? 'danger' : 'primary'} disabled={removalBusy} onClick={() => void confirmRemoval()}>
          {removal.mode === 'delete' ? 'Delete to Recycle Bin' : 'Forget'}
        </Button>
      </>}
    >
      <p className="library-removal__copy">
        {removal.mode === 'delete'
          ? 'The archives go to the Recycle Bin, so you can restore them from Windows. The mods also leave the library.'
          : 'The mods leave the library. Every file stays exactly where it is, and the next scan will find them again.'}
      </p>
      <ul className="library-removal__mods">
        {removalMods.map(mod => <li key={mod.entityId}>
          <strong>{mod.displayName || mod.entityId}</strong>
          <small>{mod.missing ? 'Archive already gone' : `${mod.archivePath} · ${formatBytes(mod.sizeBytes)}`}</small>
        </li>)}
      </ul>
      {(removal.impact.collections ?? []).length > 0 && <p className="library-removal__warning">
        <Icon name="warning" size={14} />
        <span>Also removed from {(removal.impact.collections ?? []).join(', ')}.</span>
      </p>}
      {(removal.impact.workspaces ?? []).length > 0 && <p className="library-removal__warning" role="alert">
        <Icon name="warning" size={14} />
        <span>Open in ModMaker: {(removal.impact.workspaces ?? []).join(', ')}. Delete the project first.</span>
      </p>}
      {removal.mode === 'delete' && removal.impact.archiveCount > 0 && <p className="library-removal__copy">
        {formatBytes(removal.impact.archiveBytes)} across {removal.impact.archiveCount.toLocaleString()} file{removal.impact.archiveCount === 1 ? '' : 's'}.
      </p>}
      {removalError && <p className="collection-add__error" role="alert">{removalError}</p>}
    </CollectionDialog>}
  </Page>
}

