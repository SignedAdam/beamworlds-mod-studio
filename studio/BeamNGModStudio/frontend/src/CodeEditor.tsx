import { useEffect, useRef } from 'react'
import { basicSetup } from 'codemirror'
import { Annotation, type EditorState, type Extension, type Range } from '@codemirror/state'
import { syntaxHighlighting, HighlightStyle, syntaxTree } from '@codemirror/language'
import { EditorView, keymap, Decoration, ViewPlugin, type DecorationSet, type ViewUpdate } from '@codemirror/view'
import { defaultKeymap } from '@codemirror/commands'
import { tags } from '@lezer/highlight'
import type { Tree } from '@lezer/common'
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
import './CodeEditor.css'
const githubHighlightStyle = HighlightStyle.define([
  {
    tag: [tags.comment, tags.lineComment, tags.blockComment, tags.docComment],
    color: 'var(--cm-comment)',
  },
  {
    tag: [tags.string, tags.docString, tags.character, tags.attributeValue, tags.regexp],
    color: 'var(--cm-string)',
  },
  {
    tag: [tags.number, tags.integer, tags.float, tags.bool, tags.null, tags.color, tags.unit],
    color: 'var(--cm-constant)',
  },
  {
    tag: [tags.constant(tags.variableName), tags.constant(tags.name)],
    color: 'var(--cm-constant)',
  },
  {
    tag: [
      tags.keyword,
      tags.self,
      tags.controlKeyword,
      tags.operatorKeyword,
      tags.definitionKeyword,
      tags.moduleKeyword,
      tags.modifier,
    ],
    color: 'var(--cm-keyword)',
  },
  {
    tag: [
      tags.propertyName,
      tags.attributeName,
      tags.standard(tags.variableName),
      tags.standard(tags.propertyName),
      tags.standard(tags.name),
      tags.atom,
    ],
    color: 'var(--cm-property)',
  },
  {
    tag: [
      tags.definition(tags.variableName),
      tags.definition(tags.name),
    ],
    color: 'var(--cm-declaration)',
  },
  {
    tag: [
      tags.definition(tags.propertyName),
      tags.definition(tags.special(tags.propertyName)),
    ],
    color: 'var(--cm-property)',
  },
  {
    tag: [
      tags.function(tags.definition(tags.variableName)),
      tags.function(tags.definition(tags.name)),
      tags.function(tags.definition(tags.propertyName)),
      tags.function(tags.definition(tags.special(tags.propertyName))),
      tags.function(tags.variableName),
      tags.function(tags.propertyName),
      tags.function(tags.name),
    ],
    color: 'var(--cm-function)',
  },
  {
    tag: [
      tags.typeName,
      tags.className,
      tags.tagName,
      tags.namespace,
      tags.definition(tags.typeName),
      tags.definition(tags.className),
    ],
    color: 'var(--cm-type)',
  },
  {
    tag: [tags.heading, tags.heading1, tags.heading2, tags.heading3, tags.heading4, tags.heading5, tags.heading6],
    color: 'var(--cm-property)',
    fontWeight: 'bold',
  },
  {
    tag: [tags.link, tags.url],
    color: 'var(--cm-string)',
  },
  {
    tag: [tags.invalid],
    color: 'var(--cm-invalid)',
    fontStyle: 'italic',
  },
  {
    tag: [tags.strikethrough],
    textDecoration: 'line-through',
  },
])

function selectedTextDecorations(state: EditorState): DecorationSet {
  const ranges = state.selection.ranges.filter(range => range.from < range.to)
  if (!ranges.length) return Decoration.none
  const mark = Decoration.mark({ class: 'cm-github-selected' })
  return Decoration.set(ranges.map(range => mark.range(range.from, range.to)), true)
}

const selectedTextForeground = ViewPlugin.fromClass(class {
  decorations: DecorationSet

  constructor(view: EditorView) {
    this.decorations = selectedTextDecorations(view.state)
  }

  update(update: ViewUpdate) {
    if (update.selectionSet || update.docChanged) {
      this.decorations = selectedTextDecorations(update.state)
    }
  }
}, {
  decorations: value => value.decorations,
})

function semanticDecorations(state: EditorState): DecorationSet {
  const functionMark = Decoration.mark({ class: 'cm-github-function' })
  const declarationMark = Decoration.mark({ class: 'cm-github-declaration' })
  const invalidMark = Decoration.mark({ class: 'cm-github-invalid' })
  const ranges: Range<Decoration>[] = []
  const stack: string[] = []
  const documentLength = state.doc.length
  syntaxTree(state).iterate({
    enter(node) {
      const parent = stack[stack.length - 1]
      if (node.name === 'Property') {
        // Object methods/getters/setters have a direct signature and body;
        // ordinary `name: value` properties do not.
        const propertyName = node.node.getChild('PropertyDefinition')
        const hasMethodBody =
          node.node.getChild('ParamList') !== null &&
          node.node.getChild('Block') !== null
        if (propertyName && hasMethodBody) {
          ranges.push(functionMark.range(propertyName.from, propertyName.to))
        }
      } else if (node.name === 'PropertyDefinition' && parent === 'MethodDeclaration') {
        ranges.push(functionMark.range(node.from, node.to))
      } else if (node.name === 'VariableDefinition' && parent === 'FunctionExpression') {
        ranges.push(functionMark.range(node.from, node.to))
      } else if (node.name === 'PropertyName' && parent === 'EnumBody') {
        ranges.push(declarationMark.range(node.from, node.to))
      }
      if (node.type.isError && documentLength > 0) {
        const from = Math.min(Math.max(0, node.from), documentLength - 1)
        const to = Math.min(documentLength, Math.max(from + 1, node.to))
        ranges.push(invalidMark.range(from, to))
      }
      stack.push(node.name)
    },
    leave() {
      stack.pop()
    },
  })
  return ranges.length ? Decoration.set(ranges, true) : Decoration.none
}

const semanticHighlightDecorations = ViewPlugin.fromClass(class {
  decorations: DecorationSet
  tree: Tree

  constructor(view: EditorView) {
    this.tree = syntaxTree(view.state)
    this.decorations = semanticDecorations(view.state)
  }

  update(update: ViewUpdate) {
    const tree = syntaxTree(update.state)
    if (update.docChanged || update.viewportChanged || tree !== this.tree) {
      this.tree = tree
      this.decorations = semanticDecorations(update.state)
    }
  }
}, {
  decorations: value => value.decorations,
})

const externalUpdate = Annotation.define<boolean>()

export interface CodeEditorSelection {
  from: number
  to: number
  requestId: number
}

export interface CodeEditorDiagnostic {
  from: number
  to: number
  severity: Diagnostic['severity']
  message: string
  source?: string
}

export interface CodeEditorSaveSnapshot {
  value: string
  diagnostics: CodeEditorDiagnostic[]
}

interface CodeEditorProps {
  path: string
  value: string
  onChange: (value: string) => void
  onSave: (snapshot?: CodeEditorSaveSnapshot) => void
  onDiagnostics?: (diagnostics: CodeEditorDiagnostic[]) => void
  selection?: CodeEditorSelection | null
}

export function CodeEditor({ path, value, onChange, onSave, onDiagnostics, selection }: CodeEditorProps) {
  const host = useRef<HTMLDivElement>(null)
  const view = useRef<EditorView | null>(null)
  const callbacks = useRef({ onChange, onSave, onDiagnostics })
  callbacks.current = { onChange, onSave, onDiagnostics }

  useEffect(() => {
    if (!host.current) return
    const reportDiagnostics = (editor: EditorView) => {
      const source = diagnosticSourceFor(path)
      const diagnostics = source ? Array.from(source(editor), normalizeDiagnostic) : []
      callbacks.current.onDiagnostics?.(diagnostics)
      return diagnostics
    }
    const extensions: Extension[] = [
      basicSetup,
      languageFor(path),
      syntaxHighlighting(githubHighlightStyle),
      diagnosticsFor(path),
      selectedTextForeground,
      semanticHighlightDecorations,
      keymap.of([
        {
          key: 'Mod-s',
          preventDefault: true,
          run: editor => {
            callbacks.current.onSave({
              value: editor.state.doc.toString(),
              diagnostics: reportDiagnostics(editor),
            })
            return true
          },
        },
        ...defaultKeymap,
      ]),
      EditorView.updateListener.of(update => {
        if (!update.docChanged) return
        const external = update.transactions.some(transaction => transaction.annotation(externalUpdate))
        const value = update.state.doc.toString()
        reportDiagnostics(update.view)
        if (!external) callbacks.current.onChange(value)
      }),
      EditorView.contentAttributes.of({ 'aria-label': `Editing ${path}`, spellcheck: 'false' }),
      EditorView.theme({
        '&': { height: '100%', backgroundColor: 'var(--cm-editor-canvas)', color: 'var(--cm-editor-fg)' },
        '.cm-scroller': { fontFamily: '"Cascadia Code", "SFMono-Regular", Consolas, monospace', fontSize: 'var(--font-caption)', lineHeight: '1.62' },
        '.cm-content': { caretColor: 'var(--cm-caret)', padding: '0 0 80px' },
        '.cm-gutters': { backgroundColor: 'var(--cm-gutter-bg)', color: 'var(--cm-gutter-fg)', border: '0', borderRight: '1px solid var(--cm-gutter-border)' },
        '.cm-activeLine': { backgroundColor: 'var(--cm-current-line-bg)' },
        '.cm-activeLineGutter': { backgroundColor: 'transparent', borderRight: '1px solid var(--cm-current-line-marker)' },
        '.cm-selectionBackground, &.cm-focused .cm-selectionBackground': { backgroundColor: 'var(--cm-selection-bg) !important' },
        '.cm-matchingBracket': { backgroundColor: 'var(--cm-bracket-bg)', border: '1px solid var(--cm-bracket-border)', borderRadius: '2px', color: 'var(--cm-bracket-fg) !important' },
        '.cm-cursor': { borderLeftColor: 'var(--cm-caret)' },
        '.cm-tooltip, .cm-panels': { backgroundColor: 'var(--cm-panel-bg)', color: 'var(--cm-editor-fg)', borderColor: 'var(--cm-gutter-border)' },
      }),
    ]
    view.current = new EditorView({ doc: value, extensions, parent: host.current })
    reportDiagnostics(view.current)
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

  useEffect(() => {
    const editor = view.current
    if (!editor || !selection) return
    const documentLength = editor.state.doc.length
    const from = Math.max(0, Math.min(selection.from, documentLength))
    const to = Math.max(from, Math.min(selection.to, documentLength))
    editor.dispatch({
      selection: { anchor: from, head: to },
      effects: EditorView.scrollIntoView(from, { y: 'center' }),
    })
    editor.focus()
  }, [selection?.from, selection?.to, selection?.requestId])

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

function diagnosticSourceFor(path: string): ((view: EditorView) => readonly Diagnostic[]) | null {
  if (fileExtension(path) === '.json') return jsonParseLinter()
  if (!['.js', '.ts', '.jbeam', '.pc', '.json5', '.html', '.xml', '.css', '.scss', '.md', '.yaml', '.yml', '.lua'].includes(fileExtension(path))) return null
  return view => {
    const diagnostics: Diagnostic[] = []
    syntaxTree(view.state).iterate({
      enter(node) {
        if (!node.type.isError) return
        diagnostics.push({ from: node.from, to: Math.max(node.to, node.from + 1), severity: 'error', message: 'Syntax error' })
      },
    })
    return diagnostics
  }
}

function diagnosticsFor(path: string): Extension {
  const source = diagnosticSourceFor(path)
  return source ? linter(source, { delay: 200 }) : []
}

function normalizeDiagnostic(diagnostic: Diagnostic): CodeEditorDiagnostic {
  return {
    from: diagnostic.from,
    to: diagnostic.to,
    severity: diagnostic.severity,
    message: diagnostic.message,
    source: diagnostic.source,
  }
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
