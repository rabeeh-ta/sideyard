import { useCallback, useEffect, useRef, useState } from 'react'
import { errorMessage } from './api'

/** Runs fn now and every ms milliseconds; reload() re-runs it on demand. */
export function usePoll<T>(fn: () => Promise<T>, ms: number, key: string) {
  const [data, setData] = useState<T | null>(null)
  const [error, setError] = useState<string | null>(null)
  const fnRef = useRef(fn)
  fnRef.current = fn

  const reload = useCallback(async () => {
    try {
      setData(await fnRef.current())
      setError(null)
    } catch (e) {
      setError(errorMessage(e))
    }
  }, [])

  useEffect(() => {
    setData(null)
    reload()
    if (ms <= 0) return
    const t = setInterval(reload, ms)
    return () => clearInterval(t)
  }, [reload, ms, key])

  return { data, error, reload }
}

/** The hash route split into segments: "#/p/notes/logs" → ["p", "notes", "logs"]. */
export function useRoute(): string[] {
  const [hash, setHash] = useState(window.location.hash)
  useEffect(() => {
    const onChange = () => setHash(window.location.hash)
    window.addEventListener('hashchange', onChange)
    return () => window.removeEventListener('hashchange', onChange)
  }, [])
  return hash.replace(/^#\/?/, '').split('/').filter(Boolean).map(decodeURIComponent)
}

export function go(path: string) {
  window.location.hash = path
}

export function formatBytes(n: number): string {
  if (!n) return '—'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let i = 0
  while (n >= 1024 && i < units.length - 1) {
    n /= 1024
    i++
  }
  return `${n.toFixed(n < 10 && i > 0 ? 1 : 0)} ${units[i]}`
}

export function timeAgo(iso?: string): string {
  if (!iso) return '—'
  const s = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000)
  if (s < 60) return 'just now'
  if (s < 3600) return `${Math.floor(s / 60)}m ago`
  if (s < 86400) return `${Math.floor(s / 3600)}h ago`
  return `${Math.floor(s / 86400)}d ago`
}

export function uptime(iso?: string): string {
  if (!iso) return '—'
  const s = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000)
  if (s < 60) return `${Math.floor(s)}s`
  if (s < 3600) return `${Math.floor(s / 60)}m`
  if (s < 86400) return `${Math.floor(s / 3600)}h ${Math.floor((s % 3600) / 60)}m`
  return `${Math.floor(s / 86400)}d ${Math.floor((s % 86400) / 3600)}h`
}

/** Parses .env-style text: KEY=value lines, # comments, optional export and quotes. */
export function parseEnv(text: string): Record<string, string> {
  const out: Record<string, string> = {}
  for (const raw of text.split('\n')) {
    const line = raw.trim().replace(/^export\s+/, '')
    if (!line || line.startsWith('#')) continue
    const eq = line.indexOf('=')
    if (eq <= 0) continue
    let value = line.slice(eq + 1).trim()
    if (value.length >= 2 && (value[0] === '"' || value[0] === "'") && value.endsWith(value[0])) {
      value = value.slice(1, -1)
    }
    out[line.slice(0, eq).trim()] = value
  }
  return out
}

/** The public URL of a host: HTTPS in production, the dev listener's port in dev mode. */
/**
 * An app's address. Apps are served by the same proxy as this dashboard, so
 * they're reached the way the dashboard was: https on a server, the dev
 * port locally, the forwarded port through a tunnel.
 */
export function appURL(host: string): string {
  const { protocol, port } = window.location
  return `${protocol}//${host}${port ? `:${port}` : ''}`
}

export function timeUntil(iso?: string): string {
  if (!iso) return '—'
  const s = (new Date(iso).getTime() - Date.now()) / 1000
  if (s <= 60) return 'within a minute'
  if (s < 3600) return `in ${Math.round(s / 60)}m`
  if (s < 86400) return `in ${Math.floor(s / 3600)}h ${Math.round((s % 3600) / 60)}m`
  return `in ${Math.floor(s / 86400)}d ${Math.round((s % 86400) / 3600)}h`
}

export function duration(fromIso: string, toIso?: string): string {
  if (!toIso) return '…'
  // Run times are stored to the second.
  const ms = new Date(toIso).getTime() - new Date(fromIso).getTime()
  if (ms < 1000) return '<1s'
  if (ms < 60000) return `${(ms / 1000).toFixed(1)}s`
  return `${Math.floor(ms / 60000)}m ${Math.round((ms % 60000) / 1000)}s`
}

export function formatDate(iso?: string): string {
  if (!iso) return '—'
  return new Date(iso).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' })
}

/** Saves a Blob as a file download. */
export function saveBlob(blob: Blob, filename: string) {
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  a.click()
  URL.revokeObjectURL(url)
}

export type Theme = 'dark' | 'light'

const themeKey = 'sideyard:theme'

export function storedTheme(): Theme {
  return localStorage.getItem(themeKey) === 'light' ? 'light' : 'dark'
}

export function applyTheme(t: Theme) {
  document.documentElement.dataset.theme = t
  localStorage.setItem(themeKey, t)
}

/** The colour theme, dark unless the user picked light. */
export function useTheme(): [Theme, () => void] {
  const [theme, setTheme] = useState<Theme>(storedTheme)
  return [
    theme,
    () => {
      const next = theme === 'dark' ? 'light' : 'dark'
      applyTheme(next)
      setTheme(next)
    },
  ]
}
