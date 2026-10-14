import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// `npm run dev` serves the dashboard on :5173 and forwards /api to a
// Sideyard running locally (`make dev` in the repo root).
export default defineConfig({
  plugins: [react()],
  server: {
    proxy: { '/api': 'http://127.0.0.1:7070' },
  },
})
