import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// In development, run `go run .` (API on :3000) and `npm run dev` here, then
// open http://localhost:5173. Vite forwards /api to the Go server.
export default defineConfig({
  plugins: [react()],
  server: {
    proxy: { '/api': 'http://127.0.0.1:3000' },
  },
})
