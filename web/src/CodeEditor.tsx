import { useEffect, useRef } from 'react'
import { EditorView, basicSetup } from 'codemirror'
import { keymap } from '@codemirror/view'
import { Compartment, EditorState, Prec } from '@codemirror/state'
import { sql, SQLite, type SQLNamespace } from '@codemirror/lang-sql'
import { HighlightStyle, syntaxHighlighting } from '@codemirror/language'
import { tags } from '@lezer/highlight'
import { closeCompletion } from '@codemirror/autocomplete'
import type { Theme } from './lib'

// Colours chosen to read on both themes' code background.
const highlight = HighlightStyle.define([
  { tag: tags.keyword, color: 'var(--cm-keyword)' },
  { tag: [tags.string, tags.special(tags.string)], color: 'var(--cm-string)' },
  { tag: [tags.number, tags.bool, tags.null], color: 'var(--cm-number)' },
  { tag: tags.comment, color: 'var(--text-3)', fontStyle: 'italic' },
  { tag: [tags.typeName, tags.standard(tags.name)], color: 'var(--cm-type)' },
  { tag: tags.operator, color: 'var(--text-2)' },
])

const chrome = EditorView.theme({
  '&': { backgroundColor: 'var(--code-bg)', color: 'var(--text)' },
  '.cm-content': { fontFamily: 'var(--mono)', caretColor: 'var(--brand)', padding: '10px 0' },
  '.cm-gutters': { backgroundColor: 'var(--code-bg)', color: 'var(--text-3)', border: 'none' },
  '.cm-activeLine, .cm-activeLineGutter': { backgroundColor: 'color-mix(in srgb, var(--text) 4%, transparent)' },
  '.cm-selectionBackground, &.cm-focused .cm-selectionBackground': {
    backgroundColor: 'color-mix(in srgb, var(--brand) 25%, transparent) !important',
  },
  '.cm-tooltip': { backgroundColor: 'var(--panel)', border: '1px solid var(--border-strong)', color: 'var(--text)' },
  '.cm-tooltip-autocomplete > ul > li[aria-selected]': { backgroundColor: 'var(--surface-2)', color: 'var(--text)' },
  '.cm-cursor': { borderLeftColor: 'var(--brand)' },
})

/**
 * A SQL editor with SQLite highlighting and table/column completion.
 * onRun gets the selected text, or the whole script if nothing is selected.
 */
export function CodeEditor({
  value,
  onChange,
  onRun,
  schema,
  theme,
}: {
  value: string
  onChange: (v: string) => void
  onRun: (sql: string) => void
  schema: SQLNamespace
  theme: Theme
}) {
  const host = useRef<HTMLDivElement>(null)
  const view = useRef<EditorView | null>(null)
  const lang = useRef(new Compartment())
  const handlers = useRef({ onChange, onRun })
  handlers.current = { onChange, onRun }

  useEffect(() => {
    const run = (v: EditorView) => {
      closeCompletion(v)
      const sel = v.state.selection.main
      const text = sel.empty ? v.state.doc.toString() : v.state.sliceDoc(sel.from, sel.to)
      handlers.current.onRun(text)
      return true
    }
    view.current = new EditorView({
      parent: host.current!,
      state: EditorState.create({
        doc: value,
        extensions: [
          Prec.highest(keymap.of([{ key: 'Mod-Enter', run }])),
          basicSetup,
          lang.current.of(sql({ dialect: SQLite, schema, upperCaseKeywords: true })),
          syntaxHighlighting(highlight),
          chrome,
          EditorView.updateListener.of((u) => u.docChanged && handlers.current.onChange(u.state.doc.toString())),
        ],
      }),
    })
    return () => view.current?.destroy()
    // Created once; value and schema changes are pushed in below.
  }, [])

  // Replace the document when a different query is opened.
  useEffect(() => {
    const v = view.current
    if (v && v.state.doc.toString() !== value) {
      v.dispatch({ changes: { from: 0, to: v.state.doc.length, insert: value } })
    }
  }, [value])

  useEffect(() => {
    view.current?.dispatch({ effects: lang.current.reconfigure(sql({ dialect: SQLite, schema, upperCaseKeywords: true })) })
  }, [schema])

  return <div ref={host} className="cm-host" data-theme={theme} />
}
