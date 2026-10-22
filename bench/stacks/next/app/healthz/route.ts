import prisma from '../../lib/prisma'

export const dynamic = 'force-dynamic'

export async function GET() {
  try {
    await prisma.$queryRaw`SELECT 1`
    return new Response('ok')
  } catch {
    return new Response('database unavailable', { status: 503 })
  }
}
