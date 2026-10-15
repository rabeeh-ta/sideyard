import { useState, type FormEvent } from 'react'
import { api, errorMessage, type Job, type JobRun, type JobSpec } from './api'
import { duration, formatDate, timeAgo, timeUntil, usePoll } from './lib'
import { CodeBlock, ConfirmButton, Drawer, Pill, Section } from './ui'

const presets = [
  ['Every 15 minutes', '*/15 * * * *'],
  ['Every hour', '0 * * * *'],
  ['Daily at 09:00 UTC', '0 9 * * *'],
  ['Mondays at 08:00 UTC', '0 8 * * 1'],
] as const

const runTone = { succeeded: 'green', failed: 'red', running: 'amber', skipped: 'grey' } as const

export function JobsTab({ name }: { name: string }) {
  const { data: jobs, error, reload } = usePoll(() => api.jobs(name), 5000, name)
  const [editing, setEditing] = useState<Job | 'new' | null>(null)
  const [history, setHistory] = useState<string | null>(null)
  const [howTo, setHowTo] = useState(false)
  const [actionError, setActionError] = useState<string | null>(null)

  async function act(fn: () => Promise<unknown>) {
    setActionError(null)
    try {
      await fn()
    } catch (e) {
      setActionError(errorMessage(e))
    } finally {
      reload()
    }
  }

  return (
    <>
      {error && <p className="error">{error}</p>}
      {actionError && <p className="error">{actionError}</p>}
      {editing && (
        <JobForm
          name={name}
          job={editing === 'new' ? null : editing}
          onClose={() => setEditing(null)}
          onDone={() => {
            setEditing(null)
            reload()
          }}
        />
      )}

      <div className="page-head" style={{ marginBottom: 10 }}>
        <span className="muted">
          Each job is a <code>POST /_jobs/&lt;name&gt;</code> to the app. Times are UTC unless the schedule starts with{' '}
          <code>CRON_TZ=</code>.
        </span>
        <button className="primary" onClick={() => setEditing('new')}>
          New job
        </button>
      </div>

      {jobs && jobs.length === 0 && <HowTo />}
      {jobs && jobs.length > 0 && (
        <div className="card list">
          {jobs.map((j) => (
            <div key={j.name}>
              <div className="job-row">
                <div style={{ minWidth: 0 }}>
                  <strong>{j.name}</strong>
                  {!j.enabled && (
                    <>
                      {' '}
                      <Pill tone="grey">paused</Pill>
                    </>
                  )}
                  <div className="muted small-text ellipsis" title={j.schedule}>
                    <code>{j.schedule}</code> {describeSchedule(j.schedule)}
                  </div>
                </div>
                <div className="small-text">
                  {j.running ? (
                    <Pill tone="amber">running</Pill>
                  ) : j.lastRun ? (
                    <>
                      <Pill tone={runTone[j.lastRun.status]}>{j.lastRun.status}</Pill>{' '}
                      <span className="muted">{timeAgo(j.lastRun.startedAt)}</span>
                    </>
                  ) : (
                    <span className="muted">never run</span>
                  )}
                </div>
                <div className="muted small-text">
                  <div>{j.enabled ? `next ${timeUntil(j.nextRun)}` : 'paused'}</div>
                  <div className="faint">
                    timeout {j.timeoutSec}s{j.retries > 0 && ` · ${j.retries} retr${j.retries === 1 ? 'y' : 'ies'}`}
                  </div>
                </div>
                <div className="row-actions">
                  <button className="small" disabled={j.running} onClick={() => act(() => api.runJob(name, j.name))}>
                    Run now
                  </button>
                  <button className="ghost small" onClick={() => setHistory(history === j.name ? null : j.name)}>
                    {history === j.name ? 'Hide runs' : 'Runs'}
                  </button>
                  <button className="ghost small" onClick={() => setEditing(j)}>
                    Edit
                  </button>
                </div>
              </div>
              {history === j.name && <Runs name={name} job={j.name} />}
            </div>
          ))}
        </div>
      )}
      {jobs &&
        jobs.length > 0 &&
        (howTo ? (
          <HowTo />
        ) : (
          <button className="link-button small-text" onClick={() => setHowTo(true)}>
            How does the app handle a job?
          </button>
        ))}
    </>
  )
}

/** Creates or edits a job in a side panel; editing also offers Delete. */
function JobForm({ name, job, onClose, onDone }: { name: string; job: Job | null; onClose: () => void; onDone: () => void }) {
  const [spec, setSpec] = useState<JobSpec>(
    job ?? { name: '', schedule: '0 * * * *', timeoutSec: 300, retries: 0, enabled: true },
  )
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  async function run(fn: () => Promise<unknown>) {
    setBusy(true)
    setError(null)
    try {
      await fn()
      onDone()
    } catch (err) {
      setError(errorMessage(err))
      setBusy(false)
    }
  }

  function submit(e?: FormEvent) {
    e?.preventDefault()
    run(() => (job ? api.updateJob(name, job.name, spec) : api.createJob(name, spec)))
  }

  return (
    <Drawer
      title={job ? `Edit ${job.name}` : 'New job'}
      onClose={onClose}
      footer={
        <>
          {job && <ConfirmButton label="Delete job" onConfirm={() => run(() => api.deleteJob(name, job.name))} />}
          {error ? <span className="error grow small-text">{error}</span> : <span className="grow" />}
          <button onClick={onClose}>Cancel</button>
          <button className="primary" disabled={busy || !spec.name.trim()} onClick={() => submit()}>
            {busy ? 'Saving…' : job ? 'Save' : 'Create job'}
          </button>
        </>
      }
    >
      <form className="stack" style={{ marginTop: 0 }} onSubmit={submit}>
        <label className="field">
          <span>Name</span>
          <input
            value={spec.name}
            disabled={!!job}
            autoFocus={!job}
            placeholder="daily-report"
            onChange={(e) => setSpec({ ...spec, name: e.target.value })}
            spellCheck={false}
          />
          <small className="muted">
            Called at <code>POST /_jobs/{spec.name || '<name>'}</code>
          </small>
        </label>
        <label className="field">
          <span>Schedule</span>
          <input value={spec.schedule} onChange={(e) => setSpec({ ...spec, schedule: e.target.value })} spellCheck={false} />
          <small className="muted">
            {describeSchedule(spec.schedule) || 'Five cron fields: minute hour day month weekday.'}
          </small>
          <div className="chips">
            {presets.map(([label, cron]) => (
              <button
                type="button"
                key={cron}
                className={`chip ${spec.schedule === cron ? 'on' : ''}`}
                onClick={() => setSpec({ ...spec, schedule: cron })}
              >
                {label}
              </button>
            ))}
          </div>
        </label>
        <div className="grid2">
          <label className="field">
            <span>Timeout (seconds)</span>
            <input
              type="number"
              min={1}
              max={3600}
              value={spec.timeoutSec}
              onChange={(e) => setSpec({ ...spec, timeoutSec: Number(e.target.value) })}
            />
          </label>
          <label className="field">
            <span>Retries on failure</span>
            <input
              type="number"
              min={0}
              max={5}
              value={spec.retries}
              onChange={(e) => setSpec({ ...spec, retries: Number(e.target.value) })}
            />
          </label>
        </div>
        <label className="toggle">
          <input type="checkbox" checked={spec.enabled} onChange={(e) => setSpec({ ...spec, enabled: e.target.checked })} />
          Enabled
        </label>
      </form>
    </Drawer>
  )
}

const weekdays = ['Sundays', 'Mondays', 'Tuesdays', 'Wednesdays', 'Thursdays', 'Fridays', 'Saturdays']

/** The common cron shapes in words: "every 15 minutes", "daily at 03:00 UTC". */
export function describeSchedule(cron: string): string {
  let spec = cron.trim()
  let zone = 'UTC'
  const tz = spec.match(/^CRON_TZ=(\S+)\s+(.*)$/)
  if (tz) [, zone, spec] = tz
  const named: Record<string, string> = {
    '@hourly': 'every hour',
    '@daily': `daily at 00:00 ${zone}`,
    '@midnight': `daily at 00:00 ${zone}`,
    '@weekly': `Sundays at 00:00 ${zone}`,
    '@monthly': `on the 1st at 00:00 ${zone}`,
  }
  if (named[spec]) return named[spec]
  const every = spec.match(/^@every\s+(\S+)$/)
  if (every) return `every ${every[1]}`
  const f = spec.split(/\s+/)
  if (f.length !== 5) return ''
  const [min, hour, dom, mon, dow] = f
  const num = (v: string) => /^\d+$/.test(v)
  const at = () => `${hour.padStart(2, '0')}:${min.padStart(2, '0')} ${zone}`
  if (dom !== '*' || mon !== '*') return num(dom) && mon === '*' && dow === '*' && num(min) && num(hour) ? `monthly on day ${dom} at ${at()}` : ''
  const step = min.match(/^\*\/(\d+)$/)
  if (step && hour === '*' && dow === '*') return `every ${step[1]} minutes`
  if (min === '*' && hour === '*' && dow === '*') return 'every minute'
  if (num(min) && hour === '*' && dow === '*') return min === '0' ? 'every hour' : `hourly at :${min.padStart(2, '0')}`
  if (num(min) && num(hour)) {
    if (dow === '*') return `daily at ${at()}`
    if (num(dow) && Number(dow) <= 7) return `${weekdays[Number(dow) % 7]} at ${at()}`
    if (dow === '1-5') return `weekdays at ${at()}`
  }
  return ''
}

function Runs({ name, job }: { name: string; job: string }) {
  const { data: runs } = usePoll(() => api.jobRuns(name, job), 3000, `${name}:${job}`)
  const [open, setOpen] = useState<number | null>(null)
  if (!runs) return null
  if (runs.length === 0) return <p className="muted run-output">No runs yet.</p>
  return (
    <div>
      {runs.map((r: JobRun) => (
        <div key={r.id}>
          <div className="run-row">
            <Pill tone={runTone[r.status]}>{r.status}</Pill>
            <span className="muted">{r.trigger}</span>
            <span className="muted" title={`scheduled for ${formatDate(r.scheduledAt)}`}>
              {formatDate(r.startedAt)}
            </span>
            <span className="muted num">{duration(r.startedAt, r.finishedAt)}</span>
            <span className="muted num">{r.httpStatus || ''}</span>
            <div className="row-actions">
              {r.output && (
                <button className="ghost small" onClick={() => setOpen(open === r.id ? null : r.id)}>
                  {open === r.id ? 'Hide output' : 'Output'}
                </button>
              )}
            </div>
          </div>
          {open === r.id && <pre className="run-output">{r.output}</pre>}
          {open === r.id && r.httpStatus === 404 && (
            <p className="muted small-text run-hint">
              The app has no <code>POST /_jobs/{job}</code> route, or it isn't deployed yet.
            </p>
          )}
        </div>
      ))}
    </div>
  )
}

function HowTo() {
  return (
    <Section
      title="Handling a job in your app"
      description="Only Sideyard reaches /_jobs/, and each call carries the app's SIDEYARD_TOKEN. A 2xx marks the run succeeded; the body becomes its output."
    >
      <CodeBlock>{`mux.HandleFunc("POST /_jobs/daily-report", func(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+os.Getenv("SIDEYARD_TOKEN") {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	n, err := sendDailyReport(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError) // the run fails
		return
	}
	fmt.Fprintf(w, "sent %d reports", n) // shown as the run's output
})`}</CodeBlock>
    </Section>
  )
}
