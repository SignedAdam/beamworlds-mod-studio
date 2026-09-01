import { useEffect, useMemo, useState } from 'react'
import type { FileSnapshot } from '../bindings/github.com/SignedAdam/beamworlds-modkit/models.js'
import { Icon } from './icons'
import { formatBytes } from './ui'

export interface TreeSelection {
  path: string
  kind: 'file' | 'directory'
}

interface FileTreeProps {
  files: FileSnapshot[]
  directories: string[]
  query: string
  selected: TreeSelection | null
  onSelectFile: (path: string) => void
  onSelectDirectory: (path: string) => void
  onMove: (oldPath: string, newPath: string) => void
}

interface TreeNode {
  name: string
  path: string
  kind: 'file' | 'directory'
  size: number
  children: TreeNode[]
}

export function FileTree({ files, directories, query, selected, onSelectFile, onSelectDirectory, onMove }: FileTreeProps) {
  const roots = useMemo(() => buildTree(files, directories), [files, directories])
  const normalizedQuery = query.trim().toLowerCase()
  const visible = useMemo(() => normalizedQuery ? filterTree(roots, normalizedQuery) : roots, [roots, normalizedQuery])
  const [expanded, setExpanded] = useState<Set<string>>(() => new Set(directories.slice(0, 32)))

  useEffect(() => {
    setExpanded(current => {
      const next = new Set(current)
      for (const directory of directories.slice(0, 32)) next.add(directory)
      return next
    })
  }, [directories.join('\n')])

  const toggle = (path: string) => setExpanded(current => {
    const next = new Set(current)
    if (next.has(path)) next.delete(path)
    else next.add(path)
    return next
  })

  const dropInto = (event: React.DragEvent, directory: string) => {
    event.preventDefault()
    event.stopPropagation()
    const encoded = event.dataTransfer.getData('application/x-beamworlds-path')
    if (!encoded) return
    try {
      const source = JSON.parse(encoded) as TreeSelection
      const name = source.path.split('/').pop() ?? source.path
      const destination = directory ? `${directory}/${name}` : name
      if (source.path === destination || (source.kind === 'directory' && destination.startsWith(`${source.path}/`))) return
      onMove(source.path, destination)
    } catch {
      return
    }
  }

  return <div className="project-tree" onDragOver={event => event.preventDefault()} onDrop={event => dropInto(event, '')}>
    {visible.length === 0 ? <p className="project-tree__empty">No matching project paths.</p> : visible.map(node => <TreeRow key={`${node.kind}-${node.path}`} node={node} depth={0} expanded={expanded} selected={selected} queryActive={Boolean(normalizedQuery)} onToggle={toggle} onSelectFile={onSelectFile} onSelectDirectory={onSelectDirectory} onDrop={dropInto}/>) }
  </div>
}

function TreeRow({ node, depth, expanded, selected, queryActive, onToggle, onSelectFile, onSelectDirectory, onDrop }: {
  node: TreeNode
  depth: number
  expanded: Set<string>
  selected: TreeSelection | null
  queryActive: boolean
  onToggle: (path: string) => void
  onSelectFile: (path: string) => void
  onSelectDirectory: (path: string) => void
  onDrop: (event: React.DragEvent, directory: string) => void
}) {
  const open = queryActive || expanded.has(node.path)
  const active = selected?.path === node.path && selected.kind === node.kind
  const select = () => {
    if (node.kind === 'directory') onSelectDirectory(node.path)
    else onSelectFile(node.path)
  }
  return <>
    <div
      className={`project-tree__row ${active ? 'is-active' : ''}`}
      style={{ paddingLeft: 8 + depth * 15 }}
      draggable
      onDragStart={event => {
        event.stopPropagation()
        event.dataTransfer.effectAllowed = 'move'
        event.dataTransfer.setData('application/x-beamworlds-path', JSON.stringify({ path: node.path, kind: node.kind }))
      }}
      onDragOver={event => { if (node.kind === 'directory') event.preventDefault() }}
      onDrop={event => { if (node.kind === 'directory') onDrop(event, node.path) }}
    >
      {node.kind === 'directory' ? <button className="tree-caret" onClick={() => onToggle(node.path)} aria-label={`${open ? 'Collapse' : 'Expand'} ${node.path}`}>{open ? '⌄' : '›'}</button> : <span className="tree-caret"/>}
      <button className="tree-node" onClick={select} onDoubleClick={() => { if (node.kind === 'directory') onToggle(node.path) }} title={node.path}>
        <Icon name={node.kind === 'directory' ? 'folder' : 'files'} size={13}/><span>{node.name}</span>{node.kind === 'file' && <small>{formatBytes(node.size)}</small>}
      </button>
    </div>
    {node.kind === 'directory' && open && node.children.map(child => <TreeRow key={`${child.kind}-${child.path}`} node={child} depth={depth + 1} expanded={expanded} selected={selected} queryActive={queryActive} onToggle={onToggle} onSelectFile={onSelectFile} onSelectDirectory={onSelectDirectory} onDrop={onDrop}/>) }
  </>
}

function buildTree(files: FileSnapshot[], directories: string[]): TreeNode[] {
  const roots: TreeNode[] = []
  const directoryMap = new Map<string, TreeNode>()
  const ensureDirectory = (directoryPath: string) => {
    const existing = directoryMap.get(directoryPath)
    if (existing) return existing
    const parts = directoryPath.split('/').filter(Boolean)
    const name = parts.pop() ?? directoryPath
    const parentPath = parts.join('/')
    const node: TreeNode = { name, path: directoryPath, kind: 'directory', size: 0, children: [] }
    directoryMap.set(directoryPath, node)
    if (parentPath) ensureDirectory(parentPath).children.push(node)
    else roots.push(node)
    return node
  }
  for (const directory of [...directories].sort()) ensureDirectory(directory)
  for (const file of files) {
    const parts = file.path.split('/')
    const name = parts.pop() ?? file.path
    const parentPath = parts.join('/')
    const node: TreeNode = { name, path: file.path, kind: 'file', size: file.sizeBytes, children: [] }
    if (parentPath) ensureDirectory(parentPath).children.push(node)
    else roots.push(node)
  }
  const sortNodes = (nodes: TreeNode[]) => {
    nodes.sort((left, right) => left.kind === right.kind ? left.name.localeCompare(right.name, undefined, { numeric: true, sensitivity: 'base' }) : left.kind === 'directory' ? -1 : 1)
    for (const node of nodes) sortNodes(node.children)
  }
  sortNodes(roots)
  return roots
}

function filterTree(nodes: TreeNode[], query: string): TreeNode[] {
  const result: TreeNode[] = []
  for (const node of nodes) {
    const children = filterTree(node.children, query)
    if (node.path.toLowerCase().includes(query) || children.length) result.push({ ...node, children })
  }
  return result
}
