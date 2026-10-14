import { useEffect, useState, type FormEvent } from 'react'

type Note = { id: number; body: string; createdAt: string }

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(path, {
    method,
    headers: body === undefined ? undefined : { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  if (!res.ok) throw new Error(await res.text())
  return (res.status === 204 ? null : await res.json()) as T
}

export function App() {
  const [notes, setNotes] = useState<Note[]>([])
  const [body, setBody] = useState('')
  const [error, setError] = useState<string | null>(null)

  const load = () => request<Note[]>('GET', '/api/notes').then(setNotes, (e) => setError(String(e)))
  useEffect(() => {
    load()
  }, [])

  async function add(e: FormEvent) {
    e.preventDefault()
    await request('POST', '/api/notes', { body })
    setBody('')
    load()
  }

  async function remove(id: number) {
    await request('DELETE', `/api/notes/${id}`)
    load()
  }

  return (
    <main>
      <h1>Notes</h1>
      <p className="muted">Go + React + SQLite, deployed with Sideyard.</p>
      <form onSubmit={add}>
        <input value={body} onChange={(e) => setBody(e.target.value)} placeholder="Write a note…" autoFocus />
        <button disabled={!body.trim()}>Add</button>
      </form>
      {error && <p className="error">{error}</p>}
      <ul>
        {notes.map((n) => (
          <li key={n.id}>
            <span>{n.body}</span>
            <button className="ghost" onClick={() => remove(n.id)}>
              Delete
            </button>
          </li>
        ))}
      </ul>
    </main>
  )
}
