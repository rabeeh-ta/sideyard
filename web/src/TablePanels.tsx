import { useState } from 'react'
import { KeyRound, Plus, Trash2 } from 'lucide-react'
import type { CellValue, ColumnDef, TableInfo } from './api'
import { formatBytes } from './lib'
import { Dialog, Drawer } from './ui'
import { isBlob } from './GridCells'

export const typeOptions = ['TEXT', 'INTEGER', 'REAL', 'NUMERIC', 'BLOB', 'BOOLEAN', 'DATETIME', 'DATE', 'JSON']

function TypeList() {
  return (
    <datalist id="sqlite-types">
      {typeOptions.map((t) => (
        <option key={t} value={t} />
      ))}
    </datalist>
  )
}

type FieldState = { value: string; isNull: boolean; touched: boolean }

/**
 * Inserts a row, or edits one. Only fields you touch are sent: on insert,
 * the rest get the column's default.
 */
export function RowPanel({
  table,
  row,
  onSave,
  onClose,
}: {
  table: TableInfo
  row: Record<string, CellValue> | null // null inserts
  onSave: (values: Record<string, string | null>) => Promise<void>
  onClose: () => void
}) {
  const cols = table.columns.filter((c) => !c.generated)
  const [fields, setFields] = useState<Record<string, FieldState>>(() =>
    Object.fromEntries(
      cols.map((c) => {
        const v = row?.[c.name]
        return [c.name, { value: v === null || v === undefined || isBlob(v) ? '' : String(v), isNull: !!row && v === null, touched: false }]
      }),
    ),
  )
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const set = (name: string, patch: Partial<FieldState>) =>
    setFields({ ...fields, [name]: { ...fields[name], ...patch, touched: true } })

  async function save() {
    const values: Record<string, string | null> = {}
    for (const c of cols) {
      const f = fields[c.name]
      if (f.touched) values[c.name] = f.isNull ? null : f.value
    }
    if (row && Object.keys(values).length === 0) return onClose()
    setBusy(true)
    setError(null)
    try {
      await onSave(values)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
      setBusy(false)
    }
  }

  return (
    <Drawer
      title={row ? `Edit row in ${table.name}` : `Insert a row into ${table.name}`}
      onClose={onClose}
      footer={
        <>
          {error && <span className="error grow small-text">{error}</span>}
          <button onClick={onClose}>Cancel</button>
          <button className="primary" disabled={busy} onClick={save}>
            {busy ? 'Saving…' : row ? 'Save' : 'Insert row'}
          </button>
        </>
      }
    >
      {cols.map((c) => {
        const f = fields[c.name]
        const blob = row && isBlob(row[c.name])
        const long = f.value.length > 60 || f.value.includes('\n')
        const placeholder = row ? '' : c.default !== null ? `Default: ${c.default}` : c.pk > 0 && /INT/i.test(c.type) ? 'Assigned automatically' : ''
        return (
          <div key={c.name} className="field">
            <div className="field-label">
              {c.pk > 0 && <KeyRound size={12} className="warn" />}
              <strong>{c.name}</strong>
              <span className="type">{c.type.toLowerCase() || 'any'}</span>
              {c.notNull && <span className="badge">required</span>}
              <span className="grow" />
              {!c.notNull && c.pk === 0 && !blob && (
                <label className="toggle small-text">
                  <input type="checkbox" checked={f.isNull} onChange={(e) => set(c.name, { isNull: e.target.checked })} />
                  NULL
                </label>
              )}
            </div>
            {blob ? (
              <p className="muted small-text">Binary data ({formatBytes((row![c.name] as { $blob: number }).$blob)}) can't be edited here.</p>
            ) : long ? (
              <textarea
                rows={4}
                value={f.value}
                disabled={f.isNull}
                placeholder={placeholder}
                onChange={(e) => set(c.name, { value: e.target.value, isNull: false })}
              />
            ) : (
              <input
                value={f.value}
                disabled={f.isNull}
                placeholder={f.isNull ? 'NULL' : placeholder}
                onChange={(e) => set(c.name, { value: e.target.value, isNull: false })}
                spellCheck={false}
              />
            )}
          </div>
        )
      })}
    </Drawer>
  )
}

const emptyDef = (): ColumnDef => ({ name: '', type: 'TEXT', default: null })

/** Creates a table, column by column. */
export function CreateTablePanel({
  onCreate,
  onClose,
}: {
  onCreate: (table: string, columns: ColumnDef[]) => Promise<void>
  onClose: () => void
}) {
  const [table, setTable] = useState('')
  const [cols, setCols] = useState<ColumnDef[]>([
    { name: 'id', type: 'INTEGER', pk: true },
    { name: 'created_at', type: 'DATETIME', notNull: true, default: 'CURRENT_TIMESTAMP' },
  ])
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const update = (i: number, patch: Partial<ColumnDef>) => setCols(cols.map((c, j) => (j === i ? { ...c, ...patch } : c)))

  async function create() {
    setBusy(true)
    setError(null)
    try {
      await onCreate(table.trim(), cols.filter((c) => c.name.trim()))
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
      setBusy(false)
    }
  }

  return (
    <Drawer
      title="Create a new table"
      onClose={onClose}
      footer={
        <>
          {error && <span className="error grow small-text">{error}</span>}
          <button onClick={onClose}>Cancel</button>
          <button className="primary" disabled={busy || !table.trim()} onClick={create}>
            {busy ? 'Creating…' : 'Create table'}
          </button>
        </>
      }
    >
      <TypeList />
      <label className="field">
        <span>Name</span>
        <input autoFocus value={table} onChange={(e) => setTable(e.target.value)} placeholder="orders" spellCheck={false} />
      </label>
      <div className="field">
        <span>Columns</span>
        <table className="struct">
          <thead>
            <tr>
              <th>Name</th>
              <th>Type</th>
              <th>Default</th>
              <th title="Primary key">Key</th>
              <th title="Not null">Required</th>
              <th>Unique</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {cols.map((c, i) => (
              <tr key={i}>
                <td>
                  <input value={c.name} onChange={(e) => update(i, { name: e.target.value })} spellCheck={false} />
                </td>
                <td style={{ width: 120 }}>
                  <input list="sqlite-types" value={c.type} onChange={(e) => update(i, { type: e.target.value })} />
                </td>
                <td style={{ width: 150 }}>
                  <input
                    value={c.default ?? ''}
                    placeholder="none"
                    onChange={(e) => update(i, { default: e.target.value === '' ? null : e.target.value })}
                  />
                </td>
                <td>
                  <input type="checkbox" checked={!!c.pk} onChange={(e) => update(i, { pk: e.target.checked })} />
                </td>
                <td>
                  <input type="checkbox" checked={!!c.notNull} onChange={(e) => update(i, { notNull: e.target.checked })} />
                </td>
                <td>
                  <input type="checkbox" checked={!!c.unique} onChange={(e) => update(i, { unique: e.target.checked })} />
                </td>
                <td>
                  <button className="icon" onClick={() => setCols(cols.filter((_, j) => j !== i))} aria-label="Remove column">
                    <Trash2 size={14} />
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
        <div>
          <button className="small" onClick={() => setCols([...cols, emptyDef()])}>
            <Plus size={13} /> Add column
          </button>
        </div>
        <small className="muted">
          A single <code>INTEGER</code> key becomes the rowid and numbers itself. Defaults: numbers, <code>NULL</code> and{' '}
          <code>CURRENT_TIMESTAMP</code> as they are; anything else as text.
        </small>
      </div>
    </Drawer>
  )
}

/** Adds a column to an existing table. */
export function AddColumnPanel({
  table,
  onAdd,
  onClose,
}: {
  table: string
  onAdd: (def: ColumnDef) => Promise<void>
  onClose: () => void
}) {
  const [def, setDef] = useState<ColumnDef>(emptyDef())
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  async function add() {
    setBusy(true)
    setError(null)
    try {
      await onAdd(def)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
      setBusy(false)
    }
  }

  return (
    <Drawer
      title={`Add a column to ${table}`}
      onClose={onClose}
      footer={
        <>
          {error && <span className="error grow small-text">{error}</span>}
          <button onClick={onClose}>Cancel</button>
          <button className="primary" disabled={busy || !def.name.trim()} onClick={add}>
            {busy ? 'Adding…' : 'Add column'}
          </button>
        </>
      }
    >
      <TypeList />
      <label className="field">
        <span>Name</span>
        <input autoFocus value={def.name} onChange={(e) => setDef({ ...def, name: e.target.value })} spellCheck={false} />
      </label>
      <label className="field">
        <span>Type</span>
        <input list="sqlite-types" value={def.type} onChange={(e) => setDef({ ...def, type: e.target.value })} />
      </label>
      <label className="field">
        <span>Default value</span>
        <input
          value={def.default ?? ''}
          placeholder="none"
          onChange={(e) => setDef({ ...def, default: e.target.value === '' ? null : e.target.value })}
        />
        <small className="muted">Existing rows get this value. A required column needs one.</small>
      </label>
      <label className="toggle">
        <input type="checkbox" checked={!!def.notNull} onChange={(e) => setDef({ ...def, notNull: e.target.checked })} />
        Required (NOT NULL)
      </label>
    </Drawer>
  )
}

/** Asks for a new name. */
export function RenameDialog({
  what,
  current,
  onRename,
  onClose,
}: {
  what: string
  current: string
  onRename: (name: string) => Promise<void>
  onClose: () => void
}) {
  const [name, setName] = useState(current)
  const [error, setError] = useState<string | null>(null)
  return (
    <Dialog
      onClose={onClose}
      footer={
        <>
          <button onClick={onClose}>Cancel</button>
          <button
            className="primary"
            disabled={!name.trim() || name === current}
            onClick={() => onRename(name.trim()).catch((e) => setError(e instanceof Error ? e.message : String(e)))}
          >
            Rename
          </button>
        </>
      }
    >
      <h3>Rename {what}</h3>
      <input autoFocus value={name} onChange={(e) => setName(e.target.value)} spellCheck={false} />
      {error && <p className="error small-text">{error}</p>}
    </Dialog>
  )
}

/** Confirms a destructive change; with typeToConfirm, the user types it first. */
export function ConfirmDialog({
  title,
  body,
  action,
  typeToConfirm,
  onConfirm,
  onClose,
}: {
  title: string
  body: React.ReactNode
  action: string
  typeToConfirm?: string
  onConfirm: () => Promise<void>
  onClose: () => void
}) {
  const [typed, setTyped] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  return (
    <Dialog
      onClose={onClose}
      footer={
        <>
          <button onClick={onClose}>Cancel</button>
          <button
            className="danger"
            disabled={busy || (typeToConfirm !== undefined && typed !== typeToConfirm)}
            onClick={() => {
              setBusy(true)
              onConfirm().catch((e) => {
                setError(e instanceof Error ? e.message : String(e))
                setBusy(false)
              })
            }}
          >
            {action}
          </button>
        </>
      }
    >
      <h3>{title}</h3>
      <div className="muted">{body}</div>
      {typeToConfirm !== undefined && (
        <label className="field">
          <span>
            Type <code>{typeToConfirm}</code> to confirm
          </span>
          <input autoFocus value={typed} onChange={(e) => setTyped(e.target.value)} />
        </label>
      )}
      {error && <p className="error small-text">{error}</p>}
    </Dialog>
  )
}
