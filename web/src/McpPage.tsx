import { useState, type FormEvent } from 'react'
import { Plus } from 'lucide-react'
import { api, errorMessage, type AccessLevel } from './api'
import { formatDate, timeAgo, usePoll } from './lib'
import { CodeBlock, ConfirmButton, Drawer, PageHead, Pill, Section } from './ui'

const levels: { value: AccessLevel; label: string; description: string }[] = [
  {
    value: 'read',
    label: 'Read only',
    description: 'Projects and their status, logs, variable names, jobs, and read-only SQL on their databases.',
  },
  {
    value: 'manage',
    label: 'Manage',
    description:
      'Also create projects, deploy, restart, stop and roll back, and change variables, settings, domains and jobs. It can’t delete projects.',
  },
  { value: 'full', label: 'Full', description: 'Also delete projects, with their databases and files.' },
]

const levelLabel = (v: AccessLevel) => levels.find((l) => l.value === v)?.label ?? v

/**
 * The MCP page: access tokens that let AI agents work on this server, how
 * to connect one, and what agents have done.
 */
export function McpPage() {
  const { data, error, reload } = usePoll(api.mcp, 5000, 'mcp')
  const [creating, setCreating] = useState(false)
  const [created, setCreated] = useState<{ name: string; secret: string } | null>(null)
  const endpoint = `${window.location.origin}/mcp`
  const live = new Set(data?.tokens.map((t) => t.id))

  async function revoke(id: number) {
    try {
      await api.revokeAccessToken(id)
    } finally {
      reload()
    }
  }

  return (
    <div className="page narrow">
      <PageHead
        title="MCP"
        description="Connect an AI agent such as Claude Code, so it can create, deploy and debug apps on this server."
      >
        <button className="primary" onClick={() => setCreating(true)}>
          <Plus size={14} /> New token
        </button>
      </PageHead>

      {error && <p className="error">{error}</p>}

      {created ? (
        <div className="notice">
          <strong>Token for {created.name}.</strong> This is the only time it's shown. Add it to your agent now.
          <ConnectSteps endpoint={endpoint} token={created.secret} />
          <button className="ghost" onClick={() => setCreated(null)}>
            I've added it
          </button>
        </div>
      ) : (
        <Section
          title="Connect an agent"
          description="Make a token, then add Sideyard to your agent with it. The agent can then deploy apps, read their logs and fix them without this dashboard."
        >
          <ConnectSteps endpoint={endpoint} token="<token>" />
        </Section>
      )}

      <Section title="Access tokens" description="Each token is for one agent or machine. Revoking one stops it at once.">
        {data?.tokens.length === 0 && <p className="muted">No tokens yet.</p>}
        <div className="kv">
          {data?.tokens.map((t) => (
            <div key={t.id} className="kv-row">
              <strong className="ellipsis">{t.name}</strong>
              <span className="muted small-text">
                Created {formatDate(t.createdAt)} · {t.lastUsedAt ? `last used ${timeAgo(t.lastUsedAt)}` : 'never used'}
              </span>
              <Pill tone={t.access === 'full' ? 'amber' : 'grey'}>{levelLabel(t.access)}</Pill>
              <ConfirmButton label="Revoke" confirmLabel="Revoke" onConfirm={() => revoke(t.id)} />
            </div>
          ))}
        </div>
      </Section>

      <Section title="Activity" description="The last 100 tool calls agents made. Variable values are never recorded.">
        {data?.calls.length === 0 && <p className="muted">No calls yet.</p>}
        <div className="kv">
          {data?.calls.map((c, i) => (
            <div key={i} className="kv-row call">
              <span className="ellipsis">
                <code>{c.tool}</code>
                {c.project && <span className="muted"> on {c.project}</span>}
                {c.args && (
                  <div className="muted small-text ellipsis" title={c.args}>
                    {c.args}
                  </div>
                )}
                {c.error && <div className="error small-text">{c.error}</div>}
              </span>
              <span className="muted small-text ellipsis">
                {c.token}
                {!live.has(c.tokenId) && ' (revoked)'}
              </span>
              <Pill tone={c.error ? 'red' : 'green'}>{c.error ? 'failed' : 'ok'}</Pill>
              <span className="muted small-text">{timeAgo(c.createdAt)}</span>
            </div>
          ))}
        </div>
      </Section>

      {creating && (
        <NewToken
          onClose={() => setCreating(false)}
          onCreated={(name, secret) => {
            setCreating(false)
            setCreated({ name, secret })
            reload()
          }}
        />
      )}
    </div>
  )
}

function ConnectSteps({ endpoint, token }: { endpoint: string; token: string }) {
  return (
    <>
      <h4>Claude Code</h4>
      <p className="muted">Run this once on the machine; the agent can then use Sideyard in every project there.</p>
      <CodeBlock>{`claude mcp add --scope user --transport http sideyard ${endpoint} \\
  --header "Authorization: Bearer ${token}"`}</CodeBlock>
      <h4>Other agents</h4>
      <p className="muted">Other agents connect to the same URL with the token as a header. Most take JSON like this.</p>
      <CodeBlock>{JSON.stringify(
        { mcpServers: { sideyard: { type: 'http', url: endpoint, headers: { Authorization: `Bearer ${token}` } } } },
        null,
        2,
      )}</CodeBlock>
    </>
  )
}

function NewToken({ onClose, onCreated }: { onClose: () => void; onCreated: (name: string, secret: string) => void }) {
  const [name, setName] = useState('')
  const [access, setAccess] = useState<AccessLevel>('manage')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  async function submit(e?: FormEvent) {
    e?.preventDefault()
    setBusy(true)
    setError(null)
    try {
      const { token, secret } = await api.createAccessToken(name, access)
      onCreated(token.name, secret)
    } catch (err) {
      setError(errorMessage(err))
      setBusy(false)
    }
  }

  return (
    <Drawer
      title="New access token"
      onClose={onClose}
      footer={
        <>
          {error && <span className="error grow small-text">{error}</span>}
          <button onClick={onClose}>Cancel</button>
          <button className="primary" disabled={busy || !name.trim()} onClick={() => submit()}>
            {busy ? 'Creating…' : 'Create token'}
          </button>
        </>
      }
    >
      <form onSubmit={submit} className="stack" style={{ marginTop: 0 }}>
        <label className="field">
          <span>Name</span>
          <input autoFocus placeholder="my-laptop" value={name} onChange={(e) => setName(e.target.value)} />
          <small className="muted">The agent or machine it's for.</small>
        </label>
        <div className="field">
          <span>Access</span>
          <div className="chips" role="radiogroup" aria-label="Access">
            {levels.map((l) => (
              <button
                type="button"
                key={l.value}
                role="radio"
                aria-checked={access === l.value}
                className={`chip ${access === l.value ? 'on' : ''}`}
                onClick={() => setAccess(l.value)}
              >
                {l.label}
              </button>
            ))}
          </div>
          <small className="muted">{levels.find((l) => l.value === access)?.description}</small>
        </div>
        <p className="muted">Every tool call it makes is recorded on this page.</p>
      </form>
    </Drawer>
  )
}
