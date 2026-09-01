import { useEffect, useRef } from 'react'
import { basicSetup } from 'codemirror'
import { Annotation, type Extension } from '@codemirror/state'
import { EditorView, keymap } from '@codemirror/view'
import { defaultKeymap } from '@codemirror/commands'
import { syntaxTree } from '@codemirror/language'
import { linter, type Diagnostic } from '@codemirror/lint'
import { javascript } from '@codemirror/lang-javascript'
import { json, jsonParseLinter } from '@codemirror/lang-json'
import { html } from '@codemirror/lang-html'
import { css } from '@codemirror/lang-css'
import { markdown } from '@codemirror/lang-markdown'
import { xml } from '@codemirror/lang-xml'
import { yaml } from '@codemirror/lang-yaml'
import { StreamLanguage } from '@codemirror/language'
import { lua } from '@codemirror/legacy-modes/mode/lua'
import * as prettier from 'prettier/standalone'
import * as babelPlugin from 'prettier/plugins/babel'
import * as estreePlugin from 'prettier/plugins/estree'
import * as typescriptPlugin from 'prettier/plugins/typescript'
import * as htmlPlugin from 'prettier/plugins/html'
import * as postcssPlugin from 'prettier/plugins/postcss'
import * as markdownPlugin from 'prettier/plugins/markdown'

import * as yamlPlugin from 'prettier/plugins/yaml'
const externalUpdate = Annotation.define<boolean>()

interface CodeEditorProps {
  path: string
  value: string
  onChange: (value: string) => void
  onSave: () => void
}

export function CodeEditor({ path, value, onChange, onSave }: CodeEditorProps) {
  const host = useRef<HTMLDivElement>(null)
  const view = useRef<EditorView | null>(null)
  const callbacks = useRef({ onChange, onSave })
  callbacks.current = { onChange, onSave }

  useEffect(() => {
    if (!host.current) return
    const extensions: Extension[] = [
      basicSetup,
      languageFor(path),
      diagnosticsFor(path),
      keymap.of([
        { key: 'Mod-s', preventDefault: true, run: () => { callbacks.current.onSave(); return true } },
        ...defaultKeymap,
      ]),
      EditorView.updateListener.of(update => {
        if (update.docChanged && !update.transactions.some(transaction => transaction.annotation(externalUpdate))) {
          callbacks.current.onChange(update.state.doc.toString())
        }
      }),
      EditorView.contentAttributes.of({ 'aria-label': `Editing ${path}`, spellcheck: 'false' }),
      EditorView.theme({
        '&': { height: '100%', backgroundColor: 'transparent', color: 'var(--text)' },
        '.cm-scroller': { fontFamily: '"Cascadia Code", "SFMono-Regular", Consolas, monospace', fontSize: '12px', lineHeight: '1.62' },
        '.cm-content': { caretColor: 'var(--accent)', padding: '12px 0 80px' },
        '.cm-gutters': { backgroundColor: 'color-mix(in srgb, var(--panel-2) 82%, transparent)', color: 'var(--muted)', border: '0', borderRight: '1px solid var(--line)' },
        '.cm-activeLine, .cm-activeLineGutter': { backgroundColor: 'color-mix(in srgb, var(--accent) 8%, transparent)' },
        '.cm-selectionBackground, &.cm-focused .cm-selectionBackground': { backgroundColor: 'color-mix(in srgb, var(--accent) 28%, transparent) !important' },
        '.cm-cursor': { borderLeftColor: 'var(--accent)' },
        '.cm-tooltip, .cm-panels': { backgroundColor: 'var(--panel)', color: 'var(--text)', borderColor: 'var(--line)' },
      }),
    ]
    view.current = new EditorView({ doc: value, extensions, parent: host.current })
    return () => {
      view.current?.destroy()
      view.current = null
    }
  }, [path])

  useEffect(() => {
    const editor = view.current
    if (!editor) return
    const current = editor.state.doc.toString()
    if (current === value) return
    editor.dispatch({
      changes: { from: 0, to: current.length, insert: value },
      annotations: externalUpdate.of(true),
    })
  }, [value])

  return <div className="code-mirror-host" ref={host}/>
}

function languageFor(path: string): Extension {
  const extension = fileExtension(path)
  if (extension === '.ts') return javascript({ typescript: true })
  if (extension === '.js') return javascript()
  if (extension === '.json') return json()
  if (extension === '.jbeam' || extension === '.pc' || extension === '.json5') return javascript()
  if (extension === '.html') return html()
  if (extension === '.xml') return xml()
  if (extension === '.css' || extension === '.scss') return css()
  if (extension === '.md') return markdown()
  if (extension === '.yaml' || extension === '.yml') return yaml()
  if (extension === '.lua') return StreamLanguage.define(lua)
  return []
}

function diagnosticsFor(path: string): Extension {
  if (fileExtension(path) === '.json') return linter(jsonParseLinter())
  if (!['.js', '.ts', '.html', '.xml', '.css', '.scss', '.md', '.yaml', '.yml'].includes(fileExtension(path))) return []
  return linter(view => {
    const diagnostics: Diagnostic[] = []
    syntaxTree(view.state).iterate({
      enter(node) {
        if (!node.type.isError) return
        diagnostics.push({ from: node.from, to: Math.max(node.to, node.from + 1), severity: 'error', message: 'Syntax error' })
      },
    })
    return diagnostics
  })
}

export function canFormatSource(path: string) {
  return parserFor(path) !== ''
}

export async function formatSource(path: string, source: string): Promise<string> {
  const parser = parserFor(path)
  if (!parser) throw new Error(`Formatting is not available for ${fileExtension(path) || 'this file type'}`)
  return prettier.format(source, {
    parser,
    plugins: [babelPlugin, estreePlugin, typescriptPlugin, htmlPlugin, postcssPlugin, markdownPlugin, yamlPlugin],
    printWidth: 110,
    tabWidth: 2,
    useTabs: true,
    trailingComma: 'all',
  })
}

function parserFor(path: string) {
  switch (fileExtension(path)) {
    case '.js': return 'babel'
    case '.ts': return 'typescript'
    case '.json': return 'json'
    case '.json5':
    case '.jbeam':
    case '.pc': return 'json5'
    case '.html':
    case '.xml': return 'html'
    case '.css': return 'css'
    case '.scss': return 'scss'
    case '.md': return 'markdown'
    case '.yaml':
    case '.yml': return 'yaml'
    default: return ''
  }
}

function fileExtension(path: string) {
  const lower = path.toLowerCase()
  const index = lower.lastIndexOf('.')
  return index < 0 ? '' : lower.slice(index)
}
