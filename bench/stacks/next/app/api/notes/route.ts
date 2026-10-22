import prisma from '../../../lib/prisma'

export const dynamic = 'force-dynamic'

export async function GET() {
  const notes = await prisma.note.findMany({ orderBy: { id: 'desc' }, take: 100 })
  return Response.json(notes)
}

export async function POST(request: Request) {
  const data = await request.json().catch(() => ({}))
  const body = typeof data.body === 'string' ? data.body.trim() : ''
  if (!body) return new Response('body is required', { status: 400 })
  const note = await prisma.note.create({ data: { body } })
  return Response.json(note, { status: 201 })
}
