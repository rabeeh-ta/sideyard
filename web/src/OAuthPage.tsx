import { useEffect, useState, type FormEvent, type ReactNode } from 'react'
import { api, errorMessage, type OAuthProvider } from './api'
import { usePoll } from './lib'
import { CodeBlock, CopyButton, PageHead, Pill, Section } from './ui'

type Status = { ok: boolean; message: string } | null

/** Where to create each provider's OAuth client, step by step. */
const setupSteps: Record<OAuthProvider['id'], (p: OAuthProvider) => ReactNode> = {
  google: (p) => (
    <ol className="steps">
      <li>
        In the <a href={p.consoleUrl} target="_blank" rel="noreferrer">Google Cloud console</a>, open{' '}
        <strong>Google Auth Platform → Clients</strong> and create a client of type <strong>Web application</strong>. The
        first time, Google asks you to set up the consent screen: its app name is what people see when they sign in to any
        of your apps, so pick one that covers them all.
      </li>
      <li>
        Under <strong>Authorized redirect URIs</strong>, add the callback URL below. It's the only one you need, however
        many apps use it.
      </li>
      <li>Paste the client ID and secret here, turn it on and save. Sideyard only asks for the email and profile scopes, which need no review by Google.</li>
    </ol>
  ),
  github: (p) => (
    <ol className="steps">
      <li>
        In GitHub's <a href={p.consoleUrl} target="_blank" rel="noreferrer">Developer settings</a>, open{' '}
        <strong>OAuth Apps → New OAuth App</strong> (under an organization's settings if it should belong to one).
      </li>
      <li>
        Use this dashboard's address as the <strong>Homepage URL</strong> and the callback URL below as the{' '}
        <strong>Authorization callback URL</strong>.
      </li>
      <li>Generate a client secret, then paste the client ID and secret here, turn it on and save.</li>
    </ol>
  ),
}

/**
 * The OAuth page: Google and GitHub sign-in, set up once for every app on
 * the server.
 */
export function OAuthPage() {
  const { data, error, reload } = usePoll(api.oauthProviders, 0, 'oauth')
  return (
    <div className="page narrow">
      <PageHead
        title="OAuth"
        description="Sign-in with Google and GitHub for every app on this server. Set a provider up once here; apps sign people in through Sideyard with their own token and never see the client secret."
      />
      {error && <p className="error">{error}</p>}
      {data?.providers.map((p) => <ProviderForm key={p.id} provider={p} onSaved={reload} />)}
      {data && (
        <Section
          title="Using it from an app"
          description="The starter template's auth.go does this. Each app keeps its own users and sessions; Sideyard only says who signed in."
        >
          <ol className="steps">
            <li>
              The app calls <code>POST /api/projects/&lt;name&gt;/oauth/start</code> with the provider and the URL to come
              back to (on one of its own domains), keeps the returned <code>state</code> in a cookie and sends the browser
              to the returned <code>url</code>.
            </li>
            <li>
              After the provider, Sideyard sends the browser back with a one-time <code>code</code> and the{' '}
              <code>state</code>.
            </li>
            <li>
              The app checks the state against its cookie and trades the code for the person at{' '}
              <code>POST /api/projects/&lt;name&gt;/oauth/exchange</code>.
            </li>
          </ol>
          <CodeBlock>{`mux.HandleFunc("GET /auth/{provider}", startSignIn) // link to /auth/google or /auth/github
mux.HandleFunc("GET /auth/callback", func(w http.ResponseWriter, r *http.Request) {
	user, err := finishSignIn(w, r) // provider, id, email, emailVerified, name, avatarUrl
	// upsert the user by (provider, id), start a session, redirect into the app
})`}</CodeBlock>
          <p className="muted help">
            Each project's <strong>Sign-in</strong> tab lists who has signed in to it.
          </p>
        </Section>
      )}
    </div>
  )
}

function ProviderForm({ provider: p, onSaved }: { provider: OAuthProvider; onSaved: () => void }) {
  const [enabled, setEnabled] = useState(p.enabled)
  const [clientId, setClientId] = useState(p.clientId)
  const [secret, setSecret] = useState('')
  const [status, setStatus] = useState<Status>(null)
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    setEnabled(p.enabled)
    setClientId(p.clientId)
  }, [p.enabled, p.clientId])

  async function save(e: FormEvent) {
    e.preventDefault()
    setBusy(true)
    setStatus(null)
    try {
      await api.saveOAuthProvider(p.id, { enabled, clientId, clientSecret: secret })
      setSecret('')
      setStatus({ ok: true, message: enabled ? `Saved. Apps can sign people in with ${p.name}.` : `Saved. ${p.name} sign-in is off.` })
      onSaved()
    } catch (err) {
      setStatus({ ok: false, message: errorMessage(err) })
    } finally {
      setBusy(false)
    }
  }

  // The tab opens before the request, so the browser doesn't block it.
  async function test() {
    setStatus(null)
    const tab = window.open('', '_blank')
    try {
      const { url } = await api.testOAuthProvider(p.id)
      if (tab) tab.location.href = url
      else window.location.href = url
    } catch (err) {
      tab?.close()
      setStatus({ ok: false, message: errorMessage(err) })
    }
  }

  return (
    <Section
      title={p.name}
      description={
        p.enabled ? (
          <>
            <Pill tone="green">on</Pill> Apps can sign people in with {p.name}.
          </>
        ) : (
          <>
            <Pill tone="grey">off</Pill> Not set up yet.
          </>
        )
      }
    >
      {setupSteps[p.id](p)}
      <div className="field">
        <span>Callback URL</span>
        <div className="inline-form">
          <code className="grow ellipsis callback-url">{p.callbackUrl}</code>
          <CopyButton text={p.callbackUrl} />
        </div>
        {p.callbackUrl.startsWith('http://') && (
          <small className="muted">
            Dev mode: the URL is plain http. Google accepts it for localhost addresses only.
          </small>
        )}
      </div>
      <form className="stack" onSubmit={save}>
        <div className="grid2">
          <label className="field">
            <span>Client ID</span>
            <input value={clientId} onChange={(e) => setClientId(e.target.value)} spellCheck={false} autoComplete="off" />
          </label>
          <label className="field">
            <span>Client secret</span>
            <input
              type="password"
              value={secret}
              onChange={(e) => setSecret(e.target.value)}
              placeholder={p.hasSecret ? 'saved; leave empty to keep it' : ''}
              autoComplete="new-password"
            />
          </label>
        </div>
        <label className="toggle">
          <input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} /> Let apps sign people
          in with {p.name}
        </label>
        {status && <p className={status.ok ? 'success' : 'error'}>{status.message}</p>}
        <div className="inline-form">
          <button className="primary" disabled={busy}>
            Save
          </button>
          <span className="grow" />
          <button type="button" className="ghost" disabled={!p.enabled} onClick={test} title="Sign in yourself to check the setup">
            Test sign-in
          </button>
        </div>
      </form>
    </Section>
  )
}
