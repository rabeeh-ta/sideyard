import { useEffect, useRef, useState, type ReactNode } from 'react'
import { X } from 'lucide-react'
import type { Release, RuntimeState, RuntimeStatus } from './api'

const stateLabel: Record<RuntimeState, string> = {
  running: 'Running',
  crashed: 'Crashed',
  stopped: 'Stopped',
  'not-deployed': 'Not deployed',
}

export function StatusBadge({ state, health }: { state: RuntimeState; health?: RuntimeStatus['health'] }) {
  if (state === 'running' && health?.status === 'unhealthy') {
    return (
      <span className="badge state-unhealthy" title={health.error}>
        <span className="dot" />
        Unhealthy
      </span>
    )
  }
  return (
    <span className={`badge state-${state}`}>
      <span className="dot" />
      {stateLabel[state]}
    </span>
  )
}

/** A small coloured label, e.g. a run's status. */
export function Pill({ tone, children }: { tone: 'green' | 'amber' | 'red' | 'grey'; children: ReactNode }) {
  return <span className={`badge tone-${tone}`}>{children}</span>
}

/** Asks for a second click before doing something destructive. */
export function ConfirmButton({
  label,
  confirmLabel = 'Confirm',
  onConfirm,
  disabled,
}: {
  label: string
  confirmLabel?: string
  onConfirm: () => void
  disabled?: boolean
}) {
  const [armed, setArmed] = useState(false)
  if (!armed) {
    return (
      <button className="ghost small" disabled={disabled} onClick={() => setArmed(true)}>
        {label}
      </button>
    )
  }
  return (
    <span className="confirm">
      <button
        className="danger small"
        disabled={disabled}
        onClick={() => {
          setArmed(false)
          onConfirm()
        }}
      >
        {confirmLabel}
      </button>
      <button className="ghost small" onClick={() => setArmed(false)}>
        Cancel
      </button>
    </span>
  )
}

export function ReleaseBadge({ status }: { status: Release['status'] }) {
  return <span className={`badge release-${status}`}>{status}</span>
}

export function CopyButton({ text, label = 'Copy' }: { text: string; label?: string }) {
  const [copied, setCopied] = useState(false)
  return (
    <button
      className="ghost small"
      onClick={() => {
        navigator.clipboard.writeText(text)
        setCopied(true)
        setTimeout(() => setCopied(false), 1500)
      }}
    >
      {copied ? 'Copied' : label}
    </button>
  )
}

export function CodeBlock({ children }: { children: string }) {
  return (
    <div className="codeblock">
      <pre>{children}</pre>
      <CopyButton text={children} />
    </div>
  )
}

export function Section({
  title,
  description,
  children,
  id,
}: {
  title: string
  description?: ReactNode
  children: ReactNode
  id?: string
}) {
  return (
    <section className="card section" id={id}>
      <h3>{title}</h3>
      {description && <p className="muted">{description}</p>}
      {children}
    </section>
  )
}

/** A panel that slides in from the right, like Supabase's side panels. */
export function Drawer({
  title,
  onClose,
  children,
  footer,
}: {
  title: ReactNode
  onClose: () => void
  children: ReactNode
  footer?: ReactNode
}) {
  useEscape(onClose)
  return (
    <>
      <div className="overlay" onClick={onClose} />
      <aside className="drawer" role="dialog" aria-modal="true">
        <div className="drawer-head">
          <h3>{title}</h3>
          <button className="icon" onClick={onClose} aria-label="Close">
            <X size={16} />
          </button>
        </div>
        <div className="drawer-body">{children}</div>
        {footer && <div className="drawer-foot">{footer}</div>}
      </aside>
    </>
  )
}

/** A centred confirmation dialog. */
export function Dialog({ onClose, children, footer }: { onClose: () => void; children: ReactNode; footer: ReactNode }) {
  useEscape(onClose)
  return (
    <>
      <div className="overlay" onClick={onClose} />
      <div className="dialog" role="dialog" aria-modal="true">
        <div className="dialog-body">{children}</div>
        <div className="dialog-foot">{footer}</div>
      </div>
    </>
  )
}

function useEscape(onClose: () => void) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => e.key === 'Escape' && onClose()
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])
}

/** A button that opens a dropdown menu; clicking outside closes it. */
export function Menu({
  trigger,
  children,
  align = 'left',
}: {
  trigger: (open: boolean) => ReactNode
  children: (close: () => void) => ReactNode
  align?: 'left' | 'right'
}) {
  const [open, setOpen] = useState(false)
  const ref = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (!open) return
    const onDown = (e: MouseEvent) => ref.current && !ref.current.contains(e.target as Node) && setOpen(false)
    document.addEventListener('mousedown', onDown)
    return () => document.removeEventListener('mousedown', onDown)
  }, [open])
  return (
    <div className="switcher" ref={ref}>
      <span onClick={() => setOpen(!open)}>{trigger(open)}</span>
      {open && <div className={`menu ${align === 'right' ? 'right' : ''}`}>{children(() => setOpen(false))}</div>}
    </div>
  )
}

export function Logo({ size = 18 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 16 16" aria-hidden>
      <rect x="1" y="1" width="6" height="6" rx="1.5" fill="var(--brand)" />
      <rect x="9" y="1" width="6" height="6" rx="1.5" fill="currentColor" opacity=".35" />
      <rect x="1" y="9" width="6" height="6" rx="1.5" fill="currentColor" opacity=".35" />
      <rect x="9" y="9" width="6" height="6" rx="1.5" fill="currentColor" opacity=".35" />
    </svg>
  )
}

/** Page title, description and actions. */
export function PageHead({ title, description, children }: { title: ReactNode; description?: ReactNode; children?: ReactNode }) {
  return (
    <div className="page-head">
      <div>
        <h1>{title}</h1>
        {description && <p>{description}</p>}
      </div>
      {children && <div className="actions">{children}</div>}
    </div>
  )
}

/** A headline figure on a card, with a line of context and an optional meter (0–1). */
export function Stat({ label, value, note, meter }: { label: string; value: string; note?: ReactNode; meter?: number }) {
  return (
    <div className="card stat">
      <span>{label}</span>
      <strong>{value}</strong>
      {meter !== undefined && (
        <div className={`progress ${meter >= 0.9 ? 'high' : ''}`} role="meter" aria-valuenow={Math.round(meter * 100)} aria-valuemin={0} aria-valuemax={100}>
          <div style={{ width: `${Math.min(100, meter * 100)}%` }} />
        </div>
      )}
      {note && <small className="ellipsis">{note}</small>}
    </div>
  )
}
