import { useCallback, useEffect, useMemo, useState } from 'react'
import { DataGrid, SelectColumn, type Column as GridColumn, type SortColumn } from 'react-data-grid'
import { Columns3, Database as DatabaseIcon, Eye, Filter as FilterIcon, Pencil, Plus, RefreshCw, Search, Table2, Trash2, X } from 'lucide-react'
import { api, errorMessage, type CellValue, type Filter, type FilterOp, type RowsResult, type TableInfo } from './api'
import { go, usePoll } from './lib'
import { CellEditor, CellView, ColumnHeader, columnWidth, isBlob, type GridRow } from './GridCells'
import { AddColumnPanel, ConfirmDialog, CreateTablePanel, RenameDialog, RowPanel } from './TablePanels'
import { Menu } from './ui'

const opLabels: Record<FilterOp, string> = {
  eq: 'equals',
  neq: 'is not',
  gt: 'greater than',
  gte: 'at least',
  lt: 'less than',
  lte: 'at most',
  contains: 'contains',
  is_null: 'is NULL',
  not_null: 'is not NULL',
}

/** The project's SQLite files, and which one the editors work on. */
export function useDatabase(name: string) {
  const { data } = usePoll(() => api.databases(name), 0, `dbs:${name}`)
  const key = `sideyard:db:${name}`
  const [chosen, setChosen] = useState(() => localStorage.getItem(key))
  const paths = data?.databases.map((d) => d.path) ?? null
  const path = paths ? (chosen && paths.includes(chosen) ? chosen : (paths[0] ?? null)) : null
  return {
    paths,
    path,
    choose: (p: string) => {
      localStorage.setItem(key, p)
      setChosen(p)
    },
  }
}

/** The SQLite file being edited: a menu when DATA_DIR holds more than one. */
export function DatabasePicker({ paths, path, choose }: ReturnType<typeof useDatabase>) {
  return (
    <label className="db-picker" title="SQLite file in the app's DATA_DIR">
      <DatabaseIcon size={13} />
      {paths && paths.length > 1 ? (
        <select value={path ?? ''} onChange={(e) => choose(e.target.value)} aria-label="Database">
          {paths.map((p) => (
            <option key={p}>{p}</option>
          ))}
        </select>
      ) : (
        <span>{path ?? 'no database'}</span>
      )}
    </label>
  )
}

type Panel =
  | { kind: 'insert' }
  | { kind: 'edit'; index: number }
  | { kind: 'create-table' }
  | { kind: 'add-column' }
  | { kind: 'rename-column'; column: string }
  | { kind: 'drop-column'; column: string }
  | { kind: 'rename-table' }
  | { kind: 'drop-table' }
  | { kind: 'delete-rows' }
  | null

export function TableEditor({ name, table }: { name: string; table?: string }) {
  const db = useDatabase(name)
  const [schemaVersion, setSchemaVersion] = useState(0)
  const { data: schema, error: schemaError } = usePoll(
    () => (db.path ? api.schema(name, db.path) : Promise.resolve([] as TableInfo[])),
    0,
    `${name}:${db.path}:${schemaVersion}`,
  )
  const [search, setSearch] = useState('')
  const [panel, setPanel] = useState<Panel>(null)
  const info = schema?.find((t) => t.name === table) ?? null
  const reloadSchema = () => setSchemaVersion((v) => v + 1)
  const open = (t: string) => go(`/p/${encodeURIComponent(name)}/tables/${encodeURIComponent(t)}`)
  // With no table chosen, open the first one rather than an empty page.
  useEffect(() => {
    const first = schema?.find((t) => t.type === 'table')
    if (!table && first) window.location.replace(`#/p/${encodeURIComponent(name)}/tables/${encodeURIComponent(first.name)}`)
  }, [table, schema, name])

  if (db.paths && db.paths.length === 0) {
    return (
      <div className="editor-empty">
        <div>
          <Table2 size={28} />
          <h3>No database yet</h3>
          <p className="muted">
            Tables appear once the app creates its SQLite file, or after an import on the{' '}
            <a href={`#/p/${encodeURIComponent(name)}/database`}>Database</a> page.
          </p>
        </div>
      </div>
    )
  }

  const tables = (schema ?? []).filter((t) => t.name.toLowerCase().includes(search.toLowerCase()))
  return (
    <div className="editor">
      <div className="editor-side">
        <div className="editor-side-head">
          <div className="editor-side-title">
            <span>Tables</span>
            <button className="small" onClick={() => setPanel({ kind: 'create-table' })} disabled={!db.path}>
              <Plus size={13} /> New table
            </button>
          </div>
          <DatabasePicker {...db} />
          <div className="search" style={{ maxWidth: 'none' }}>
            <Search size={14} />
            <input value={search} onChange={(e) => setSearch(e.target.value)} placeholder="Search tables" />
          </div>
        </div>
        <div className="editor-list">
          {schemaError && <p className="error small-text">{schemaError}</p>}
          {schema && tables.length === 0 && <p className="faint small-text" style={{ padding: 8 }}>No tables</p>}
          {tables.map((t) => (
            <button key={t.name} className={`editor-list-item ${t.name === table ? 'active' : ''}`} onClick={() => open(t.name)}>
              {t.type === 'table' ? <Table2 size={14} /> : <Eye size={14} />}
              <span className="name">{t.name}</span>
              <span className="count">{t.rows >= 0 ? t.rows.toLocaleString() : ''}</span>
            </button>
          ))}
        </div>
      </div>

      <div className="editor-main">
        {info && db.path ? (
          <TableView
            key={`${db.path}:${info.name}`}
            name={name}
            path={db.path}
            info={info}
            panel={panel}
            setPanel={setPanel}
            onSchemaChange={reloadSchema}
            onRenamed={(t) => {
              reloadSchema()
              open(t)
            }}
            onDropped={() => {
              reloadSchema()
              go(`/p/${encodeURIComponent(name)}/tables`)
            }}
          />
        ) : (
          <div className="editor-empty">
            <div>
              <Table2 size={28} />
              <h3>{table && schema ? `No table named ${table}` : 'Select a table'}</h3>
              <p className="muted">Pick one on the left to browse and edit its rows, or create a new one.</p>
              <button className="primary" onClick={() => setPanel({ kind: 'create-table' })} disabled={!db.path}>
                <Plus size={14} /> New table
              </button>
            </div>
          </div>
        )}
      </div>

      {panel?.kind === 'create-table' && db.path && (
        <CreateTablePanel
          onClose={() => setPanel(null)}
          onCreate={async (t, columns) => {
            await api.alter(name, db.path!, { kind: 'create_table', table: t, columns })
            setPanel(null)
            reloadSchema()
            open(t)
          }}
        />
      )}
    </div>
  )
}

function TableView({
  name,
  path,
  info,
  panel,
  setPanel,
  onSchemaChange,
  onRenamed,
  onDropped,
}: {
  name: string
  path: string
  info: TableInfo
  panel: Panel
  setPanel: (p: Panel) => void
  onSchemaChange: () => void
  onRenamed: (table: string) => void
  onDropped: () => void
}) {
  const [view, setView] = useState<'data' | 'structure'>('data')
  const [sort, setSort] = useState<SortColumn[]>([])
  const [filters, setFilters] = useState<Filter[]>([])
  const [draft, setDraft] = useState<Filter[] | null>(null)
  const [limit, setLimit] = useState(100)
  const [offset, setOffset] = useState(0)
  const [data, setData] = useState<RowsResult | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [selected, setSelected] = useState<ReadonlySet<string>>(new Set())
  const [loading, setLoading] = useState(false)

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const res = await api.rows(name, path, {
        table: info.name,
        limit,
        offset,
        sort: sort.map((s) => ({ column: s.columnKey, desc: s.direction === 'DESC' })),
        filters,
      })
      setData(res)
      setError(null)
    } catch (e) {
      setError(errorMessage(e))
    } finally {
      setLoading(false)
    }
  }, [name, path, info.name, limit, offset, sort, filters])

  useEffect(() => {
    load()
  }, [load])

  const editable = !!data?.editable
  // Widths are measured from the first page, then left alone so a resize
  // survives refreshes.
  const [widths, setWidths] = useState<Record<string, number> | null>(null)
  useEffect(() => {
    if (!widths && data) {
      setWidths(
        Object.fromEntries(
          info.columns.map((c, j) => [c.name, columnWidth(`${c.name} ${c.type}`, data.rows.map((r) => r[j]))]),
        ),
      )
    }
  }, [data, widths, info.columns])
  const rows: GridRow[] = useMemo(
    () =>
      (data?.rows ?? []).map((r, i) => {
        const row: GridRow = { __k: JSON.stringify(data!.keys[i] ?? i), __i: i }
        data!.columns.forEach((c, j) => (row[c] = r[j]))
        return row
      }),
    [data],
  )

  const columns = useMemo<GridColumn<GridRow>[]>(() => {
    const cols: GridColumn<GridRow>[] = info.columns.map((c) => ({
      key: c.name,
      name: c.name,
      resizable: true,
      sortable: true,
      minWidth: 80,
      width: widths?.[c.name] ?? 140,
      editable: (row) => editable && !c.generated && !isBlob(row[c.name]),
      renderHeaderCell: (p) => <ColumnHeader col={c} sortDirection={p.sortDirection} />,
      renderCell: (p) => <CellView value={p.row[c.name]} />,
      renderEditCell: CellEditor,
    }))
    return editable ? [SelectColumn, ...cols] : cols
  }, [info, editable, widths])

  async function onRowsChange(next: GridRow[], change: { indexes: number[]; column: { key: string } }) {
    const col = change.column.key
    for (const i of change.indexes) {
      const before = rows[i][col]
      const after = next[i][col] as string
      if ((before === null ? '' : String(before)) === after) continue
      try {
        await api.edit(name, path, { table: info.name, kind: 'update', keys: [data!.keys[i]], values: { [col]: after } })
      } catch (e) {
        setError(errorMessage(e))
      }
    }
    load()
  }

  const rowValues = (i: number): Record<string, CellValue> =>
    Object.fromEntries(data!.columns.map((c, j) => [c, data!.rows[i][j]]))

  const done = async (fn: () => Promise<unknown>, schema = false) => {
    await fn()
    setPanel(null)
    if (schema) onSchemaChange()
    load()
  }

  const keysFor = (ks: ReadonlySet<string>) => rows.filter((r) => ks.has(r.__k)).map((r) => data!.keys[r.__i])
  const readOnly = info.type !== 'table'

  return (
    <>
      <div className="editor-toolbar">
        <div className="title">
          <Table2 size={15} />
          {info.name}
          {readOnly && <span className="badge">{info.type} · read-only</span>}
        </div>
        {view === 'data' && (
          <>
            {!readOnly && (
              <Menu
                trigger={() => (
                  <button className="primary small">
                    <Plus size={13} /> Insert
                  </button>
                )}
              >
                {(close) => (
                  <>
                    <button className="menu-item" onClick={() => (close(), setPanel({ kind: 'insert' }))}>
                      <Plus size={14} /> Insert row
                    </button>
                    <button className="menu-item" onClick={() => (close(), setPanel({ kind: 'add-column' }))}>
                      <Columns3 size={14} /> Insert column
                    </button>
                  </>
                )}
              </Menu>
            )}
            <button className="small" onClick={() => setDraft(draft ? null : filters.length ? filters : [{ column: info.columns[0]?.name, op: 'eq', value: '' }])}>
              <FilterIcon size={13} /> {filters.length ? `Filtered by ${filters.length}` : 'Filter'}
            </button>
            {selected.size > 0 && (
              <button className="small danger" onClick={() => setPanel({ kind: 'delete-rows' })}>
                <Trash2 size={13} /> Delete {selected.size} row{selected.size === 1 ? '' : 's'}
              </button>
            )}
            {selected.size === 1 && (
              <button className="small" onClick={() => setPanel({ kind: 'edit', index: rows.find((r) => selected.has(r.__k))!.__i })}>
                <Pencil size={13} /> Edit row
              </button>
            )}
          </>
        )}
        <span className="grow" />
        <button className="icon" onClick={load} title="Refresh" aria-label="Refresh">
          <RefreshCw size={15} className={loading ? 'spin' : ''} />
        </button>
        <div className="segmented">
          <button className={view === 'data' ? 'on' : ''} onClick={() => setView('data')}>
            Data
          </button>
          <button className={view === 'structure' ? 'on' : ''} onClick={() => setView('structure')}>
            Structure
          </button>
        </div>
      </div>

      {error && (
        <div className="banner" style={{ margin: '10px 12px 0' }}>
          <span className="grow">{error}</span>
          <button className="icon" onClick={() => setError(null)} aria-label="Dismiss">
            <X size={14} />
          </button>
        </div>
      )}

      {view === 'data' && draft && (
        <div className="filters">
          {draft.map((f, i) => (
            <div key={i} className="filter-row">
              <select value={f.column} onChange={(e) => setDraft(draft.map((x, j) => (j === i ? { ...x, column: e.target.value } : x)))}>
                {info.columns.map((c) => (
                  <option key={c.name}>{c.name}</option>
                ))}
              </select>
              <select value={f.op} onChange={(e) => setDraft(draft.map((x, j) => (j === i ? { ...x, op: e.target.value as FilterOp } : x)))}>
                {Object.entries(opLabels).map(([op, label]) => (
                  <option key={op} value={op}>
                    {label}
                  </option>
                ))}
              </select>
              {f.op === 'is_null' || f.op === 'not_null' ? (
                <span />
              ) : (
                <input value={f.value} placeholder="value" onChange={(e) => setDraft(draft.map((x, j) => (j === i ? { ...x, value: e.target.value } : x)))} />
              )}
              <button className="icon" onClick={() => setDraft(draft.filter((_, j) => j !== i))} aria-label="Remove filter">
                <X size={14} />
              </button>
            </div>
          ))}
          <div className="inline-form" style={{ marginTop: 0 }}>
            <button className="small" onClick={() => setDraft([...draft, { column: info.columns[0]?.name, op: 'eq', value: '' }])}>
              <Plus size={13} /> Add filter
            </button>
            <span className="grow" />
            <button
              className="small ghost"
              onClick={() => {
                setFilters([])
                setDraft(null)
                setOffset(0)
              }}
            >
              Clear
            </button>
            <button
              className="small primary"
              onClick={() => {
                setFilters(draft)
                setDraft(null)
                setOffset(0)
              }}
            >
              Apply
            </button>
          </div>
        </div>
      )}

      {view === 'data' ? (
        <>
          <div className="grid-wrap">
            <DataGrid
              className="sy-grid"
              columns={columns}
              rows={rows}
              rowKeyGetter={(r) => r.__k}
              selectedRows={selected}
              onSelectedRowsChange={setSelected}
              onRowsChange={onRowsChange}
              sortColumns={sort}
              onSortColumnsChange={(s) => {
                setSort(s.slice(-1))
                setOffset(0)
              }}
              rowHeight={34}
              headerRowHeight={36}
            />
          </div>
          <div className="editor-footer">
            {data && (
              <span>
                {data.total === 0
                  ? 'No rows'
                  : `Rows ${(offset + 1).toLocaleString()}–${(offset + data.rows.length).toLocaleString()} of ${data.total.toLocaleString()}`}
              </span>
            )}
            {editable && <span className="faint">Double-click a cell to edit it</span>}
            <span className="grow" />
            <select
              value={limit}
              onChange={(e) => {
                setLimit(Number(e.target.value))
                setOffset(0)
              }}
            >
              {[50, 100, 250, 500].map((n) => (
                <option key={n} value={n}>
                  {n} rows
                </option>
              ))}
            </select>
            <button className="small" disabled={offset === 0} onClick={() => setOffset(Math.max(0, offset - limit))}>
              Previous
            </button>
            <button className="small" disabled={!data || offset + limit >= data.total} onClick={() => setOffset(offset + limit)}>
              Next
            </button>
          </div>
        </>
      ) : (
        <Structure info={info} onPanel={setPanel} />
      )}

      {panel?.kind === 'insert' && (
        <RowPanel
          table={info}
          row={null}
          onClose={() => setPanel(null)}
          onSave={(values) => done(() => api.edit(name, path, { table: info.name, kind: 'insert', values }))}
        />
      )}
      {panel?.kind === 'edit' && data && (
        <RowPanel
          table={info}
          row={rowValues(panel.index)}
          onClose={() => setPanel(null)}
          onSave={(values) =>
            done(() => api.edit(name, path, { table: info.name, kind: 'update', keys: [data.keys[panel.index]], values }))
          }
        />
      )}
      {panel?.kind === 'delete-rows' && (
        <ConfirmDialog
          title={`Delete ${selected.size} row${selected.size === 1 ? '' : 's'}?`}
          body={`They'll be removed from ${info.name} straight away. The app sees the change immediately.`}
          action="Delete"
          onClose={() => setPanel(null)}
          onConfirm={() =>
            done(async () => {
              await api.edit(name, path, { table: info.name, kind: 'delete', keys: keysFor(selected) })
              setSelected(new Set())
            })
          }
        />
      )}
      {panel?.kind === 'add-column' && (
        <AddColumnPanel
          table={info.name}
          onClose={() => setPanel(null)}
          onAdd={(def) => done(() => api.alter(name, path, { kind: 'add_column', table: info.name, def }), true)}
        />
      )}
      {panel?.kind === 'rename-column' && (
        <RenameDialog
          what={`column ${panel.column}`}
          current={panel.column}
          onClose={() => setPanel(null)}
          onRename={(newName) =>
            done(() => api.alter(name, path, { kind: 'rename_column', table: info.name, column: panel.column, newName }), true)
          }
        />
      )}
      {panel?.kind === 'drop-column' && (
        <ConfirmDialog
          title={`Drop column ${panel.column}?`}
          body="Its data is deleted from every row. If the app still reads this column, it will break."
          action="Drop column"
          onClose={() => setPanel(null)}
          onConfirm={() => done(() => api.alter(name, path, { kind: 'drop_column', table: info.name, column: panel.column }), true)}
        />
      )}
      {panel?.kind === 'rename-table' && (
        <RenameDialog
          what={`table ${info.name}`}
          current={info.name}
          onClose={() => setPanel(null)}
          onRename={async (newName) => {
            await api.alter(name, path, { kind: 'rename_table', table: info.name, newName })
            setPanel(null)
            onRenamed(newName)
          }}
        />
      )}
      {panel?.kind === 'drop-table' && (
        <ConfirmDialog
          title={`Drop table ${info.name}?`}
          body={`All ${info.rows.toLocaleString()} rows are deleted for good. Download a backup on the Database page first if you might need them.`}
          action="Drop table"
          typeToConfirm={info.name}
          onClose={() => setPanel(null)}
          onConfirm={async () => {
            await api.alter(name, path, { kind: 'drop_table', table: info.name })
            setPanel(null)
            onDropped()
          }}
        />
      )}
    </>
  )
}

function Structure({ info, onPanel }: { info: TableInfo; onPanel: (p: Panel) => void }) {
  const editable = info.type === 'table'
  return (
    <div className="structure">
      <p className="muted small-text" style={{ marginTop: 0 }}>
        Changes here apply to the live database at once, outside the app's own migrations. Make sure the app's code
        expects them.
      </p>
      <div className="editor-side-title" style={{ marginBottom: 8 }}>
        <h3>Columns</h3>
        {editable && (
          <button className="small" onClick={() => onPanel({ kind: 'add-column' })}>
            <Plus size={13} /> Add column
          </button>
        )}
      </div>
      <table className="struct">
        <thead>
          <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Required</th>
            <th>Default</th>
            <th>Key</th>
            <th />
          </tr>
        </thead>
        <tbody>
          {info.columns.map((c) => (
            <tr key={c.name}>
              <td>
                <code>{c.name}</code>
              </td>
              <td className="muted">{c.type || 'any'}</td>
              <td className="muted">{c.notNull ? 'yes' : ''}</td>
              <td className="muted">{c.default ?? ''}</td>
              <td className="muted">{c.pk > 0 ? 'primary' : c.generated ? 'generated' : ''}</td>
              <td>
                {editable && (
                  <div className="row-actions">
                    <button className="small ghost" onClick={() => onPanel({ kind: 'rename-column', column: c.name })}>
                      Rename
                    </button>
                    <button className="small ghost" onClick={() => onPanel({ kind: 'drop-column', column: c.name })}>
                      Drop
                    </button>
                  </div>
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>

      <h3 style={{ marginTop: 24 }}>Indexes</h3>
      {info.indexes.length === 0 ? (
        <p className="faint small-text">None</p>
      ) : (
        <table className="struct">
          <tbody>
            {info.indexes.map((ix) => (
              <tr key={ix.name}>
                <td>
                  <code>{ix.name}</code>
                </td>
                <td className="muted">{ix.columns.join(', ')}</td>
                <td className="muted">{ix.unique ? 'unique' : ''}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      <h3 style={{ marginTop: 24 }}>Definition</h3>
      <div className="codeblock">
        <pre>{info.sql || '—'}</pre>
      </div>

      {editable && (
        <div className="inline-form" style={{ marginTop: 24 }}>
          <button onClick={() => onPanel({ kind: 'rename-table' })}>Rename table</button>
          <button className="danger" onClick={() => onPanel({ kind: 'drop-table' })}>
            <Trash2 size={14} /> Drop table
          </button>
        </div>
      )}
    </div>
  )
}
