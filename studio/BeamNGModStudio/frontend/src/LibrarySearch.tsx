import { useMemo, useRef, useState, type CSSProperties } from 'react'
import type { LibraryFolder, LibraryItem, ModTag } from '../bindings/github.com/SignedAdam/beamng-mod-studio/models.js'
import { Icon, type IconName } from './icons'
import { Spinner } from './ui'
import './LibrarySearch.css'

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
  icon?: IconName
  query: string
  color?: string
  continue?: boolean
  keepFocus?: boolean
}

interface OperatorCompletion {
  fragment: string
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
const tagIcons: IconName[] = ['tag', 'vehicle', 'map', 'code', 'files', 'shield', 'user']
const fallbackTagColor = '#7a8791'

const sourceChoices: Array<{ value: string; description: string; icon: IconName }> = [
  { value: 'BeamNG Repository', description: 'Mods ingested from the BeamNG Repository', icon: 'archive' },
  { value: 'User added', description: 'Mods added through a local or manual flow', icon: 'user' },
]

export function LibrarySearch({ value, loading, items, folders, tags, onChange }: LibrarySearchProps) {
  const [focused, setFocused] = useState(false)
  const [activeIndex, setActiveIndex] = useState(0)
  const [dismissedValue, setDismissedValue] = useState<string | null>(null)
  const inputRef = useRef<HTMLInputElement>(null)
  const tagCompletion = trailingOperatorCompletion(value, ['tag', 'tags'])
  const sourceCompletion = trailingOperatorCompletion(value, ['source'])
  const suggestions = useMemo(() => buildSuggestions(value, items, folders, tags), [value, items, folders, tags])
  const hasCompletion = tagCompletion !== null || sourceCompletion !== null
  const open = focused && value !== dismissedValue && (hasCompletion || (value.trim().length > 0 && suggestions.length > 0))
  const hasOptions = open && suggestions.length > 0
  const safeActiveIndex = Math.min(activeIndex, Math.max(0, suggestions.length - 1))
  const emptyMessage = tagCompletion && suggestions.length === 0
    ? 'No matching tags'
    : sourceCompletion && suggestions.length === 0
      ? 'No matching sources'
      : ''

  const editValue = (nextValue: string) => {
    setDismissedValue(null)
    setActiveIndex(0)
    setFocused(true)
    onChange(nextValue)
  }

  const choose = (suggestion: SearchSuggestion) => {
    onChange(suggestion.query)
    setActiveIndex(0)
    setDismissedValue(suggestion.keepFocus ? suggestion.query : null)
    if (suggestion.continue) {
      requestAnimationFrame(() => inputRef.current?.focus())
    } else {
      setFocused(false)
      if (suggestion.keepFocus) requestAnimationFrame(() => inputRef.current?.focus())
      else inputRef.current?.blur()
    }
  }

  return <div className={`library-search${open ? ' is-open' : ''}`}>
    <label className="search-box">
      <Icon name="search" size={16}/>
      <input
        ref={inputRef}
        value={value}
        role="combobox"
        aria-expanded={open}
        aria-haspopup="listbox"
        aria-controls={hasOptions ? 'library-search-suggestions' : undefined}
        onFocus={() => { setFocused(true); setActiveIndex(0) }}
        onBlur={() => window.setTimeout(() => setFocused(false), 80)}
        onChange={event => editValue(event.target.value)}
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
            event.preventDefault()
            setFocused(false)
          }
        }}
        placeholder="Search mods or type in: for filters"
        aria-label="Search and filter mods"
        aria-autocomplete="list"
        aria-activedescendant={hasOptions ? `library-search-option-${safeActiveIndex}` : undefined}
      />
      {loading && <Spinner small/>}
      {value && <button type="button" onMouseDown={event => event.preventDefault()} onClick={() => { editValue(''); inputRef.current?.focus() }} aria-label="Clear search"><Icon name="close" size={13}/></button>}
    </label>
    {open && <div className="library-search__popup">
      {suggestions.length > 0 && <div className="search-suggestions" id="library-search-suggestions" role="listbox" aria-label="Search suggestions">
        {suggestions.map((suggestion, index) => <button
          type="button"
          id={`library-search-option-${index}`}
          role="option"
          aria-selected={index === safeActiveIndex}
          className={`library-search__suggestion${index === safeActiveIndex ? ' is-active' : ''}`}
          style={suggestion.color ? { '--suggestion-color': suggestion.color } as CSSProperties : undefined}
          key={suggestion.key}
          onMouseEnter={() => setActiveIndex(index)}
          onMouseDown={event => { event.preventDefault(); choose(suggestion) }}
        >
          {suggestion.icon
            ? <Icon className="library-search__suggestion-icon" name={suggestion.icon} size={16}/>
            : <span className="library-search__suggestion-icon-empty" aria-hidden="true"/>}
          <span><strong>{suggestion.label}</strong><small>{suggestion.description}</small></span>
          <kbd>{suggestion.detail}</kbd>
        </button>)}
      </div>}
      {emptyMessage && <div className="library-search__empty" role="status" aria-live="polite">{emptyMessage}</div>}
      <footer className="library-search__help" aria-label="Search suggestion keyboard shortcuts">
        {suggestions.length > 0 && <><span><kbd>↑</kbd><kbd>↓</kbd> move</span><span><kbd>Enter</kbd> apply</span></>}
        <span><kbd>Esc</kbd> close</span>
      </footer>
    </div>}
  </div>
}

function buildSuggestions(value: string, items: LibraryItem[], folders: LibraryFolder[], tags: ModTag[]): SearchSuggestion[] {
  const tagCompletion = trailingOperatorCompletion(value, ['tag', 'tags'])
  if (tagCompletion) return tagSuggestions(value, tagCompletion.fragment, tags)

  const sourceCompletion = trailingOperatorCompletion(value, ['source'])
  if (sourceCompletion) return sourceSuggestions(value, sourceCompletion.fragment)

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
  suggestions.push(...sourceSuggestions(value, fragment, false))
  for (const scope of ['tag', 'kind', 'author', 'collection', 'name', 'source'] as const) {
    suggestions.push(...valueSuggestions(scope, fragment, value, items, folders, tags))
  }
  return dedupeSuggestions(suggestions).slice(0, 10)
}

function tagSuggestions(value: string, fragment: string, tags: ModTag[]): SearchSuggestion[] {
  const normalized = fragment.trim().toLocaleLowerCase()
  return tags
    .filter(tag => tag.name.trim() && tag.name.toLocaleLowerCase().includes(normalized))
    .map(tag => ({
      key: `tags-${tag.id || tag.name.toLocaleLowerCase()}`,
      label: tag.name,
      detail: 'tags:',
      description: `${tag.modCount.toLocaleString()} assigned mod${tag.modCount === 1 ? '' : 's'}`,
      icon: configuredTagIcon(tag.icon),
      color: configuredTagColor(tag.color),
      query: replaceCurrentExpression(value, formatTagQuery(tag.name)),
      keepFocus: true,
    }))
}

function sourceSuggestions(value: string, fragment: string, includeClear = true): SearchSuggestion[] {
  const normalized = fragment.trim().toLocaleLowerCase()
  const matches = sourceChoices
    .filter(choice => !normalized || choice.value.toLocaleLowerCase().includes(normalized))
    .map(choice => ({
      key: `source-${choice.value.toLocaleLowerCase().replace(/\s+/g, '-')}`,
      label: choice.value,
      detail: 'source:',
      description: choice.description,
      icon: choice.icon,
      query: replaceCurrentExpression(value, `source:${quoteSearchValue(choice.value)}`),
      keepFocus: true,
    }))
  if (!includeClear) return matches
  matches.push({
    key: 'source-all',
    label: 'All sources',
    detail: 'clear source',
    description: 'Remove the active source filter',
    icon: 'filter',
    query: removeSourceExpression(value),
    keepFocus: true,
  })
  return matches
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
  let values: Array<{ value: string; label?: string; description: string; icon?: IconName; color?: string }> = []
  if (scope === 'tag') {
    values = tags.map(tag => ({ value: tag.name, description: `${tag.modCount.toLocaleString()} tagged mod${tag.modCount === 1 ? '' : 's'}`, icon: configuredTagIcon(tag.icon), color: configuredTagColor(tag.color) }))
  } else if (scope === 'kind') {
    values = kinds.map(kind => ({ value: kind, description: 'Mod kind', icon: kind === 'vehicle' ? 'vehicle' : kind === 'map' ? 'map' : kind === 'mixed' ? 'mixed' : kind === 'unknown' ? 'unknown' : 'code' }))
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
      color: option.color,
      query: replaceCurrentExpression(value, `in:${scope} ${quoteSearchValue(option.value)}`),
    }))
}

function replaceCurrentExpression(value: string, replacement: string) {
  const tokens = searchTokenSpans(value)
  const current = tokens[tokens.length - 1]
  if (!current) return replacement
  return `${value.slice(0, current.start)}${replacement}${value.slice(current.end)}`
}

function trailingFragment(value: string) {
  const tokens = searchTokenSpans(value)
  return tokens[tokens.length - 1]?.text ?? ''
}

function trailingOperatorCompletion(value: string, operators: readonly string[]): OperatorCompletion | null {
  const tokens = searchTokenSpans(value)
  const current = tokens[tokens.length - 1]
  if (!current) return null
  const separator = current.text.indexOf(':')
  if (separator <= 0) return null
  const operator = current.text.slice(0, separator).toLocaleLowerCase()
  if (!operators.includes(operator)) return null
  const raw = current.text.slice(separator + 1)
  const quoted = raw.startsWith('"')
  const closed = quoted && hasClosingQuote(raw)
  return {
    fragment: unescapeSearchValue(quoted ? raw.slice(1, closed ? -1 : undefined) : raw),
  }
}

function searchTokenSpans(value: string) {
  const tokens: Array<{ start: number; end: number; text: string }> = []
  let start = -1
  let quoted = false
  let escaped = false
  const push = (end: number) => {
    if (start >= 0) tokens.push({ start, end, text: value.slice(start, end) })
    start = -1
  }
  for (let index = 0; index < value.length; index += 1) {
    const char = value[index]
    if (start < 0) {
      if (/\s/.test(char)) continue
      start = index
    }
    if (escaped) {
      escaped = false
      continue
    }
    if (char === '\\' && quoted) {
      escaped = true
      continue
    }
    if (char === '"') {
      quoted = !quoted
      continue
    }
    if (!quoted && /\s/.test(char)) push(index)
  }
  push(value.length)
  return tokens
}

function hasClosingQuote(value: string) {
  if (!value.endsWith('"')) return false
  let slashCount = 0
  for (let index = value.length - 2; index >= 0 && value[index] === '\\'; index -= 1) slashCount += 1
  return slashCount % 2 === 0
}

function unescapeSearchValue(value: string) {
  return value.replace(/\\(.)/g, '$1')
}

function formatTagQuery(value: string) {
  return `tags:${/[\s"\\]/.test(value) ? quoteSearchValue(value) : value}`
}

function quoteSearchValue(value: string) {
  return `"${value.replace(/\\/g, '\\\\').replace(/"/g, '\\"')}"`
}

function removeSourceExpression(value: string) {
  const current = searchTokenSpans(value).reverse().find(token => /^source:/i.test(token.text))
  if (!current) return value
  const before = value.slice(0, current.start).trimEnd()
  const after = value.slice(current.end).trimStart()
  if (!before) return after
  if (!after) return before
  return `${before} ${after}`
}

function configuredTagIcon(value: string | undefined): IconName | undefined {
  return tagIcons.includes(value as IconName) ? value as IconName : undefined
}

function configuredTagColor(value: string | undefined) {
  return typeof value === 'string' && /^#[0-9a-f]{6}$/i.test(value) ? value : fallbackTagColor
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
