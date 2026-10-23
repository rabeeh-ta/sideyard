import { useCallback, useEffect, useRef, useState } from 'react'
import { AlertCircle, ArrowUpCircle, CheckCircle2, CircleDashed, RefreshCw } from 'lucide-react'
import { ApiError, api, errorMessage, type UpdateStatus } from './api'
import { useServer } from './App'
import { formatDate, timeAgo } from './lib'

/** How long a restart into a new version may take before we say so. */
const restartPatience = 120_000

/**
 * The Updates section of Platform settings: the running version, the newest
 * release, and installing it. An install restarts Sideyard; this follows it
 * through the restart and reloads the page once the new version answers.
 */
export function UpdatesSection({ focus }: { focus: boolean }) {
  const server = useServer()
  const [st, setSt] = useState<UpdateStatus | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState<'check' | 'install' | null>(null)
  // The version an install is restarting away from. unsure: the request to
  // start it got no answer, so it may not have reached Sideyard.
  const [leaving, setLeaving] = useState<{ from: string; since: number; unsure?: boolean } | null>(null)
  const [slow, setSlow] = useState(false)
  const ref = useRef<HTMLElement>(null)

  const load = useCallback(async () => {
    try {
      setSt(await api.update())
      setError(null)
    } catch (e) {
      setError(errorMessage(e))
    }
  }, [])

  useEffect(() => {
    load()
  }, [load])

  useEffect(() => {
    if (focus) ref.current?.scrollIntoView({ block: 'start' })
  }, [focus, st !== null])

  useEffect(() => {
    if (!leaving) return
    const t = setInterval(async () => {
      try {
        const s = await api.update()
        setSt(s)
        if (s.current !== leaving.from) {
          window.location.reload() // the new version is up, with its own dashboard
        } else if (s.state === 'idle') {
          setLeaving(null) // the install failed, and s.error says why, or it never started
          if (leaving.unsure && !s.error) setError("The install didn't start: the connection dropped. Try again.")
        }
      } catch {
        // Sideyard is restarting
      }
      setSlow(Date.now() - leaving.since > restartPatience)
    }, 1500)
    return () => clearInterval(t)
  }, [leaving])

  async function check() {
    setBusy('check')
    setError(null)
    try {
      setSt(await api.checkUpdate())
    } catch (e) {
      setError(errorMessage(e))
    } finally {
      setBusy(null)
    }
  }

  async function install() {
    if (!st?.latest) return
    const from = st.current
    setBusy('install')
    setError(null)
    try {
      setSt(await api.installUpdate(st.latest))
      setLeaving({ from, since: Date.now() })
    } catch (e) {
      // No answer at all: the restart may have cut it off, so follow the
      // install as if it started; the status says whether it did.
      if (e instanceof ApiError) setError(errorMessage(e))
      else setLeaving({ from, since: Date.now(), unsure: true })
    } finally {
      setBusy(null)
    }
  }

  async function setAuto(on: boolean) {
    try {
      setSt(await api.saveUpdateSettings(on))
    } catch (e) {
      setError(errorMessage(e))
    }
  }

  if (!st) {
    return (
      <section ref={ref} className="card section" id="updates">
        <h3>Updates</h3>
        {error && <p className="error">{error}</p>}
      </section>
    )
  }

  const inFlight = st.state === 'installing' || st.state === 'restarting' || leaving !== null
  return (
    <section ref={ref} className="card section" id="updates">
      <h3>Updates</h3>
      <p className="muted">
        {st.source ? (
          <>
            Sideyard looks for new releases at <code>{st.source.replace(/^https?:\/\//, '')}</code> every six hours.
          </>
        ) : (
          <>
            This build doesn't look for updates: it was built locally. Installs from a release (with the install script) do.
          </>
        )}
      </p>

      <div className="update-box">
        <div className="update-version">
          <span className="muted small-text">Running</span>
          <strong>{st.current}</strong>
          {st.last && (
            <span className="muted small-text">
              updated from {st.last.from} {timeAgo(st.last.at)}
            </span>
          )}
        </div>
        <div className="update-state">
          {inFlight ? (
            <div className="update-progress">
              <RefreshCw size={16} className="spin" />
              <div>
                <strong>{st.state === 'installing' ? `Installing ${st.latest}…` : `Restarting into ${st.latest}…`}</strong>
                <div className="muted small-text">
                  {st.state === 'installing'
                    ? 'Downloading it and checking its checksum.'
                    : 'This page reloads when Sideyard is back, usually within a few seconds.'}
                </div>
                {slow && (
                  <div className="error small-text">
                    Sideyard hasn't come back yet. On the server, <code>journalctl -u sideyard -n 50</code> shows why; the previous
                    binary is kept next to the new one as <code>sideyard.prev</code>.
                  </div>
                )}
              </div>
            </div>
          ) : st.available ? (
            <div className="update-available">
              <ArrowUpCircle size={18} className="brand" />
              <div className="grow">
                <strong>{st.latest} is available</strong>
                <div className="muted small-text">
                  {st.published ? `Published ${formatDate(st.published)}. ` : ''}
                  {server?.runner === 'systemd'
                    ? 'Sideyard restarts into it; apps keep running, and the dashboard and new connections pause for a few seconds.'
                    : 'Sideyard restarts into it, and with the process runner the apps restart too.'}
                </div>
              </div>
              <button className="primary" disabled={busy !== null} onClick={install}>
                {busy === 'install' ? 'Starting…' : `Install ${st.latest}`}
              </button>
            </div>
          ) : st.source ? (
            <div className="update-available">
              {st.latest ? (
                <CheckCircle2 size={18} className="brand" />
              ) : st.checkedAt ? (
                <AlertCircle size={18} className="error" />
              ) : (
                <CircleDashed size={18} className="faint" />
              )}
              <div className="grow">
                <strong>{st.latest ? 'Up to date' : st.checkedAt ? "Couldn't check" : 'Not checked yet'}</strong>
                <div className="muted small-text">
                  {st.checkedAt ? `Checked ${timeAgo(st.checkedAt)}` : 'Sideyard checks a minute after it starts.'}
                  {st.latest && st.latest !== st.current && ` · newest release is ${st.latest}`}
                </div>
              </div>
              <button disabled={busy !== null || st.state === 'checking'} onClick={check}>
                {busy === 'check' || st.state === 'checking' ? 'Checking…' : 'Check now'}
              </button>
            </div>
          ) : null}
        </div>
      </div>

      {st.rolledBack && (
        <p className="error">
          {st.rolledBack.to} didn't start, so Sideyard went back to {st.rolledBack.from} {timeAgo(st.rolledBack.at)}.
          {st.latest === st.rolledBack.to && ' Automatic updates skip it; installing it again here is up to you.'} The
          server's log has the reason: <code>journalctl -u sideyard</code>.
        </p>
      )}
      {(error || st.error) && <p className="error">{error || st.error}</p>}

      {st.source && (
        <label className="toggle" style={{ marginTop: 14 }}>
          <input type="checkbox" checked={st.autoInstall} onChange={(e) => setAuto(e.target.checked)} disabled={inFlight} />
          Install new releases automatically, when a check finds one
        </label>
      )}
    </section>
  )
}
