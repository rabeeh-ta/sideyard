// The notes API as a typical MERN backend: Express and Mongoose on MongoDB.
const express = require('express')
const mongoose = require('mongoose')

const Note = mongoose.model(
  'Note',
  new mongoose.Schema({ body: { type: String, required: true } }, { timestamps: true }),
)

const app = express()
app.use(express.json())

const toJSON = (n) => ({ id: n._id, body: n.body, createdAt: n.createdAt })

app.get('/healthz', async (req, res) => {
  try {
    await mongoose.connection.db.admin().ping()
    res.send('ok')
  } catch {
    res.status(503).send('database unavailable')
  }
})

app.get('/api/notes', async (req, res) => {
  const notes = await Note.find().sort({ _id: -1 }).limit(100).lean()
  res.json(notes.map(toJSON))
})

app.post('/api/notes', async (req, res) => {
  const body = typeof req.body?.body === 'string' ? req.body.body.trim() : ''
  if (!body) return res.status(400).send('body is required')
  const note = await Note.create({ body })
  res.status(201).json(toJSON(note))
})

mongoose.connect(process.env.MONGO_URL).then(() => {
  app.listen(Number(process.env.PORT), '127.0.0.1')
})
