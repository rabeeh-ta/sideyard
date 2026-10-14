import { useState } from 'react'
import type { RenderEditCellProps, RenderHeaderCellProps } from 'react-data-grid'
import { ArrowDown, ArrowUp, KeyRound } from 'lucide-react'
import type { CellValue, Column } from './api'
import { formatBytes } from './lib'

/** A row as the grid holds it: column values plus its key and position. */
export type GridRow = { __k: string; __i: number } & Record<string, CellValue | string | number>

export function isBlob(v: unknown): v is { $blob: number } {
  return typeof v === 'object' && v !== null && '$blob' in v
}

/** How a stored value looks in a cell: NULL and blobs are marked. */
export function CellView({ value }: { value: unknown }) {
  if (value === null || value === undefined) return <span className="cell-null">NULL</span>
  if (isBlob(value)) return <span className="cell-blob">blob · {formatBytes(value.$blob) || '0 B'}</span>
  if (typeof value === 'number') return <span className="cell-num">{value}</span>
  return <>{String(value)}</>
}

/** The text a value is edited as. */
export function editText(v: unknown): string {
  return v === null || v === undefined ? '' : String(v)
}

/** Inline cell editor: Enter saves, Escape cancels, leaving the cell saves. */
export function CellEditor({ row, column, onRowChange, onClose }: RenderEditCellProps<GridRow>) {
  const [value, setValue] = useState(editText(row[column.key]))
  return (
    <input
      className="cell-editor"
      autoFocus
      value={value}
      onFocus={(e) => e.target.select()}
      onChange={(e) => setValue(e.target.value)}
      onKeyDown={(e) => {
        if (e.key === 'Enter') {
          onRowChange({ ...row, [column.key]: value }, true)
        } else if (e.key === 'Escape') {
          onClose(false)
        }
      }}
      onBlur={() => onRowChange({ ...row, [column.key]: value }, true)}
    />
  )
}

/** Column header: key marker, name, type, sort direction. */
export function ColumnHeader({ col, sortDirection }: { col: Pick<Column, 'name' | 'type' | 'pk'> } & Pick<RenderHeaderCellProps<GridRow>, 'sortDirection'>) {
  return (
    <div className="col-head" title={`${col.name} ${col.type}`}>
      {col.pk > 0 && <KeyRound size={12} className="pk" />}
      <span className="name">{col.name}</span>
      {col.type && <span className="type">{col.type.toLowerCase()}</span>}
      {sortDirection === 'ASC' && <ArrowUp size={12} />}
      {sortDirection === 'DESC' && <ArrowDown size={12} />}
    </div>
  )
}

/** A starting width for a column, from its header and a sample of values. */
export function columnWidth(header: string, values: unknown[]): number {
  let longest = header.length + 2
  for (const v of values.slice(0, 60)) {
    const n = v === null || v === undefined ? 4 : isBlob(v) ? 12 : String(v).length
    if (n > longest) longest = n
  }
  return Math.min(Math.max(Math.round(longest * 7.4 + 32), 90), 360)
}
