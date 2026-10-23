import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { DataGrid, type Column as GridColumn } from 'react-data-grid'
import { FileCode2, Pencil, Play, Plus, Trash2 } from 'lucide-react'
import { api, errorMessage, type ExecResult, type SavedQuery, type TableInfo } from './api'
import { useTheme, usePoll } from './lib'
import { CodeEditor } from './CodeEditor'
import { CellView, columnWidth, type GridRow } from './GridCells'
import { DatabasePicker, useDatabase } from './TableEditor'
import { Dialog } from './ui'

const templates: Omit<SavedQuery, 'id'>[] = [
  {
    name: 'Tables and views',
    sql: "SELECT name, type FROM sqlite_schema\nWHERE type IN ('table', 'view') AND name NOT LIKE 'sqlite_%'\nORDER BY name;",
  },
  {
    name: 'Database size',
    sql: 'SELECT page_count * page_size AS bytes, freelist_count * page_size AS free_bytes\nFROM pragma_page_count(), pragma_page_size(), pragma_freelist_count();',
  },
  { name: 'Indexes', sql: "SELECT tbl_name AS \"table\", name, sql FROM sqlite_schema\nWHERE type = 'index' ORDER BY tbl_name;" },
]

/**
 * The project's saved queries. Sideyard keeps them, so every browser sees
 * the same ones; edits save half a second after typing stops.
 */
function useQueries(project: string) {
  const [queries, setQueries] = useState<SavedQuery[] | null>(null)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const latest = useRef(queries)
  latest.current = queries
  const unsaved = useRef(new Map<string, SavedQuery>()) // edits not sent yet
  const edits = useRef(0) // counts changes, so a reload can tell it raced one
  const flushing = useRef<Promise<void>>(Promise.resolve())
  const timer = useRef<ReturnType<typeof setTimeout>>(undefined)

  // Saves run one after another, so an older version never lands last.
  const flush = useCallback(() => {
    clearTimeout(timer.current)
    const batch = [...unsaved.current.values()]
    unsaved.current.clear()
    if (batch.length > 0) {
      flushing.current = flushing.current.then(async () => {
        try {
          await Promise.all(batch.map((q) => api.saveQuery(project, q)))
          setError(null)
        } catch (e) {
          // Keep them for the next try, unless they've been edited since.
          for (const q of batch) if (!unsaved.current.has(q.id)) unsaved.current.set(q.id, q)
          setError(errorMessage(e))
        } finally {
          setSaving(unsaved.current.size > 0)
        }
      })
    }
    return flushing.current
  }, [project])

  const schedule = (q: SavedQuery, delay: number) => {
    edits.current++
    unsaved.current.set(q.id, q)
    setSaving(true)
    clearTimeout(timer.current)
    timer.current = setTimeout(flush, delay)
  }

  // Coming back to the page picks up changes made in other tabs and
  // browsers, unless an edit made here meanwhile would be overwritten.
  const reload = useCallback(async () => {
    const before = edits.current
    await flushing.current
    try {
      const list = await api.queries(project)
      if (edits.current === before && unsaved.current.size === 0) setQueries(list)
    } catch {
      // Keep the list we have.
    }
  }, [project])

  useEffect(() => {
    let live = true
    setQueries(null)
    moveFromBrowser(project)
      .catch(() => {}) // they stay in the browser and move next time
      .then(() => api.queries(project))
      .then((list) => live && setQueries(list))
      .catch((e) => live && setError(errorMessage(e)))
    const onFocus = () => void reload()
    // A page closed within half a second of typing would lose the edit.
    const onLeave = (e: BeforeUnloadEvent) => {
      if (unsaved.current.size > 0) {
        void flush()
        e.preventDefault()
      }
    }
    window.addEventListener('focus', onFocus)
    window.addEventListener('beforeunload', onLeave)
    return () => {
      live = false
      window.removeEventListener('focus', onFocus)
      window.removeEventListener('beforeunload', onLeave)
      void flush()
    }
  }, [project, reload, flush])

  const replace = (next: SavedQuery[]) => {
    latest.current = next
    setQueries(next)
  }

  return {
    queries,
    saving,
    error,
    create(q: Omit<SavedQuery, 'id'>) {
      const created = { ...q, id: crypto.randomUUID() }
      replace([created, ...(latest.current ?? [])])
      schedule(created, 0)
      return created
    },
    update(id: string, patch: Partial<SavedQuery>) {
      const q = latest.current?.find((x) => x.id === id)
      if (!q) return
      const next = { ...q, ...patch }
      replace(latest.current!.map((x) => (x.id === id ? next : x)))
      schedule(next, 500)
    },
    async remove(id: string) {
      edits.current++
      unsaved.current.delete(id)
      replace((latest.current ?? []).filter((q) => q.id !== id))
      await flushing.current // a save still on its way would bring it back
      try {
        await api.deleteQuery(project, id)
      } catch (e) {
        setError(errorMessage(e))
        void reload()
      }
    },
  }
}

// Saved queries used to live in each browser. Move this browser's to
// Sideyard, oldest first so they keep their order, then forget them here.
async function moveFromBrowser(project: string) {
  const key = `sideyard:queries:${project}`
  let old: unknown
  try {
    old = JSON.parse(localStorage.getItem(key) ?? '[]')
  } catch {
    return
  }
  if (!Array.isArray(old)) return
  for (const q of [...old].reverse()) {
    const id = typeof q?.id === 'string' && /^[A-Za-z0-9-]{1,64}$/.test(q.id) ? q.id : crypto.randomUUID()
    await api.saveQuery(project, { id, name: String(q?.name ?? '').slice(0, 100), sql: String(q?.sql ?? '') })
  }
  try {
    localStorage.removeItem(key)
  } catch {
    // Saving them again next time does no harm.
  }
}

export function SqlEditor({ name }: { name: string }) {
  const db = useDatabase(name)
  const [theme] = useTheme()
  const { queries, saving, error: saveError, create, update, remove } = useQueries(name)
  // A saved query's ID, or template:<n> for a template.
  const [currentId, setCurrentId] = useState<string | null>(null)
  const template = currentId?.startsWith('template:') ? templates[Number(currentId.slice(9))] : undefined
  const current = template ? { id: currentId!, ...template } : (queries?.find((q) => q.id === currentId) ?? null)
  const [deleting, setDeleting] = useState<SavedQuery | null>(null)
  const [result, setResult] = useState<{ res: ExecResult; ms: number; write: boolean } | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [running, setRunning] = useState(false)
  const [confirm, setConfirm] = useState<string | null>(null)
  const [editorHeight, setEditorHeight] = useState(42)
  const splitRef = useRef<HTMLDivElement>(null)

  const { data: schema } = usePoll(
    () => (db.path ? api.schema(name, db.path) : Promise.resolve([] as TableInfo[])),
    0,
    `${name}:${db.path}:${result?.write ? result.ms : ''}`,
  )
  const completion = useMemo(
    () => Object.fromEntries((schema ?? []).map((t) => [t.name, t.columns.map((c) => c.name)])),
    [schema],
  )

  // Open the newest saved query, or a template when there are none.
  useEffect(() => {
    if (queries && !current) open(queries[0]?.id ?? 'template:0')
  }, [queries, current])

  // A different query starts with no results: they belonged to the last one.
  function open(id: string) {
    setCurrentId(id)
    setResult(null)
    setError(null)
  }

  // Editing a template saves a copy of it, and the template stays as it is.
  function edit(patch: Partial<SavedQuery>) {
    if (template) {
      setCurrentId(create({ ...template, ...patch }).id)
    } else if (current) {
      update(current.id, patch)
    }
  }

  async function run(text: string, write = false) {
    if (!db.path || !text.trim()) return
    setRunning(true)
    setError(null)
    try {
      const { result: res, ms } = await api.sql(name, db.path, text, write)
      if (res.needsWrite) {
        setConfirm(text)
      } else {
        setResult({ res, ms, write })
      }
    } catch (e) {
      setResult(null)
      setError(errorMessage(e))
    } finally {
      setRunning(false)
    }
  }

  function dragSplit(e: React.PointerEvent) {
    const box = splitRef.current!.getBoundingClientRect()
    const move = (ev: PointerEvent) => setEditorHeight(Math.min(85, Math.max(15, ((ev.clientY - box.top) / box.height) * 100)))
    const up = () => {
      window.removeEventListener('pointermove', move)
      window.removeEventListener('pointerup', up)
    }
    window.addEventListener('pointermove', move)
    window.addEventListener('pointerup', up)
    e.preventDefault()
  }

  const columns = useMemo<GridColumn<GridRow>[]>(
    () =>
      (result?.res.columns ?? []).map((c, i) => ({
        key: `c${i}`,
        name: c,
        resizable: true,
        minWidth: 80,
        width: columnWidth(c, (result?.res.rows ?? []).map((r) => r[i])),
        renderCell: (p) => <CellView value={p.row[`c${i}`]} />,
      })),
    [result],
  )
  const rows = useMemo<GridRow[]>(
    () =>
      (result?.res.rows ?? []).map((r, i) => {
        const row: GridRow = { __k: String(i), __i: i }
        r.forEach((v, j) => (row[`c${j}`] = v))
        return row
      }),
    [result],
  )

  return (
    <div className="editor">
      <div className="editor-side">
        <div className="editor-side-head">
          <div className="editor-side-title">
            <span>SQL Editor</span>
            <button className="small" disabled={!queries} onClick={() => open(create({ name: '', sql: '' }).id)}>
              <Plus size={13} /> New query
            </button>
          </div>
        </div>
        <div className="editor-list">
          <div className="menu-label">Saved queries</div>
          {!queries || queries.length === 0 ? (
            <p className="faint" style={{ padding: '2px 8px', fontSize: 12 }}>
              {queries ? 'None yet. Start a new query, or edit a template.' : saveError ? `Couldn't load them: ${saveError}` : 'Loading…'}
            </p>
          ) : (
            queries.map((q) => (
              <div key={q.id} style={{ display: 'flex', alignItems: 'center' }}>
                <button className={`editor-list-item ${q.id === currentId ? 'active' : ''}`} onClick={() => open(q.id)}>
                  <FileCode2 size={14} />
                  <span className="name">{q.name || 'Untitled query'}</span>
                </button>
                {q.id === currentId && (
                  <button className="icon" title="Delete query" aria-label="Delete query" onClick={() => setDeleting(q)}>
                    <Trash2 size={13} />
                  </button>
                )}
              </div>
            ))
          )}
          <div className="menu-label" style={{ marginTop: 12 }}>
            Templates
          </div>
          {templates.map((t, i) => (
            <button
              key={t.name}
              className={`editor-list-item ${currentId === `template:${i}` ? 'active' : ''}`}
              onClick={() => open(`template:${i}`)}
            >
              <FileCode2 size={14} />
              <span className="name">{t.name}</span>
            </button>
          ))}
        </div>
      </div>

      <div className="editor-main">
        <div className="editor-toolbar">
          <label className="title" title="Rename">
            <input
              style={{ maxWidth: 260, border: 'none', background: 'transparent', boxShadow: 'none', padding: 0, font: 'inherit' }}
              value={current?.name ?? ''}
              placeholder="Untitled query"
              maxLength={100}
              disabled={!current}
              onChange={(e) => edit({ name: e.target.value })}
              aria-label="Query name"
            />
            <Pencil size={12} className="faint" />
          </label>
          <span className={saveError && !template ? 'error' : 'faint'} style={{ fontSize: 12 }}>
            {template
              ? 'Template: editing it saves a copy'
              : saveError
                ? `Not saved: ${saveError}`
                : current && (saving ? 'Saving…' : 'Saved')}
          </span>
          <span className="grow" />
          <DatabasePicker {...db} />
          <button className="primary" disabled={running || !db.path || !current?.sql.trim()} onClick={() => current && run(current.sql)}>
            <Play size={13} /> {running ? 'Running…' : 'Run'}
            <span className="faint" style={{ color: 'inherit', opacity: 0.7 }}>
              ⌘↵
            </span>
          </button>
        </div>

        <div className="sql-split" ref={splitRef} style={{ ['--editor-height' as string]: `${editorHeight}%` }}>
          {current ? (
            <CodeEditor
              value={current.sql}
              onChange={(sql) => edit({ sql })}
              onRun={(text) => run(text)}
              schema={completion}
              theme={theme}
            />
          ) : (
            <div className="cm-host" />
          )}
          <div className="split-handle" onPointerDown={dragSplit} />
          <div className="results">
            {error ? (
              <div className="results-error">{error}</div>
            ) : result ? (
              result.res.columns.length > 0 ? (
                <div className="grid-wrap">
                  <DataGrid className="sy-grid" columns={columns} rows={rows} rowKeyGetter={(r) => r.__k} rowHeight={32} headerRowHeight={34} />
                </div>
              ) : (
                <div className="editor-empty">
                  <div>
                    <h3>Done</h3>
                    <p className="muted">
                      {result.write ? `${result.res.changes.toLocaleString()} row${result.res.changes === 1 ? '' : 's'} changed.` : 'No rows returned.'}
                    </p>
                  </div>
                </div>
              )
            ) : (
              <div className="editor-empty">Run a query to see results here. Select part of the script to run only that.</div>
            )}
            {result && (
              <div className="editor-footer">
                <span>
                  {result.res.rows.length.toLocaleString()} row{result.res.rows.length === 1 ? '' : 's'}
                  {result.res.truncated && ' (stopped at 1,000)'}
                </span>
                {result.write && <span>{result.res.changes.toLocaleString()} changed</span>}
                <span className="faint">{result.ms} ms</span>
              </div>
            )}
          </div>
        </div>
      </div>

      {deleting && (
        <Dialog
          onClose={() => setDeleting(null)}
          footer={
            <>
              <button onClick={() => setDeleting(null)}>Cancel</button>
              <button
                className="danger"
                onClick={() => {
                  void remove(deleting.id)
                  setDeleting(null)
                }}
              >
                Delete query
              </button>
            </>
          }
        >
          <h3>Delete “{deleting.name || 'Untitled query'}”?</h3>
          <p className="muted">It's deleted for every browser, and can't be brought back.</p>
        </Dialog>
      )}

      {confirm !== null && (
        <Dialog
          onClose={() => setConfirm(null)}
          footer={
            <>
              <button onClick={() => setConfirm(null)}>Cancel</button>
              <button
                className="danger"
                onClick={() => {
                  const text = confirm
                  setConfirm(null)
                  run(text, true)
                }}
              >
                Run and change data
              </button>
            </>
          }
        >
          <h3>This query changes the database</h3>
          <p className="muted">
            It runs against the live <code>{db.path}</code> that the app is using, and can't be undone here. Statements run in
            order; if one fails, the ones before it stay applied.
          </p>
          <div className="codeblock wrap">
            <pre>{confirm}</pre>
          </div>
        </Dialog>
      )}
    </div>
  )
}
