import { useMemo } from 'react'
import type { LibraryItem, ModCollection } from '../bindings/github.com/SignedAdam/beamng-mod-studio/models.js'
import { Icon } from './icons'
import './SelectionCollections.css'

interface CollectionChip {
  id: string
  name: string
  count: number
  entityIDs: Set<string>
}

export interface SelectionCollectionsProps {
  selectedEntityIDs: Set<string>
  catalogItems: LibraryItem[]
  collections: ModCollection[]
  onSelectionChange: (ids: Set<string>) => void
  onCollectionChange: (id: string) => void
}

export function SelectionCollections({
  selectedEntityIDs,
  catalogItems,
  collections,
  onSelectionChange,
  onCollectionChange,
}: SelectionCollectionsProps) {
  const chips = useMemo(() => {
    if (selectedEntityIDs.size === 0) return []

    const nameMap = new Map(collections.map(c => [c.id, c.name]))
    const byCollection = new Map<string, Set<string>>()
    const unfiled = new Set<string>()

    const index = new Map<string, LibraryItem>()
    for (const item of catalogItems) index.set(item.entityId, item)

    for (const entityID of selectedEntityIDs) {
      const item = index.get(entityID)
      if (!item) continue
      const cids = item.collectionIds ?? []
      if (cids.length === 0) {
        unfiled.add(entityID)
        continue
      }
      for (const cid of cids) {
        let set = byCollection.get(cid)
        if (!set) { set = new Set(); byCollection.set(cid, set) }
        set.add(entityID)
      }
    }

    const result: CollectionChip[] = []
    for (const [cid, members] of byCollection) {
      const name = nameMap.get(cid)
      if (!name) continue
      result.push({ id: cid, name, count: members.size, entityIDs: members })
    }
    result.sort((a, b) => b.count - a.count)

    if (unfiled.size > 0) {
      result.push({ id: 'unfiled', name: 'No collection', count: unfiled.size, entityIDs: unfiled })
    }

    return result
  }, [selectedEntityIDs, catalogItems, collections])

  if (chips.length === 0) return null

  const addAll = (chip: CollectionChip) => {
    const next = new Set(selectedEntityIDs)
    if (chip.id === 'unfiled') {
      for (const item of catalogItems) {
        if ((item.collectionIds ?? []).length === 0) next.add(item.entityId)
      }
    } else {
      for (const item of catalogItems) {
        if ((item.collectionIds ?? []).includes(chip.id)) next.add(item.entityId)
      }
    }
    onSelectionChange(next)
  }

  const removeAll = (chip: CollectionChip) => {
    const next = new Set(selectedEntityIDs)
    for (const id of chip.entityIDs) next.delete(id)
    onSelectionChange(next)
  }

  return (
    <div className="selection-collections" role="region" aria-label="Collections in selection">
      {chips.map(chip => (
        <div key={chip.id} className="sc-chip" role="group" aria-label={`${chip.name}: ${chip.count.toLocaleString()} selected`}>
          <button
            type="button"
            className="sc-chip__label"
            title={`Show only ${chip.name}`}
            onClick={() => onCollectionChange(chip.id)}
          >
            <span className="sc-chip__name">{chip.name}</span>
            <span className="sc-chip__count">{chip.count.toLocaleString()}</span>
          </button>
          <button
            type="button"
            className="sc-chip__action"
            title={`Select all from ${chip.name}`}
            aria-label={`Add all ${chip.name} mods to selection`}
            onClick={() => addAll(chip)}
          >
            <Icon name="plus" size={12}/>
          </button>
          <button
            type="button"
            className="sc-chip__action sc-chip__action--remove"
            title={`Deselect ${chip.name}`}
            aria-label={`Remove ${chip.name} mods from selection`}
            onClick={() => removeAll(chip)}
          >
            <Icon name="close" size={12}/>
          </button>
        </div>
      ))}
    </div>
  )
}
