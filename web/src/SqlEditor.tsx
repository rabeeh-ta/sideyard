import { useEffect, useMemo, useRef, useState } from 'react'
import { DataGrid, type Column as GridColumn } from 'react-data-grid'
import { FileCode2, Play, Plus, Trash2 } from 'lucide-react'
import { api, errorMessage, type ExecResult, type TableInfo } from './api'
import { useTheme, usePoll } from './lib'
import { CodeEditor } from './CodeEditor'
import { CellView, columnWidth, type GridRow } from './GridCells'
import { DatabasePicker, useDatabase } from './TableEditor'
import { Dialog } from './ui'

type Query = { id: string; name: string; sql: string; updatedAt: number }

const templates: Omit<Query, 'id' | 'updatedAt'>[] = [
  {
    name: 'Tables and row counts',
    sql: "SELECT name, type FROM sqlite_schema\nWHERE type IN ('table', 'view') AND name NOT LIKE 'sqlite_%'\nORDER BY name;",
  },
  {
    name: 'Database size',
    sql: 'SELECT page_count * page_size AS bytes, freelist_count * page_size AS free_bytes\nFROM pragma_page_count(), pragma_page_size(), pragma_freelist_count();',
  },
  { name: 'Indexes', sql: "SELECT tbl_name AS \"table\", name, sql FROM sqlite_schema\nWHERE type = 'index' ORDER BY tbl_name;" },
]

/** Saved queries, kept in this browser per project; edits save as you type. */
function useQueries(project: string) {
  const key = `sideyard:queries:${project}`
  const [queries, setQueries] = useState<Query[]>(() => {
    try {
      return JSON.parse(localStorage.getItem(key) ?? '[]')
    } catch {
      return []
    }
  })
  const save = (next: Query[]) => {
    setQueries(next)
    localStorage.setItem(key, JSON.stringify(next))
  }
  return {
    queries,
    create: (q: Omit<Query, 'id' | 'updatedAt'>) => {
      const created = { ...q, id: crypto.randomUUID(), updatedAt: Date.now() }
      save([created, ...queries])
      return created
    },
    update: (id: string, patch: Partial<Query>) =>
      save(queries.map((q) => (q.id === id ? { ...q, ...patch, updatedAt: Date.now() } : q))),
    remove: (id: string) => save(queries.filter((q) => q.id !== id)),
  }
}

export function SqlEditor({ name }: { name: string }) {
  const db = useDatabase(name)
  const [theme] = useTheme()
  const { queries, create, update, remove } = useQueries(name)
  const [currentId, setCurrentId] = useState<string | null>(() => queries[0]?.id ?? null)
  const current = queries.find((q) => q.id === currentId) ?? null
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

  useEffect(() => {
    if (!current && queries.length === 0) {
      setCurrentId(create({ name: 'Untitled query', sql: 'SELECT ' }).id)
    }
  }, [current, queries.length])

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
            <button className="small" onClick={() => setCurrentId(create({ name: 'Untitled query', sql: '' }).id)}>
              <Plus size={13} /> New query
            </button>
          </div>
        </div>
        <div className="editor-list">
          <div className="menu-label">Saved in this browser</div>
          {queries.map((q) => (
            <div key={q.id} style={{ display: 'flex', alignItems: 'center' }}>
              <button className={`editor-list-item ${q.id === currentId ? 'active' : ''}`} onClick={() => setCurrentId(q.id)}>
                <FileCode2 size={14} />
                <span className="name">{q.name || 'Untitled query'}</span>
              </button>
              {q.id === currentId && (
                <button
                  className="icon"
                  title="Delete query"
                  aria-label="Delete query"
                  onClick={() => {
                    remove(q.id)
                    setCurrentId(queries.find((x) => x.id !== q.id)?.id ?? null)
                  }}
                >
                  <Trash2 size={13} />
                </button>
              )}
            </div>
          ))}
          <div className="menu-label" style={{ marginTop: 12 }}>
            Templates
          </div>
          {templates.map((t) => (
            <button key={t.name} className="editor-list-item" onClick={() => setCurrentId(create(t).id)}>
              <FileCode2 size={14} />
              <span className="name">{t.name}</span>
            </button>
          ))}
        </div>
      </div>

      <div className="editor-main">
        <div className="editor-toolbar">
          <input
            className="title"
            style={{ maxWidth: 260, border: 'none', background: 'transparent', boxShadow: 'none', paddingLeft: 0 }}
            value={current?.name ?? ''}
            onChange={(e) => current && update(current.id, { name: e.target.value })}
            aria-label="Query name"
          />
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
              onChange={(sql) => update(current.id, { sql })}
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
          <div className="codeblock">
            <pre style={{ maxHeight: 180, overflow: 'auto', paddingRight: 14 }}>{confirm}</pre>
          </div>
        </Dialog>
      )}
    </div>
  )
}
