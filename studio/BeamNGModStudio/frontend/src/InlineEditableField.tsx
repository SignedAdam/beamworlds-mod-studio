import {
  useCallback,
  useEffect,
  useId,
  useRef,
  useState,
  type KeyboardEvent,
  type ReactNode,
} from 'react'
import './InlineEditableField.css'

export type InlineEditableFieldState = 'display' | 'editing' | 'saving' | 'error'

export type InlineEditableFieldErrorMessage =
  | ReactNode
  | ((error: unknown) => ReactNode)

export interface InlineEditableFieldEditorProps<T> {
  /** The current draft value. */
  value: T
  /** Alias for value that makes draft-oriented editors self-documenting. */
  draft: T
  /** Replace the current draft value. */
  onChange: (value: T) => void
  /** Stable id to assign to the editor control. */
  id: string
  /** Id of the visible label for the editor's accessible name. */
  labelId: string
  /** Id for the field-local status/error message, when present. */
  describedBy?: string
  /** Whether the editor should refuse input. */
  disabled: boolean
  /** True on the first render after entering edit mode. */
  autoFocus: boolean
  /** Escape handling for editors that do not bubble keyboard events. */
  onKeyDown: (event: KeyboardEvent<HTMLElement>) => void
}

export interface InlineEditableFieldProps<T> {
  /** Human-readable field name shown above the value and used for the edit name. */
  label: ReactNode
  /** Committed value supplied by the parent. */
  value: T
  /** Display renderer for the committed value. */
  renderDisplay: (value: T) => ReactNode
  /** Editor renderer for the field-local draft. */
  renderEditor: (props: InlineEditableFieldEditorProps<T>) => ReactNode
  /** Persist one field. Rejection leaves the draft in place for retry/cancel. */
  onSave: (value: T) => Promise<void> | void
  /** Called after a local cancel, after the draft has been discarded. */
  onCancel?: () => void
  /** Called only when this field's unsaved draft state changes. */
  onDirtyChange?: (dirty: boolean) => void
  /** Optional parent-level observation of the field-local save failure. */
  onError?: (error: unknown) => void
  /** Compare values when the value is not a primitive or Object.is is insufficient. */
  isEqual?: (left: T, right: T) => boolean
  /** True when the field must not expose editing. */
  readOnly?: boolean
  /** Disable the edit control and editor without removing the field. */
  disabled?: boolean
  /** Lock the field while retaining its stable edit-control slot. */
  locked?: boolean
  /** Allocate the larger stable value area used for textareas and long content. */
  multiline?: boolean
  /** Optional stable id prefix for label/editor/status relationships. */
  id?: string
  /** Optional class name on the field root. */
  className?: string
  /** Explicit accessible name for the compact edit control. */
  editLabel?: string
  /** Label for the primary save action. */
  saveLabel?: string
  /** Label for the cancel action. */
  cancelLabel?: string
  /** Label for a retry after a rejected save. */
  retryLabel?: string
  /** Optional replacement for the default save failure text. */
  errorMessage?: InlineEditableFieldErrorMessage
}

const defaultErrorText = 'Could not save this field. Try again or cancel.'

function errorText(error: unknown, message?: InlineEditableFieldErrorMessage): ReactNode {
  const resolved = typeof message === 'function' ? message(error) : message
  if (resolved !== undefined && resolved !== null && resolved !== '') return resolved
  if (error instanceof Error && error.message.trim()) return error.message
  if (typeof error === 'string' && error.trim()) return error
  return defaultErrorText
}

function focusWithoutScroll(element: HTMLElement | null): void {
  if (!element || element.getAttribute('aria-disabled') === 'true' || (element instanceof HTMLButtonElement && element.disabled)) return
  try {
    element.focus({ preventScroll: true })
  } catch {
    element.focus()
  }
}

function scheduleFocus(callback: () => void): () => void {
  if (typeof window === 'undefined' || typeof window.requestAnimationFrame !== 'function') {
    callback()
    return () => undefined
  }
  const frame = window.requestAnimationFrame(callback)
  return () => window.cancelAnimationFrame(frame)
}

export function InlineEditableField<T>({
  label,
  value,
  renderDisplay,
  renderEditor,
  onSave,
  onCancel,
  onDirtyChange,
  onError,
  isEqual = Object.is,
  readOnly = false,
  disabled = false,
  locked = false,
  multiline = false,
  id,
  className,
  editLabel,
  saveLabel = 'Save',
  cancelLabel = 'Cancel',
  retryLabel = 'Retry',
  errorMessage,
}: InlineEditableFieldProps<T>) {
  const generatedId = useId()
  const baseId = (id ?? `inline-editable-field-${generatedId}`).replace(/[^a-zA-Z0-9_-]/g, '-')
  const labelId = `${baseId}-label`
  const editorId = `${baseId}-editor`
  const messageId = `${baseId}-message`

  const [draft, setDraft] = useState(value)
  const [state, setState] = useState<InlineEditableFieldState>('display')
  const fieldRootRef = useRef<HTMLDivElement>(null)
  const [saveError, setSaveError] = useState<unknown | null>(null)
  const editorHostRef = useRef<HTMLDivElement>(null)
  const editButtonRef = useRef<HTMLButtonElement>(null)
  const saveRequestRef = useRef(0)
  const dirtyRef = useRef(false)
  const returnFocusRef = useRef(false)
  const mountedRef = useRef(true)
  const onDirtyChangeRef = useRef(onDirtyChange)
  const onErrorRef = useRef(onError)

  onDirtyChangeRef.current = onDirtyChange
  onErrorRef.current = onError

  useEffect(() => {
    mountedRef.current = true
    return () => {
      mountedRef.current = false
      if (dirtyRef.current) {
        dirtyRef.current = false
        onDirtyChangeRef.current?.(false)
      }
    }
  }, [])

  useEffect(() => {
    if (state === 'display') setDraft(value)
  }, [state, value])

  useEffect(() => {
    const dirty = state !== 'display' && !isEqual(draft, value)
    if (dirty === dirtyRef.current) return
    dirtyRef.current = dirty
    onDirtyChangeRef.current?.(dirty)
  }, [draft, isEqual, state, value])

  useEffect(() => {
    if (state !== 'editing' && state !== 'error') return
    const cleanup = scheduleFocus(() => {
      const host = editorHostRef.current
      const editor = host ? host.querySelector<HTMLElement>(
        '[autofocus], input:not([disabled]), select:not([disabled]), textarea:not([disabled]), button:not([disabled]), [contenteditable="true"], [tabindex]:not([tabindex="-1"])',
      ) : null
      focusWithoutScroll(editor)
    })
    return cleanup
  }, [state])

  useEffect(() => {
    if (state !== 'display' || !returnFocusRef.current) return
    returnFocusRef.current = false
    const cleanup = scheduleFocus(() => focusWithoutScroll(editButtonRef.current))
    return cleanup
  }, [state])

  const beginEdit = useCallback(() => {
    if (readOnly || disabled || locked || state !== 'display') return
    setDraft(value)
    setSaveError(null)
    setState('editing')
  }, [disabled, locked, readOnly, state, value])

  const cancel = useCallback(() => {
    if (state === 'saving' || state === 'display') return
    setDraft(value)
    setSaveError(null)
    setState('display')
    returnFocusRef.current = !readOnly && !disabled && !locked
    onCancel?.()
  }, [disabled, locked, onCancel, readOnly, state, value])

  const handleEditorKeyDown = useCallback((event: KeyboardEvent<HTMLElement>) => {
    if (event.key !== 'Escape' || event.defaultPrevented || event.nativeEvent.isComposing || state === 'saving') return
    event.preventDefault()
    event.stopPropagation()
    cancel()
  }, [cancel, state])

  const changeDraft = useCallback((next: T) => {
    setDraft(next)
    if (state === 'error') {
      setSaveError(null)
      setState('editing')
    }
  }, [state])

  const save = useCallback(async () => {
    if (readOnly || disabled || locked || state !== 'editing' && state !== 'error') return
    const requestId = ++saveRequestRef.current
    const pending = draft
    focusWithoutScroll(fieldRootRef.current)
    setState('saving')
    setSaveError(null)
    try {
      await onSave(pending)
      if (!mountedRef.current || requestId !== saveRequestRef.current) return
      setState('display')
      setSaveError(null)
      returnFocusRef.current = !readOnly && !disabled && !locked
    } catch (error) {
      if (!mountedRef.current || requestId !== saveRequestRef.current) return
      setSaveError(error)
      setState('error')
      onErrorRef.current?.(error)
    }
  }, [disabled, draft, locked, onSave, readOnly, state])

  const isEditing = state !== 'display'
  const editorDisabled = disabled || locked || state === 'saving'
  const accessibleEditLabel = editLabel?.trim() || (typeof label === 'string' || typeof label === 'number' ? `Edit ${label}` : 'Edit field')
  const actionLabel = accessibleEditLabel.replace(/^Edit\s+/, '')
  const resolvedError = saveError === null ? null : errorText(saveError, errorMessage)
  const rootClassName = [
    'inline-editable-field',
    multiline ? 'inline-editable-field--multiline' : '',
    isEditing ? `inline-editable-field--${state}` : '',
    disabled ? 'inline-editable-field--disabled' : '',
    locked ? 'inline-editable-field--locked' : '',
    resolvedError !== null ? 'inline-editable-field--has-error' : '',
    className ?? '',
  ].filter(Boolean).join(' ')

  return <div ref={fieldRootRef} className={rootClassName} data-state={state} role="group" tabIndex={-1} aria-labelledby={labelId} aria-busy={state === 'saving'}>
    <div className="inline-editable-field__heading">
      <span id={labelId} className="inline-editable-field__label">{label}</span>
      {!readOnly && <button
        ref={editButtonRef}
        type="button"
        className={`inline-editable-field__edit${isEditing ? ' inline-editable-field__edit--suppressed' : ''}`}
        aria-label={accessibleEditLabel}
        aria-hidden={isEditing}
        tabIndex={isEditing ? -1 : 0}
        disabled={disabled || locked || isEditing}
        onClick={beginEdit}
      >
        Edit
      </button>}
    </div>

    <div className="inline-editable-field__value" ref={editorHostRef} aria-labelledby={isEditing ? labelId : undefined} aria-describedby={isEditing && resolvedError !== null ? messageId : undefined} onKeyDown={isEditing ? handleEditorKeyDown : undefined}>
      {isEditing ? renderEditor({
        value: draft,
        draft,
        onChange: changeDraft,
        id: editorId,
        labelId,
        describedBy: resolvedError !== null ? messageId : undefined,
        disabled: editorDisabled,
        autoFocus: state === 'editing',
        onKeyDown: handleEditorKeyDown,
      }) : renderDisplay(value)}
    </div>

    <div className="inline-editable-field__actions" role="group" aria-label={`${actionLabel || 'Field'} actions`}>
      {isEditing && <>
        <button type="button" className="inline-editable-field__action inline-editable-field__action--cancel" disabled={state === 'saving'} onClick={cancel}>
          {cancelLabel}
        </button>
        <button type="button" className="inline-editable-field__action inline-editable-field__action--save" disabled={editorDisabled} onClick={() => void save()}>
          {state === 'saving' ? 'Saving…' : state === 'error' ? retryLabel : saveLabel}
        </button>
      </>}
    </div>

    <div id={messageId} className="inline-editable-field__message" role={resolvedError !== null ? 'alert' : undefined} aria-live={resolvedError !== null ? 'assertive' : 'polite'} aria-atomic="true">
      {resolvedError !== null && <span className="inline-editable-field__error">{resolvedError}</span>}
    </div>
  </div>
}

export default InlineEditableField
