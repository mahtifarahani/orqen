import path from 'node:path'
import { writeFileSync } from 'node:fs'
import tailwindcss from '@tailwindcss/vite'
import react from '@vitejs/plugin-react'
import { defineConfig } from 'vite'

// https://vite.dev/config/
export default defineConfig({
  plugins: [
    react(),
    tailwindcss(),
    {
      // Keep Go's embed directory present in a fresh checkout and after Vite cleans dist.
      name: 'go-embed-placeholder',
      closeBundle() {
        writeFileSync(path.resolve(import.meta.dirname, 'dist/.gitkeep'), '')
      },
    },
  ],
  resolve: { alias: { '@': path.resolve(import.meta.dirname, 'src') } },
  server: {
    port: 5173,
    proxy: { '/api': { target: 'http://127.0.0.1:4280', changeOrigin: false } },
  },
})
