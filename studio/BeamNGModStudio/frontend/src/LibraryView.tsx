import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import type { FormEvent, MouseEvent as ReactMouseEvent } from 'react'
import { AppService as API } from '../bindings/github.com/SignedAdam/beamng-mod-studio/index.js'
import type { LibraryGroupPage as GroupPage, LibraryGroupRow, LibraryItem, ModCollection, ModFamily, ModRemovalImpact, ModTag, OrganizationState, ScanProgress } from '../bindings/github.com/SignedAdam/beamng-mod-studio/models.js'
import { AddModDialog } from './AddModDialog'
import { CollectionDialog, CollectionMenuPopup, type CollectionMenuAction } from './CollectionUI'
import { DuplicatesDialog } from './DuplicatesDialog'
import { CollectionMenu } from './CollectionMenu'
import { Icon } from './icons'
import { LibraryPreviewGrid, type LibraryPreviewSize } from './LibraryPreviewGrid'
import { LibrarySearch } from './LibrarySearch'
import { useFileManagerLabel } from './fileManager'
import { ModTable, sortLibraryItems, type ModFamilyBadge, type ModTableSort } from './ModTable'
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
  families: ModFamily[]
  scanning: boolean
  loading: boolean
  query: string
  collectionID: string
  selectedID: string
  scope: 'active' | 'archived'
  // Bumped by the app whenever a mod's own data changes outside this view, so
  // the server-rendered grouped rows are refetched instead of going stale.
  libraryRevision: number
  onQueryChange: (value: string) => void
  onCollectionChange: (value: string) => void
  onScopeChange: (scope: 'active' | 'archived') => void
  onManageCollections: () => void
  onOrganization: (state: OrganizationState) => void
  onError: (error: unknown) => void
  onSelect: (item: LibraryItem) => void
  onVirusScan: (item: LibraryItem) => void
  onScan: () => void
  onCancelScan: () => void
  onRemoved: () => void
  onImported: () => Promise<void>
  onRefreshFamilies: () => Promise<void>
  onFamiliesChange: (families: ModFamily[]) => void
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
  const fileManagerLabel = useFileManagerLabel()
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
  const [importOpen, setImportOpen] = useState(false)
  // Removal sends the archive to the Recycle Bin; the mod leaves the library
  // with it. Forgetting was removed: it left the file, so the next scan
  // re-indexed the mod and undid the action.
  const [duplicatesOpen, setDuplicatesOpen] = useState(false)
  const [duplicateFocusFamilyID, setDuplicateFocusFamilyID] = useState('')
  const [removal, setRemoval] = useState<{ impact: ModRemovalImpact } | null>(null)
  const [removalBusy, setRemovalBusy] = useState(false)
  const [removalError, setRemovalError] = useState('')

  // Grouping mode: when enabled, the table renders server-paged group rows.
  const GROUPING_KEY = 'beamworlds.library-grouped'
  const PAGE_SIZE_KEY = 'beamworlds.library-page-size.v1'
  const [grouped, setGrouped] = useState(() => window.localStorage.getItem(GROUPING_KEY) === 'true')
  const [groupData, setGroupData] = useState<GroupPage | null>(null)
  const [groupPage, setGroupPage] = useState(0)
  const [groupPageSize, setGroupPageSize] = useState(() => {
    const stored = window.localStorage.getItem(PAGE_SIZE_KEY)
    if (stored === 'all') return 0
    const n = Number(stored)
    return n > 0 ? n : 50
  })
  const [groupLoading, setGroupLoading] = useState(false)
  const groupLoadVersion = useRef(0)
  // Cache mod rows per group for instant fold/unfold. Invalidated when
  // the underlying stream changes (search, filter, scope, external edits).
  const groupRowCacheRef = useRef(new Map<string, LibraryGroupRow[]>())
  const cacheKeyRef = useRef('')
  const [groupNameDraft, setGroupNameDraft] = useState('')
  const [groupCreateOpen, setGroupCreateOpen] = useState(false)
  const [groupCreateBusy, setGroupCreateBusy] = useState(false)
  const [groupCreateError, setGroupCreateError] = useState('')
  const [groupAddOpen, setGroupAddOpen] = useState(false)
  const [groupAddTarget, setGroupAddTarget] = useState('')
  const [groupRemoveOpen, setGroupRemoveOpen] = useState(false)
  const [groupRemoveTarget, setGroupRemoveTarget] = useState('')
  const [manageGroupsOpen, setManageGroupsOpen] = useState(false)
  const [promotionBusy, setPromotionBusy] = useState('')

  const groupedTags = useMemo(() => (props.tags ?? []).filter(t => t.grouped), [props.tags])

  // Invalidate cache when the stream parameters change.
  const cacheKey = `${props.query}\0${props.collectionID}\0${props.scope}\0${props.libraryRevision}`
  if (cacheKeyRef.current !== cacheKey) {
    cacheKeyRef.current = cacheKey
    groupRowCacheRef.current.clear()
  }

  const loadGroupPage = useCallback(async (
    pageNum: number,
    pageSize?: number,
    silent = false,
  ) => {
    const version = ++groupLoadVersion.current
    if (!silent) setGroupLoading(true)
    try {
      const ps = pageSize ?? groupPageSize
      const result = await API.LibraryGroupPage(
        'all', 'all',
        props.query,
        props.collectionID,
        props.scope,
        pageNum, ps,
      )
      if (version !== groupLoadVersion.current) return
      setGroupData(result)
      // Populate cache from server data: store mod rows for each expanded group.
      const cache = groupRowCacheRef.current
      let gid = ''
      for (const row of result.rows ?? []) {
        if (row.rowType === 'group') {
          gid = row.groupId
          if (!row.collapsed) cache.set(gid, [])
        } else if (gid && cache.has(gid)) {
          cache.get(gid)!.push(row)
        }
      }
    } catch (error) {
      if (version !== groupLoadVersion.current) return
      if (!silent) props.onError(error)
    } finally {
      if (version === groupLoadVersion.current) setGroupLoading(false)
    }
  }, [props.query, props.collectionID, props.scope, props.onError, groupPageSize])

  // Reload group page when any input to the grouped stream changes.
  // loadGroupPage identity encodes query, collectionID, scope, and groupPageSize.
  // props.libraryRevision covers edits made outside this view — assigning tags
  // in the details sidebar rewrites a row the server already sent us, and the
  // grouped table renders those rows rather than the item list App updates.
  useEffect(() => {
    if (!grouped) return
    void loadGroupPage(groupPage)
  }, [grouped, groupPage, loadGroupPage, props.libraryRevision])

  // Reset group page on filter change.
  useEffect(() => {
    if (grouped) setGroupPage(0)
  }, [props.query, props.collectionID, props.scope])

  const toggleGrouped = (on: boolean) => {
    setGrouped(on)
    try { window.localStorage.setItem(GROUPING_KEY, String(on)) } catch {}
    if (on) {
      setGroupPage(0)
      void loadGroupPage(0)
    }
  }

  const handleToggleGroupCollapsed = (groupId: string, collapsed: boolean) => {
    const cache = groupRowCacheRef.current
    if (collapsed) {
      // Collapsing: strip mod rows for this group, cache them, update instantly.
      setGroupData(prev => {
        if (!prev?.rows) return prev
        const removed: LibraryGroupRow[] = []
        const next: LibraryGroupRow[] = []
        let inTarget = false
        for (const row of prev.rows) {
          if (row.rowType === 'group') {
            inTarget = row.groupId === groupId
            next.push(inTarget ? { ...row, collapsed: true } : row)
          } else if (inTarget) {
            removed.push(row)
          } else {
            next.push(row)
          }
        }
        if (removed.length > 0) cache.set(groupId, removed)
        return { ...prev, rows: next, totalRows: prev.totalRows - removed.length }
      })
      API.SetGroupCollapsed(groupId, true).catch(props.onError)
    } else {
      // Expanding: restore from cache if available, else fetch.
      const cached = cache.get(groupId)
      setGroupData(prev => {
        if (!prev?.rows) return prev
        const next: LibraryGroupRow[] = []
        for (const row of prev.rows) {
          if (row.rowType === 'group' && row.groupId === groupId) {
            next.push({ ...row, collapsed: false })
            if (cached) next.push(...cached)
          } else {
            next.push(row)
          }
        }
        return { ...prev, rows: next, totalRows: prev.totalRows + (cached?.length ?? 0) }
      })
      if (!cached) {
        // No cache — persist then fetch the actual rows.
        API.SetGroupCollapsed(groupId, false)
          .then(() => loadGroupPage(groupPage))
          .catch(props.onError)
        return
      }
      API.SetGroupCollapsed(groupId, false).catch(props.onError)
    }
  }

  const handleGroupPageChange = (page: number) => {
    setGroupPage(page)
  }

  const handlePageSizeChange = useCallback(() => {
    const stored = window.localStorage.getItem(PAGE_SIZE_KEY)
    const next = stored === 'all' ? 0 : (Number(stored) > 0 ? Number(stored) : 50)
    setGroupPageSize(next)
    setGroupPage(0)
  }, [])

  const addModsToGroup = async (tagID: string, entityIDs: string[]) => {
    try {
      const result = await API.AddModsToGroup(tagID, entityIDs)
      props.onOrganization(result)
      if (grouped) void loadGroupPage(groupPage)
    } catch (error) {
      props.onError(error)
    }
  }

  const removeModsFromGroup = async (tagID: string, entityIDs: string[]) => {
    try {
      const result = await API.RemoveModsFromGroup(tagID, entityIDs)
      props.onOrganization(result)
      if (grouped) void loadGroupPage(groupPage)
    } catch (error) {
      props.onError(error)
    }
  }

  const createGroupFromSelection = async (name: string, entityIDs: string[]) => {
    try {
      const result = await API.CreateGroupFromSelection(name, entityIDs)
      props.onOrganization(result)
      if (grouped) void loadGroupPage(groupPage)
    } catch (error) {
      props.onError(error)
    }
  }

  const toggleTagGrouped = async (tagID: string, on: boolean) => {
    setPromotionBusy(tagID)
    try {
      const result = await API.SetTagGrouped(tagID, on)
      props.onOrganization(result)
      if (grouped) void loadGroupPage(groupPage)
    } catch (error) {
      props.onError(error)
    } finally {
      setPromotionBusy('')
    }
  }

  // Switching between active and archived clears the selection since the two
  // sets never overlap.
  useEffect(() => {
    setSelectedEntityIDs(new Set())
  }, [props.scope])

  const archiveOrRestore = async (entityIDs: string[]) => {
    setContextMenu(null)
    setSelectionMenu(null)
    try {
      const isRestore = props.scope === 'archived'
      const result = isRestore
        ? await API.RestoreMods(entityIDs)
        : await API.ArchiveMods(entityIDs)
      const failures = result.failures ?? []
      if (failures.length > 0) {
        props.onNotify(failures.join(' · '), 'error')
      } else {
        const messages: string[] = []
        if (isRestore) {
          messages.push(`Restored ${result.restored.toLocaleString()} mod${result.restored === 1 ? '' : 's'}`)
          if (result.reenabledMemberships > 0)
            messages.push(`re-enabled in ${result.reenabledMemberships.toLocaleString()} collection${result.reenabledMemberships === 1 ? '' : 's'}`)
        } else {
          messages.push(`Archived ${result.archived.toLocaleString()} mod${result.archived === 1 ? '' : 's'}`)
          if (result.disabledMemberships > 0)
            messages.push(`disabled in ${result.disabledMemberships.toLocaleString()} collection${result.disabledMemberships === 1 ? '' : 's'}`)
        }
        props.onNotify(messages.join(' · '), 'success')
      }
      setSelectedEntityIDs(current => {
        const next = new Set(current)
        for (const entityID of entityIDs) next.delete(entityID)
        return next
      })
      props.onRemoved()
    } catch (error) {
      props.onError(error)
    }
  }

  const removalMods = removal?.impact.mods ?? []
  const removalWorkspaces = removal?.impact.workspaces ?? []

  const openRemoval = async (entityIDs: string[]) => {
    setContextMenu(null)
    setRemovalError('')
    try {
      const impact = await API.PlanModRemoval(entityIDs)
      setRemoval({ impact })
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
      const hasWorkspaces = removalWorkspaces.length > 0
      const result = hasWorkspaces
        ? await API.DeleteModArchivesAndWorkspaces(entityIDs)
        : await API.DeleteModArchives(entityIDs)
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
      const projectNote = hasWorkspaces ? ' and removed ModMaker projects' : ''
      props.onNotify(
        `Deleted ${result.recycled.toLocaleString()} archive${result.recycled === 1 ? '' : 's'} to the Recycle Bin${projectNote}`,
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
    const isArchived = props.scope === 'archived'
    const actions: CollectionMenuAction[] = []
    if (!isArchived) {
      actions.push({
        label: count === 1 ? 'Add to collection' : `Add ${count.toLocaleString()} mods to collection`,
        icon: 'folderPlus',
        onClick: () => openAddDialog(entityIDs),
      })
      if (groupedTags.length > 0) {
        actions.push({
          label: count === 1 ? 'Add to group' : `Add ${count.toLocaleString()} mods to group`,
          icon: 'folder',
          onClick: () => {
            setGroupAddTarget(groupedTags[0].id)
            setGroupAddOpen(true)
            setContextMenu(null)
            setSelectionMenu(null)
          },
        })
        actions.push({
          label: count === 1 ? 'Remove from group' : `Remove ${count.toLocaleString()} mods from group`,
          icon: 'close',
          onClick: () => {
            setGroupRemoveTarget(groupedTags[0].id)
            setGroupRemoveOpen(true)
            setContextMenu(null)
            setSelectionMenu(null)
          },
        })
      }
      actions.push({
        label: 'New group from selection',
        icon: 'plus',
        onClick: () => {
          setGroupNameDraft('')
          setGroupCreateError('')
          setGroupCreateOpen(true)
          setContextMenu(null)
          setSelectionMenu(null)
        },
      })
    }
    if (scanTarget) {
      actions.push({
        label: 'Scan for threats',
        icon: 'shield',
        disabled: !scanTarget.linked,
        detail: 'The archive is no longer on disk',
        onClick: () => props.onVirusScan(scanTarget),
      })
      actions.push({
        label: fileManagerLabel,
        icon: 'folder',
        disabled: !scanTarget.linked,
        detail: 'The archive is no longer on disk',
        onClick: () => {
          setContextMenu(null)
          setSelectionMenu(null)
          API.RevealLibraryArchive(scanTarget.entityId).catch(props.onError)
        },
      })
    }
    actions.push({
      label: isArchived
        ? (count === 1 ? 'Restore' : `Restore ${count.toLocaleString()} mods`)
        : (count === 1 ? 'Archive' : `Archive ${count.toLocaleString()} mods`),
      icon: isArchived ? 'refresh' : 'archive',
      detail: isArchived
        ? 'Returns it to the library and re-enables affected collections'
        : 'Hides it from the library and stops it shipping from collections',
      onClick: () => void archiveOrRestore(entityIDs),
    })
    actions.push({
      label: count === 1 ? 'Delete archive…' : `Delete ${count.toLocaleString()} archives…`,
      icon: 'trash',
      danger: true,
      detail: 'Sends the file to the Recycle Bin',
      onClick: () => void openRemoval(entityIDs),
    })
    return actions
  }

  // A mod can be both an extra copy on disk and one of several versions. The
  // on-disk case is the certain one, so it owns the row's single badge.
  const familyByEntityID = useMemo<Record<string, ModFamilyBadge>>(() => {
    const lookup: Record<string, ModFamilyBadge> = {}
    for (const family of props.families) {
      const members = family.members ?? []
      const badge: ModFamilyBadge = {
        count: members.length,
        kind: family.confidence === 'identical' ? 'files' : family.kind,
        familyId: family.id,
      }
      for (const member of members) {
        const existing = lookup[member.entityId]
        if (existing && existing.kind === 'files' && badge.kind !== 'files') continue
        lookup[member.entityId] = badge
      }
    }
    return lookup
  }, [props.families])

  const openDuplicates = (familyID = '') => {
    setDuplicateFocusFamilyID(familyID)
    setDuplicatesOpen(true)
  }

  const pageActions: PageActionSpec[] = [{
    key: 'add-mod',
    label: 'Add mod…',
    icon: 'plus',
    role: 'primary',
    disabled: props.scanning,
    onClick: () => setImportOpen(true),
    title: 'Browse ZIP mods and copy them into your library',
  }]
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
          role: 'secondary',
          className: 'library-scan-action',
          onClick: props.onScan,
          title: 'Scans configured locations for new or updated mods',
        },
  )
  if (props.families.length > 0) {
    pageActions.push({
      key: 'review-duplicates',
      label: `Review duplicates (${props.families.length.toLocaleString()})`,
      icon: 'copy',
      role: 'secondary',
      onClick: () => openDuplicates(),
      title: 'Review duplicate and alternative-version mod families',
    })
  }
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
  const scopeLabel = props.scope === 'archived' ? 'archived mods' : 'mods'
  const emptyFilterTitle = appliedFilters.length > 0
    ? `No matching ${scopeLabel} for these filters: ${appliedFilters.join(', ')}.`
    : props.scope === 'archived'
      ? 'No archived mods. Mods you archive from the active view appear here.'
      : 'No mods found.'
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

  return <Page title="Mod Library" className={`library-view${props.scope === 'archived' ? ' library-view--archived' : ''}`} ariaLabel="Mod library" actions={pageActions}>
    {props.scan && (props.scanning || Boolean(props.scan.error)) && <div className={`scan-strip ${props.scan.error && !props.scanning ? 'scan-strip--error' : ''}`}><div className="scan-strip__pulse"><Icon name={props.scan.error && !props.scanning ? 'error' : 'scan'} size={15}/></div><strong>{props.scan.error && !props.scanning ? 'Scan stopped' : props.scan.phase === 'discovering' ? 'Discovering' : 'Analyzing'}</strong><span title={props.scan.path}>{props.scan.error || props.scan.path || 'Finalizing index'}</span><div className="scan-strip__metrics"><span>{props.scan.discovered} found</span><span>{props.scan.analyzed} processed</span>{props.scan.cached > 0 && <span>{props.scan.cached} cached</span>}{props.scan.failed > 0 && <span>{props.scan.failed} failed</span>}</div></div>}
    <div className="page-toolbar">
      <LibrarySearch value={props.query} loading={props.loading} items={props.catalogItems} collections={props.collections} tags={props.tags} onChange={props.onQueryChange}/>
      <CollectionMenu collections={props.collections} value={props.collectionID} total={props.catalogItems.length} onChange={props.onCollectionChange} onManageCollections={props.onManageCollections}/>
      <div className="segmented" role="group" aria-label="Library scope">
        <button type="button" aria-pressed={props.scope === 'active'} className={props.scope === 'active' ? 'is-active' : ''} title="Active mods" onClick={() => props.onScopeChange('active')}><Icon name="library" size={15}/><span>Active</span></button>
        <button type="button" aria-pressed={props.scope === 'archived'} className={props.scope === 'archived' ? 'is-active' : ''} title="Archived mods" onClick={() => props.onScopeChange('archived')}><Icon name="archive" size={15}/><span>Archived</span></button>
      </div>
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
      {viewMode === 'table' && <div className="segmented segmented--compact" role="group" aria-label="Grouping">
        <button
          type="button"
          aria-pressed={grouped}
          className={grouped ? 'is-active' : ''}
          title={grouped ? 'Turn off grouping' : 'Group mods by tag'}
          onClick={() => toggleGrouped(!grouped)}
        ><Icon name="folder" size={14}/><span>Grouped</span></button>
        {grouped && <button
          type="button"
          title="Choose which tags are groups"
          onClick={() => setManageGroupsOpen(true)}
        ><Icon name="settings" size={14}/></button>}
      </div>}
      {selectedEntityIDs.size > 0 && <div className="library-selection" role="group" aria-label="Selected mods">
        <span className="library-selection__count" role="status" aria-live="polite" aria-label={selectedCountLabel}>{selectedChipLabel}</span>
        {props.scope === 'archived' ? (
          <Button
            type="button"
            icon="refresh"
            className="library-selection__add"
            title="Restore the selected mods to the active library"
            aria-label={`Restore ${selectedCountLabel}`}
            onClick={() => void archiveOrRestore([...selectedEntityIDs])}
          >Restore</Button>
        ) : (
          <Button
            type="button"
            icon="folderPlus"
            className="library-selection__add"
            title="Add the selected mods to a collection"
            aria-label={`Add ${selectedCountLabel} to collection`}
            onClick={() => openAddDialog([...selectedEntityIDs])}
          >Add to collection</Button>
        )}
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
          familyByEntityID={familyByEntityID}
          onReviewFamily={openDuplicates}
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
            selectedIDs: selectedEntityIDs,
            onSelectionChange: setSelectedEntityIDs,
            selectAllLabel: 'Select all matching mods',
            onActivate: props.onSelect,
            onContextMenu: openContextMenu,
          }}
          loading={grouped ? groupLoading : props.loading}
          loadingLabel="Filtering mods"
          familyByEntityID={familyByEntityID}
          onReviewFamily={openDuplicates}
          emptyTitle={emptyFilterTitle}
          resetKey={`${props.query}\u0000${props.collectionID}`}
          groupRows={grouped ? (groupData?.rows ?? undefined) : undefined}
          groupTotalRows={grouped ? (groupData?.totalRows ?? 0) : undefined}
          groupDistinctMods={grouped ? (groupData?.distinctMods ?? 0) : undefined}
          groupPage={grouped ? groupPage : undefined}
          onGroupPageChange={grouped ? handleGroupPageChange : undefined}
          onToggleGroupCollapsed={grouped ? handleToggleGroupCollapsed : undefined}
          hasActiveSearch={grouped ? Boolean(props.query.trim()) : false}
          onPageSizeChange={grouped ? handlePageSizeChange : undefined}
        />
      )}
    </div>
    {importOpen && <AddModDialog
      onClose={() => setImportOpen(false)}
      onImported={async (result) => {
        setSelectedEntityIDs(new Set((result.items ?? []).map(item => item.entityId)))
        await props.onImported()
        const messages: string[] = []
        if (result.importedCount > 0) messages.push(`Added ${result.importedCount.toLocaleString()} mod${result.importedCount === 1 ? '' : 's'} to your library`)
        if (result.existingCount > 0) messages.push(`${result.existingCount.toLocaleString()} already in your library`)
        props.onNotify(messages.join(' · '), 'success')
      }}
    />}
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
      title={`Delete ${removalMods.length === 1 ? 'this archive' : `${removalMods.length.toLocaleString()} archives`}?`}
      onClose={() => { if (!removalBusy) setRemoval(null) }}
      footer={<>
        <Button type="button" onClick={() => setRemoval(null)} disabled={removalBusy}>Cancel</Button>
        <Button type="button" tone="danger" disabled={removalBusy} onClick={() => void confirmRemoval()}>
          {removalWorkspaces.length > 0
            ? `Delete ${removalMods.length === 1 ? 'archive' : `${removalMods.length.toLocaleString()} archives`} + ${removalWorkspaces.length === 1 ? 'project' : `${removalWorkspaces.length.toLocaleString()} projects`}`
            : 'Delete to Recycle Bin'}
        </Button>
      </>}
    >
      <p className="library-removal__copy">
        The archives go to the Recycle Bin, so you can restore them from Windows. The mods also leave the library.
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
      {removalWorkspaces.length > 0 && <p className="library-removal__warning" role="alert">
        <Icon name="warning" size={14} />
        <span>
          Also deletes {removalWorkspaces.length === 1 ? 'the ModMaker project' : `${removalWorkspaces.length} ModMaker projects`}:{' '}
          {removalWorkspaces.join(', ')}.
          Project files go to the Recycle Bin; editor drafts and Virgil sessions are lost.
        </span>
      </p>}
      {removal.impact.archiveCount > 0 && <p className="library-removal__copy">
        {formatBytes(removal.impact.archiveBytes)} across {removal.impact.archiveCount.toLocaleString()} file{removal.impact.archiveCount === 1 ? '' : 's'}.
      </p>}
      {removalError && <p className="collection-add__error" role="alert">{removalError}</p>}
    </CollectionDialog>}
    {groupCreateOpen && selectedEntityIDs.size > 0 && <CollectionDialog
      title="New group from selection"
      onClose={() => { if (!groupCreateBusy) setGroupCreateOpen(false) }}
      footer={<><Button type="button" onClick={() => setGroupCreateOpen(false)} disabled={groupCreateBusy}>Cancel</Button><Button type="button" tone="primary" disabled={groupCreateBusy || !groupNameDraft.trim()} onClick={() => {
        const name = groupNameDraft.trim()
        if (!name) return
        setGroupCreateBusy(true)
        setGroupCreateError('')
        void createGroupFromSelection(name, [...selectedEntityIDs]).then(() => {
          setGroupCreateOpen(false)
          props.onNotify(`Created group "${name}"`, 'success')
        }).catch(error => {
          setGroupCreateError(errorMessage(error))
        }).finally(() => setGroupCreateBusy(false))
      }}>Create</Button></>}
    >
      <form className="collection-add__field" onSubmit={e => { e.preventDefault() }}>
        <span>Group name</span>
        <input autoFocus value={groupNameDraft} onChange={event => { setGroupNameDraft(event.target.value); setGroupCreateError('') }} disabled={groupCreateBusy} aria-label="Group name" />
      </form>
      <p style={{ margin: '8px 0 0', color: 'var(--text-3)', fontSize: 'inherit' }}>
        {selectedEntityIDs.size.toLocaleString()} {selectedEntityIDs.size === 1 ? 'mod' : 'mods'} will be added to the new group.
      </p>
      {groupCreateError && <p className="collection-add__error" role="alert">{groupCreateError}</p>}
    </CollectionDialog>}
    {groupAddOpen && selectedEntityIDs.size > 0 && groupedTags.length > 0 && <CollectionDialog
      title={`Add ${selectedEntityIDs.size === 1 ? 'mod' : `${selectedEntityIDs.size.toLocaleString()} mods`} to group`}
      onClose={() => setGroupAddOpen(false)}
      footer={<><Button type="button" onClick={() => setGroupAddOpen(false)}>Cancel</Button><Button type="button" tone="primary" disabled={!groupAddTarget} onClick={() => {
        setGroupAddOpen(false)
        void addModsToGroup(groupAddTarget, [...selectedEntityIDs]).then(() => {
          const tag = groupedTags.find(t => t.id === groupAddTarget)
          props.onNotify(`Added to ${tag?.name ?? 'group'}`, 'success')
        })
      }}>Add</Button></>}
    >
      <label className="collection-add__field">
        <span>Group</span>
        <select value={groupAddTarget} onChange={e => setGroupAddTarget(e.target.value)}>
          {groupedTags.map(tag => <option key={tag.id} value={tag.id}>{tag.name}</option>)}
        </select>
      </label>
    </CollectionDialog>}
    {groupRemoveOpen && selectedEntityIDs.size > 0 && groupedTags.length > 0 && <CollectionDialog
      title={`Remove ${selectedEntityIDs.size === 1 ? 'mod' : `${selectedEntityIDs.size.toLocaleString()} mods`} from group`}
      onClose={() => setGroupRemoveOpen(false)}
      footer={<><Button type="button" onClick={() => setGroupRemoveOpen(false)}>Cancel</Button><Button type="button" tone="primary" disabled={!groupRemoveTarget} onClick={() => {
        setGroupRemoveOpen(false)
        void removeModsFromGroup(groupRemoveTarget, [...selectedEntityIDs]).then(() => {
          const tag = groupedTags.find(t => t.id === groupRemoveTarget)
          props.onNotify(`Removed from ${tag?.name ?? 'group'}`, 'success')
        })
      }}>Remove</Button></>}
    >
      <label className="collection-add__field">
        <span>Group</span>
        <select value={groupRemoveTarget} onChange={e => setGroupRemoveTarget(e.target.value)}>
          {groupedTags.map(tag => <option key={tag.id} value={tag.id}>{tag.name}</option>)}
        </select>
      </label>
    </CollectionDialog>}
    {manageGroupsOpen && <CollectionDialog
      title="Manage groups"
      onClose={() => setManageGroupsOpen(false)}
      footer={<Button type="button" onClick={() => setManageGroupsOpen(false)}>Done</Button>}
    >
      <p style={{ margin: '0 0 10px', color: 'var(--text-3)', fontSize: 'inherit' }}>
        Toggle which tags appear as groups. Scanner tags are marked &mdash; promoting one with many mods creates a large group.
      </p>
      <div style={{ maxHeight: 320, overflow: 'auto' }}>
        {(props.tags ?? []).filter(t => t.modCount > 0).sort((a, b) => a.name.localeCompare(b.name)).map(tag => (
          <label key={tag.id} style={{ display: 'flex', flexDirection: 'row', alignItems: 'center', gap: 8, padding: '5px 0', borderBottom: '1px solid var(--line)', cursor: 'pointer' }}>
            <input
              type="checkbox"
              checked={tag.grouped}
              disabled={promotionBusy === tag.id}
              onChange={() => void toggleTagGrouped(tag.id, !tag.grouped)}
              style={{ accentColor: 'var(--orange)' }}
            />
            <span style={{ flex: 1, minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
              {tag.name}
            </span>
            <small style={{ color: 'var(--text-3)', whiteSpace: 'nowrap', fontSize: 10 }}>
              {tag.modCount.toLocaleString()} {tag.modCount === 1 ? 'mod' : 'mods'}
              {tag.origin === 'scanner' ? ' · scanner' : ''}
            </small>
          </label>
        ))}
      </div>
    </CollectionDialog>}
    {duplicatesOpen && <DuplicatesDialog
      families={props.families}
      focusFamilyID={duplicateFocusFamilyID || undefined}
      onClose={() => {
        setDuplicatesOpen(false)
        setDuplicateFocusFamilyID('')
      }}
      onRefresh={props.onRefreshFamilies}
      onFamiliesChange={props.onFamiliesChange}
      onNotify={props.onNotify}
    />}
  </Page>
}

