import { useEffect, useState, type FormEvent } from 'react'
import { api, errorMessage } from './api'
import { formatDate, usePoll } from './lib'
import { CodeBlock, Pill, Section } from './ui'

export function EmailTab({ name }: { name: string }) {
  const { data, error, reload } = usePoll(() => api.projectEmail(name), 10000, name)
  const [sender, setSender] = useState('')
  const [to, setTo] = useState('')
  const [status, setStatus] = useState<{ ok: boolean; message: string } | null>(null)
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    if (data) setSender(data.sender)
  }, [data?.sender])

  async function run(fn: () => Promise<unknown>, ok: string) {
    setBusy(true)
    setStatus(null)
    try {
      await fn()
      setStatus({ ok: true, message: ok })
    } catch (e) {
      setStatus({ ok: false, message: errorMessage(e) })
    } finally {
      setBusy(false)
      reload()
    }
  }

  if (error) return <p className="error">{error}</p>
  if (!data) return null
  if (!data.configured) {
    return (
      <div className="card empty">
        <h3>Email isn't set up</h3>
        <p className="muted">
          Connect an email provider once in <a href="#/settings">Platform settings</a>, then every project can send through it
          without its own credentials.
        </p>
      </div>
    )
  }

  return (
    <>
      {status && <p className={status.ok ? 'success' : 'error'}>{status.message}</p>}
      <Section
        title="Sender"
        description={
          data.senderDomains.length > 0
            ? `Who this project's email comes from. It has to be on a verified domain: ${data.senderDomains.join(', ')}.`
            : "Who this project's email comes from."
        }
      >
        <form
          className="inline-form"
          onSubmit={(e: FormEvent) => {
            e.preventDefault()
            run(() => api.setSender(name, sender), 'Sender saved.')
          }}
        >
          <input
            className="grow"
            value={sender}
            onChange={(e) => setSender(e.target.value)}
            placeholder={`${data.defaultSender} (the platform default)`}
            spellCheck={false}
          />
          <button className="primary" disabled={busy || sender === data.sender}>
            Save
          </button>
        </form>
        <form
          className="inline-form"
          onSubmit={(e: FormEvent) => {
            e.preventDefault()
            run(() => api.projectTestEmail(name, to), `Test email sent to ${to}.`)
          }}
        >
          <input className="grow" type="email" value={to} onChange={(e) => setTo(e.target.value)} placeholder="you@example.com" />
          <button className="ghost" disabled={busy || !to}>
            Send a test email
          </button>
        </form>
      </Section>

      <Section
        title="Sending from the app"
        description="The app calls Sideyard's API with its SIDEYARD_TOKEN; the provider's credentials never reach it. Up to 50 recipients per email and 60 emails a minute."
      >
        <CodeBlock>{`func sendEmail(ctx context.Context, to []string, subject, text string) error {
	body, _ := json.Marshal(map[string]any{"to": to, "subject": subject, "text": text})
	url := os.Getenv("SIDEYARD_API_URL") + "/api/projects/" + os.Getenv("SIDEYARD_PROJECT") + "/email/send"
	req, _ := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+os.Getenv("SIDEYARD_TOKEN"))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("email: %s", msg)
	}
	return nil
}`}</CodeBlock>
        <p className="muted help">
          Also accepted: <code>html</code>, <code>replyTo</code>, and <code>from</code> to override the sender.
        </p>
      </Section>

      <Section title="Delivery log" description="The last 100 emails this project sent.">
        {data.log.length === 0 && <p className="muted">Nothing sent yet.</p>}
        <div className="kv">
          {data.log.map((e, i) => (
            <div key={i} className="kv-row" title={e.detail}>
              <span>
                <Pill tone={e.status === 'sent' ? 'green' : 'red'}>{e.status}</Pill>
              </span>
              <span className="ellipsis">
                <strong>{e.subject}</strong> <span className="muted">→ {e.recipients}</span>
                {e.detail && <div className="error small-text">{e.detail}</div>}
              </span>
              <span className="muted small-text">{formatDate(e.createdAt)}</span>
              <span />
            </div>
          ))}
        </div>
      </Section>
    </>
  )
}
