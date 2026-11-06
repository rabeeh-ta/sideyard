import { api } from './api'
import { formatDate, timeAgo, usePoll } from './lib'
import { CodeBlock, Pill, Section } from './ui'

const providerName: Record<string, string> = { google: 'Google', github: 'GitHub' }

/** A project's sign-in: which providers its app can use, and who has signed in. */
export function SignInTab({ name }: { name: string }) {
  const { data, error } = usePoll(() => api.projectSignIns(name), 10000, name)
  if (error) return <p className="error">{error}</p>
  if (!data) return null

  return (
    <>
      {data.providers.length === 0 ? (
        <div className="card empty">
          <h3>Sign-in isn't set up</h3>
          <p className="muted">
            Set up Google or GitHub once on the <a href="#/oauth">OAuth</a> page, then every app on this server can sign
            people in with it.
          </p>
        </div>
      ) : (
        <Section
          title="Signing in from the app"
          description={
            <>
              Available: {data.providers.map((p) => providerName[p] ?? p).join(' and ')}. The starter's{' '}
              <code>auth.go</code> talks to Sideyard with the app's SIDEYARD_TOKEN; the app keeps its own users and
              sessions.
            </>
          }
        >
          <CodeBlock>{`mux.HandleFunc("GET /auth/{provider}", startSignIn) // link to ${data.providers.map((p) => `/auth/${p}`).join(' or ')}
mux.HandleFunc("GET /auth/callback", func(w http.ResponseWriter, r *http.Request) {
	user, err := finishSignIn(w, r) // provider, id, email, emailVerified, name, avatarUrl
	// upsert the user by (provider, id), start a session, redirect into the app
})`}</CodeBlock>
          <p className="muted help">
            The browser only comes back to this project's own domains, at <code>/auth/callback</code>.
          </p>
        </Section>
      )}

      <Section
        title="People"
        description={
          data.total > data.people.length
            ? `${data.total} people have signed in; the ${data.people.length} most recent are shown.`
            : 'Everyone who has signed in to this app through Sideyard, most recent first.'
        }
      >
        {data.people.length === 0 ? (
          <p className="muted">Nobody has signed in yet.</p>
        ) : (
          <div className="scroll-x">
            <table className="table">
              <thead>
                <tr>
                  <th>Person</th>
                  <th>Provider</th>
                  <th className="num">Sign-ins</th>
                  <th>First</th>
                  <th>Last</th>
                </tr>
              </thead>
              <tbody>
                {data.people.map((u) => (
                  <tr key={`${u.provider}:${u.id}`}>
                    <td>
                      <div className="person">
                        {u.avatarUrl && <img className="avatar" src={u.avatarUrl} alt="" referrerPolicy="no-referrer" />}
                        <div className="ellipsis">
                          <strong>{u.name || u.email || u.id}</strong>
                          {u.email && u.name && <div className="muted small-text">{u.email}</div>}
                        </div>
                      </div>
                    </td>
                    <td>
                      <Pill tone="grey">{providerName[u.provider] ?? u.provider}</Pill>
                    </td>
                    <td className="num">{u.count}</td>
                    <td className="muted small-text">{formatDate(u.firstAt)}</td>
                    <td className="muted small-text" title={formatDate(u.lastAt)}>
                      {timeAgo(u.lastAt)}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Section>
    </>
  )
}
