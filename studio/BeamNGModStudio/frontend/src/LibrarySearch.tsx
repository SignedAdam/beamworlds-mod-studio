import { useMemo, useRef, useState } from 'react'
import type { LibraryFolder, LibraryItem, ModTag } from '../bindings/github.com/SignedAdam/beamng-mod-studio/models.js'
import { Icon, type IconName } from './icons'
import { Spinner, kindLabel } from './ui'

interface LibrarySearchProps {
  value: string
  loading: boolean
  items: LibraryItem[]
  folders: LibraryFolder[]
  tags: ModTag[]
  onChange: (value: string) => void
}

interface SearchSuggestion {
  key: string
  label: string
  detail: string
  description: string
  icon: IconName
  query: string
  continue?: boolean
}

const scopes: Array<{ scope: string; label: string; description: string; icon: IconName }> = [
  { scope: 'tag', label: 'Tags', description: 'Find mods with a custom tag', icon: 'tag' },
  { scope: 'kind', label: 'Kinds', description: 'Vehicle, map, UI, script, or mixed', icon: 'filter' },
  { scope: 'author', label: 'Authors', description: 'Search declared mod authors', icon: 'user' },
  { scope: 'collection', label: 'Collections', description: 'Search optional single-home groups', icon: 'folder' },
  { scope: 'name', label: 'Names', description: 'Search only mod names', icon: 'archive' },
  { scope: 'path', label: 'Paths', description: 'Search source archive paths', icon: 'link' },
  { scope: 'source', label: 'Source availability', description: 'Available or missing source archives', icon: 'link' },
]

const kinds = ['vehicle', 'map', 'ui', 'script', 'mixed', 'unknown']

export function LibrarySearch({ value, loading, items, folders, tags, onChange }: LibrarySearchProps) {
  const [focused, setFocused] = useState(false)
  const [activeIndex, setActiveIndex] = useState(0)
  const inputRef = useRef<HTMLInputElement>(null)
  const suggestions = useMemo(() => buildSuggestions(value, items, folders, tags), [value, items, folders, tags])
  const open = focused && value.trim().length > 0 && suggestions.length > 0
  const safeActiveIndex = Math.min(activeIndex, Math.max(0, suggestions.length - 1))

  const choose = (suggestion: SearchSuggestion) => {
    onChange(suggestion.query)
    setActiveIndex(0)
    if (suggestion.continue) {
      requestAnimationFrame(() => inputRef.current?.focus())
    } else {
      setFocused(false)
      inputRef.current?.blur()
    }
  }

  return <div className={`library-search ${open ? 'is-open' : ''}`}>
    <label className="search-box" role="combobox" aria-expanded={open} aria-haspopup="listbox" aria-controls="library-search-suggestions">
      <Icon name="search" size={16}/>
      <input
        ref={inputRef}
        value={value}
        onFocus={() => { setFocused(true); setActiveIndex(0) }}
        onBlur={() => window.setTimeout(() => setFocused(false), 80)}
        onChange={event => { onChange(event.target.value); setActiveIndex(0); setFocused(true) }}
        onKeyDown={event => {
          if (event.key === 'ArrowDown' && suggestions.length > 0) {
            event.preventDefault()
            setFocused(true)
            setActiveIndex(index => (index + 1) % suggestions.length)
          } else if (event.key === 'ArrowUp' && suggestions.length > 0) {
            event.preventDefault()
            setFocused(true)
            setActiveIndex(index => (index - 1 + suggestions.length) % suggestions.length)
          } else if (event.key === 'Enter' && open && suggestions[safeActiveIndex]) {
            event.preventDefault()
            choose(suggestions[safeActiveIndex])
          } else if (event.key === 'Escape') {
            setFocused(false)
            inputRef.current?.blur()
          }
        }}
        placeholder="Search mods or type in: for filters"
        aria-label="Search and filter mods"
        aria-autocomplete="list"
        aria-controls="library-search-suggestions"
        aria-activedescendant={open ? `library-search-option-${safeActiveIndex}` : undefined}
      />
      {loading && <Spinner small/>}
      {value && <button type="button" onMouseDown={event => event.preventDefault()} onClick={() => { onChange(''); inputRef.current?.focus() }} aria-label="Clear search"><Icon name="close" size={13}/></button>}
    </label>
    {open && <div className="search-suggestions" id="library-search-suggestions" role="listbox" aria-label="Search suggestions">
      {suggestions.map((suggestion, index) => <button
        type="button"
        id={`library-search-option-${index}`}
        role="option"
        aria-selected={index === safeActiveIndex}
        className={index === safeActiveIndex ? 'is-active' : ''}
        key={suggestion.key}
        onMouseEnter={() => setActiveIndex(index)}
        onMouseDown={event => { event.preventDefault(); choose(suggestion) }}
      >
        <Icon name={suggestion.icon} size={16}/>
        <span><strong>{suggestion.label}</strong><small>{suggestion.description}</small></span>
        <kbd>{suggestion.detail}</kbd>
      </button>)}
      <footer><span><kbd>↑</kbd><kbd>↓</kbd> move</span><span><kbd>Enter</kbd> apply</span><span><kbd>Esc</kbd> close</span></footer>
    </div>}
  </div>
}

function buildSuggestions(value: string, items: LibraryItem[], folders: LibraryFolder[], tags: ModTag[]): SearchSuggestion[] {
  const trimmed = value.trimStart()
  const scopeMatch = trimmed.match(/(?:^|\s)in:([a-z]*)$/i)
  if (scopeMatch) {
    const fragment = scopeMatch[1].toLowerCase()
    return scopes.filter(scope => scope.scope.startsWith(fragment) || scope.label.toLowerCase().startsWith(fragment)).map(scope => ({
      key: `scope-${scope.scope}`,
      label: scope.label,
      detail: `in: ${scope.scope}`,
      description: scope.description,
      icon: scope.icon,
      query: replaceCurrentExpression(value, `in:${scope.scope} `),
      continue: true,
    }))
  }

  const statusMatch = trimmed.match(/(?:^|\s)is:([a-z]*)$/i)
  if (statusMatch) return statusSuggestions(value, statusMatch[1])

  const activeScopeMatch = trimmed.match(/(?:^|\s)in:(tag|kind|author|collection|name|path|source)\s+([^\s"]*|"[^"]*)$/i)
  if (activeScopeMatch) {
    const scope = activeScopeMatch[1].toLowerCase()
    const fragment = activeScopeMatch[2].replace(/^"/, '').toLowerCase()
    return valueSuggestions(scope, fragment, value, items, folders, tags)
  }

  const fragment = trailingFragment(trimmed).toLowerCase()
  if (!fragment) return []
  const suggestions: SearchSuggestion[] = []
  suggestions.push(...statusSuggestions(value, fragment).filter(suggestion => suggestion.label.toLowerCase().includes(fragment) || suggestion.detail.includes(fragment)))
  for (const scope of ['tag', 'kind', 'author', 'collection', 'name', 'source'] as const) {
    suggestions.push(...valueSuggestions(scope, fragment, value, items, folders, tags))
  }
  return dedupeSuggestions(suggestions).slice(0, 10)
}

function statusSuggestions(value: string, fragment: string): SearchSuggestion[] {
  const options = [
    { key: 'safe', label: 'Safe', description: 'Latest scan found no threat signals', icon: 'check' as IconName },
    { key: 'review', label: 'Review needed', description: 'Latest scan found items that need review', icon: 'warning' as IconName },
    { key: 'threat', label: 'Threat found', description: 'Latest scan found a high-risk threat', icon: 'error' as IconName },
    { key: 'broken', label: 'Broken', description: 'Structural errors prevent clean loading', icon: 'error' as IconName },
    { key: 'unscanned', label: 'Not scanned', description: 'No virus scan has completed for this artifact', icon: 'shield' as IconName },
    { key: 'scan_failed', label: 'Scan failed', description: 'The latest virus scan did not complete', icon: 'error' as IconName },
  ]
  const normalized = fragment.toLowerCase()
  return options.filter(option => !normalized || option.key.startsWith(normalized) || option.label.toLowerCase().includes(normalized)).map(option => ({
    key: `status-${option.key}`,
    label: option.label,
    detail: `is: ${option.key}`,
    description: option.description,
    icon: option.icon,
    query: replaceCurrentExpression(value, `is:${option.key}`),
  }))
}

function valueSuggestions(scope: string, fragment: string, value: string, items: LibraryItem[], folders: LibraryFolder[], tags: ModTag[]): SearchSuggestion[] {
  let values: Array<{ value: string; label?: string; description: string; icon: IconName }> = []
  if (scope === 'tag') {
    values = tags.map(tag => ({ value: tag.name, description: `${tag.modCount.toLocaleString()} tagged mod${tag.modCount === 1 ? '' : 's'}`, icon: 'tag' }))
  } else if (scope === 'kind') {
    values = kinds.map(kind => ({ value: kind, label: kindLabel(kind), description: 'Mod kind', icon: kind === 'vehicle' ? 'vehicle' : kind === 'map' ? 'map' : kind === 'mixed' ? 'mixed' : kind === 'unknown' ? 'unknown' : 'code' }))
  } else if (scope === 'author') {
    values = uniqueValues(items.map(item => item.manifest?.author ?? '')).map(author => ({ value: author, description: 'Declared author', icon: 'user' }))
  } else if (scope === 'collection') {
    values = [{ value: 'Unfiled', description: 'Mods without a collection', icon: 'folder' }, ...folders.map(folder => ({ value: folder.name, description: `${folder.modCount.toLocaleString()} mod${folder.modCount === 1 ? '' : 's'}`, icon: 'folder' as IconName }))]
  } else if (scope === 'name') {
    values = uniqueValues(items.map(item => item.displayName)).map(name => ({ value: name, description: 'Mod name', icon: 'archive' }))
  } else if (scope === 'path') {
    values = uniqueValues(items.map(item => item.archivePath)).map(path => ({ value: path, description: 'Source archive path', icon: 'link' }))
  } else if (scope === 'source') {
    values = [
      { value: 'available', label: 'Available source', description: 'Archive exists in a configured mod folder', icon: 'link' },
      { value: 'missing', label: 'Missing source', description: 'Source archive is no longer present', icon: 'unlink' },
    ]
  }
  return values
    .filter(option => !fragment || option.value.toLowerCase().includes(fragment) || option.label?.toLowerCase().includes(fragment))
    .slice(0, scope === 'name' || scope === 'path' ? 5 : 10)
    .map(option => ({
      key: `${scope}-${option.value.toLowerCase()}`,
      label: option.label ?? option.value,
      detail: `in: ${scope}`,
      description: option.description,
      icon: option.icon,
      query: replaceCurrentExpression(value, `in:${scope} ${quoteSearchValue(option.value)}`),
    }))
}

function replaceCurrentExpression(value: string, replacement: string) {
  const expression = value.match(/(?:^|\s)(?:in:[a-z]+(?:\s+(?:"[^"]*|[^\s]*))?|is:[^\s]*|[^\s]*)$/i)
  if (!expression || expression.index === undefined) return replacement
  const leadingSpace = expression[0].startsWith(' ') ? ' ' : ''
  return `${value.slice(0, expression.index)}${leadingSpace}${replacement}`
}

function trailingFragment(value: string) {
  const match = value.match(/(?:^|\s)([^\s]*)$/)
  return match?.[1] ?? value
}

function quoteSearchValue(value: string) {
  return `"${value.replace(/\\/g, '\\\\').replace(/"/g, '\\"')}"`
}

function uniqueValues(values: string[]) {
  const seen = new Set<string>()
  return values.filter(value => {
    const normalized = value.trim().toLowerCase()
    if (!normalized || seen.has(normalized)) return false
    seen.add(normalized)
    return true
  }).sort((left, right) => left.localeCompare(right, undefined, { sensitivity: 'base' }))
}

function dedupeSuggestions(suggestions: SearchSuggestion[]) {
  const seen = new Set<string>()
  return suggestions.filter(suggestion => !seen.has(suggestion.key) && seen.add(suggestion.key))
}
