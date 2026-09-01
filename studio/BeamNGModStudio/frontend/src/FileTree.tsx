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
  showSizes: boolean
  onSelectFile: (path: string) => void
  onMove: (oldPath: string, newPath: string) => void
  onRename: (selection: TreeSelection) => void
  onDelete: (selection: TreeSelection) => void
  onReveal: (selection: TreeSelection) => void
}

interface TreeNode {
  name: string
  path: string
  kind: 'file' | 'directory'
  size: number
  children: TreeNode[]
}

export function FileTree({ files, directories, query, selected, showSizes, onSelectFile, onMove, onRename, onDelete, onReveal }: FileTreeProps) {
  const roots = useMemo(() => buildTree(files, directories), [files, directories])
  const normalizedQuery = query.trim().toLowerCase()
  const visible = useMemo(() => normalizedQuery ? filterTree(roots, normalizedQuery) : roots, [roots, normalizedQuery])
  const [expanded, setExpanded] = useState<Set<string>>(() => new Set(directories.slice(0, 32)))
  const [contextMenu, setContextMenu] = useState<{ selection: TreeSelection; x: number; y: number } | null>(null)

  useEffect(() => {
    setExpanded(current => {
      const next = new Set(current)
      for (const directory of directories.slice(0, 32)) next.add(directory)
      return next
    })
  }, [directories.join('\n')])

  useEffect(() => {
    if (!contextMenu) return
    const close = () => setContextMenu(null)
    const escape = (event: KeyboardEvent) => { if (event.key === 'Escape') close() }
    window.addEventListener('pointerdown', close)
    window.addEventListener('blur', close)
    window.addEventListener('keydown', escape)
    return () => {
      window.removeEventListener('pointerdown', close)
      window.removeEventListener('blur', close)
      window.removeEventListener('keydown', escape)
    }
  }, [contextMenu])

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

  const showContextMenu = (event: React.MouseEvent, node: TreeNode) => {
    event.preventDefault()
    event.stopPropagation()
    setContextMenu({ selection: { path: node.path, kind: node.kind }, x: Math.min(event.clientX, window.innerWidth - 180), y: Math.min(event.clientY, window.innerHeight - 126) })
  }

  const runContextAction = (action: (selection: TreeSelection) => void) => {
    if (!contextMenu) return
    action(contextMenu.selection)
    setContextMenu(null)
  }

  return <div className="project-tree" role="tree" onDragOver={event => event.preventDefault()} onDrop={event => dropInto(event, '')}>
    {visible.length === 0 ? <p className="project-tree__empty">No matching project paths.</p> : visible.map(node => <TreeRow key={`${node.kind}-${node.path}`} node={node} depth={0} expanded={expanded} selected={selected} queryActive={Boolean(normalizedQuery)} showSizes={showSizes} onToggle={toggle} onSelectFile={onSelectFile} onDrop={dropInto} onContextMenu={showContextMenu}/>)}
    {contextMenu && <div className="context-menu" role="menu" style={{ left: contextMenu.x, top: contextMenu.y }} onPointerDown={event => event.stopPropagation()}>
      <button role="menuitem" onClick={() => runContextAction(onRename)}><Icon name="edit" size={15}/>Rename</button>
      <button role="menuitem" onClick={() => runContextAction(onReveal)}><Icon name="folder" size={15}/>Reveal in file manager</button>
      <span/>
      <button className="context-menu__danger" role="menuitem" onClick={() => runContextAction(onDelete)}><Icon name="trash" size={15}/>Delete</button>
    </div>}
  </div>
}

function TreeRow({ node, depth, expanded, selected, queryActive, showSizes, onToggle, onSelectFile, onDrop, onContextMenu }: {
  node: TreeNode
  depth: number
  expanded: Set<string>
  selected: TreeSelection | null
  queryActive: boolean
  showSizes: boolean
  onToggle: (path: string) => void
  onSelectFile: (path: string) => void
  onDrop: (event: React.DragEvent, directory: string) => void
  onContextMenu: (event: React.MouseEvent, node: TreeNode) => void
}) {
  const open = queryActive || expanded.has(node.path)
  const active = node.kind === 'file' && selected?.path === node.path
  return <>
    <div
      className={`project-tree__row ${active ? 'is-active' : ''}`}
      style={{ paddingLeft: 8 + depth * 15 }}
      role="treeitem"
      aria-expanded={node.kind === 'directory' ? open : undefined}
      draggable
      onContextMenu={event => onContextMenu(event, node)}
      onDragStart={event => {
        event.stopPropagation()
        event.dataTransfer.effectAllowed = 'move'
        event.dataTransfer.setData('application/x-beamworlds-path', JSON.stringify({ path: node.path, kind: node.kind }))
      }}
      onDragOver={event => { if (node.kind === 'directory') event.preventDefault() }}
      onDrop={event => { if (node.kind === 'directory') onDrop(event, node.path) }}
    >
      {node.kind === 'directory' ? <button className={`tree-caret ${open ? 'is-open' : ''}`} onClick={() => onToggle(node.path)} aria-label={`${open ? 'Collapse' : 'Expand'} ${node.path}`}><Icon name="chevron" size={15}/></button> : <span className="tree-caret"/>}
      <button className="tree-node" onClick={() => node.kind === 'directory' ? onToggle(node.path) : onSelectFile(node.path)} title={node.path}>
        <Icon name={node.kind === 'directory' ? 'folder' : 'files'} size={15}/><span>{node.name}</span>{showSizes && <small>{formatBytes(node.size)}</small>}
      </button>
    </div>
    {node.kind === 'directory' && open && node.children.map(child => <TreeRow key={`${child.kind}-${child.path}`} node={child} depth={depth + 1} expanded={expanded} selected={selected} queryActive={queryActive} showSizes={showSizes} onToggle={onToggle} onSelectFile={onSelectFile} onDrop={onDrop} onContextMenu={onContextMenu}/>)}
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
  const finalize = (nodes: TreeNode[]): number => {
    nodes.sort((left, right) => left.kind === right.kind ? left.name.localeCompare(right.name, undefined, { numeric: true, sensitivity: 'base' }) : left.kind === 'directory' ? -1 : 1)
    let total = 0
    for (const node of nodes) {
      if (node.kind === 'directory') node.size = finalize(node.children)
      total += node.size
    }
    return total
  }
  finalize(roots)
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
