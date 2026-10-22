import prisma from '../lib/prisma'

export const dynamic = 'force-dynamic'

export default async function Home() {
  const notes = await prisma.note.findMany({ orderBy: { id: 'desc' }, take: 20 })
  return (
    <main>
      <h1>Notes</h1>
      <ul>
        {notes.map((n) => (
          <li key={n.id}>{n.body}</li>
        ))}
      </ul>
    </main>
  )
}
