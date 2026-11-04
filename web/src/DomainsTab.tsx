import { useState } from 'react'
import { api, errorMessage, type DomainStatus, type Project } from './api'
import { useServer } from './App'
import { usePoll } from './lib'
import { Dialog, Pill, Section } from './ui'

export function DomainsTab({ project, onChange }: { project: Project; onChange: () => void }) {
  const server = useServer()
  const dev = !!server?.dev
  const [host, setHost] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [removing, setRemoving] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const { data: statuses, reload } = usePoll(() => api.domainStatus(project.name), 0, project.domains.join(','))

  async function run(fn: () => Promise<unknown>) {
    setError(null)
    setBusy(true)
    try {
      await fn()
      return true
    } catch (e) {
      setError(errorMessage(e))
      return false
    } finally {
      setBusy(false)
      onChange()
      reload()
    }
  }

  async function remove(d: string) {
    await run(() => api.removeDomain(project.name, d))
    setRemoving(null)
  }

  return (
    <>
      <Section
        title="Domains"
        description={
          dev
            ? 'Dev mode: *.localhost names resolve to this machine in most browsers.'
            : 'Add a domain, then create the DNS record shown for it.'
        }
      >
        <div className="kv">
          {project.domains.map((d) => (
            <div key={d} className="kv-row domain">
              <div>
                <code className="kv-key">{d}</code>
                <DomainChecks status={statuses?.find((x) => x.host === d)} dev={dev} />
              </div>
              <div className="row-actions">
                <button className="ghost small" disabled={busy} onClick={() => setRemoving(d)}>
                  Remove
                </button>
              </div>
            </div>
          ))}
        </div>
        <form
          className="inline-form"
          onSubmit={async (e) => {
            e.preventDefault()
            if (await run(() => api.addDomain(project.name, host))) setHost('')
          }}
        >
          <input className="grow" placeholder="app.example.com" value={host} onChange={(e) => setHost(e.target.value)} />
          <button className="ghost" disabled={!host.trim() || busy}>
            Add domain
          </button>
        </form>
        {error && <p className="error">{error}</p>}
      </Section>

      {removing && (
        <Dialog
          onClose={() => !busy && setRemoving(null)}
          footer={
            <>
              <button disabled={busy} onClick={() => setRemoving(null)}>
                Cancel
              </button>
              <button className="danger" disabled={busy} onClick={() => remove(removing)}>
                {busy ? 'Removing…' : `Remove ${removing}`}
              </button>
            </>
          }
        >
          <h3>Remove {removing}?</h3>
          <p className="muted">
            {project.name} stops answering at {removing} right away. The app keeps running, and the domain's DNS records
            stay as they are.
          </p>
        </Dialog>
      )}
    </>
  )
}

/** Whether a domain resolves to this server and has a certificate, and the record to create when it doesn't. */
function DomainChecks({ status, dev }: { status?: DomainStatus; dev: boolean }) {
  if (!status) return null
  const cert = status.certificate
  // Caddy renews with a third of a certificate's lifetime left (30 of 90
  // days, 4 of 12 hours for its own CA); a quarter left means it's late.
  const left = cert ? new Date(cert.notAfter).getTime() - Date.now() : 0
  const late = cert ? left < (new Date(cert.notAfter).getTime() - new Date(cert.notBefore).getTime()) / 4 : false
  const elsewhere = status.pointsHere === false
  return (
    <>
      <div className="domain-status" style={{ marginTop: 6 }}>
        {status.dnsError ? (
          <Pill tone="red">DNS: {status.dnsError}</Pill>
        ) : elsewhere ? (
          <Pill tone="red">DNS points elsewhere: {status.addresses.join(', ')}</Pill>
        ) : status.pointsHere ? (
          <Pill tone="green">DNS points here</Pill>
        ) : (
          <Pill tone="grey">DNS: {status.addresses.join(', ')}</Pill>
        )}
        {dev ? (
          <Pill tone="grey">dev mode: plain HTTP</Pill>
        ) : cert ? (
          <Pill tone={late ? 'amber' : 'green'}>
            HTTPS by {cert.issuer} · {late ? `expires in ${duration(left)}, renewal is late` : 'renews automatically'}
          </Pill>
        ) : (
          <Pill tone="amber">no certificate yet: issued on the first visit once DNS points here</Pill>
        )}
      </div>
      {!dev && (elsewhere || status.dnsError) && <DnsRecords status={status} replace={elsewhere} />}
    </>
  )
}

/** The DNS record that points a domain here. */
function DnsRecords({ status, replace }: { status: DomainStatus; replace: boolean }) {
  if (status.records.length === 0) {
    return <p className="muted dns-help">Point an A record for {status.host} at this server's public IP address.</p>
  }
  return (
    <div className="dns-help">
      <p className="muted">
        {replace
          ? `At the domain's DNS provider, replace the A and AAAA records for ${status.host} with:`
          : "Create this record at the domain's DNS provider:"}
      </p>
      <div className="dns-records">
        {status.records.map((r) => (
          <div key={r.type + r.value} className="dns-record">
            <code>{r.name}</code>
            <code>{r.type}</code>
            <code>{r.value}</code>
          </div>
        ))}
      </div>
      <p className="muted">
        DNS only, not proxied (Cloudflare's grey cloud). Once it has spread, this shows DNS points here.
      </p>
    </div>
  )
}

/** "3 hours", "64 days". */
function duration(ms: number): string {
  const hours = Math.max(0, Math.floor(ms / 3600000))
  if (hours < 48) return hours === 1 ? '1 hour' : `${hours} hours`
  return `${Math.floor(hours / 24)} days`
}
