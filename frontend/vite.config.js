import {fileURLToPath, URL} from 'node:url'
import {defineConfig} from 'vite'
import {svelte} from '@sveltejs/vite-plugin-svelte'

// Game covers are served by the Go asset handler. Under `wails dev`, Wails only falls back to that handler when the
// Vite dev server answers 404, but Vite's SPA fallback would otherwise return index.html for these URLs.
const gameCoversNotFound = () => ({
  name: 'game-covers-not-found',
  configureServer(server) {
    server.middlewares.use((req, res, next) => {
      if (req.url?.startsWith('/game-covers/')) {
        res.statusCode = 404
        res.end()
        return
      }
      next()
    })
  }
})

// https://vitejs.dev/config/
export default defineConfig(({mode}) => ({
  plugins: [svelte(), gameCoversNotFound()],
  resolve: {
    ...(mode === 'test' ? {conditions: ['browser']} : {}),
    alias: mode === 'test'
      ? [
          {
            find: '../wailsjs/go/main/App.js',
            replacement: fileURLToPath(new URL('./src/test/wails-app-mock.js', import.meta.url))
          }
        ]
      : []
  },
  test: {
    environment: 'jsdom',
    setupFiles: ['./src/test/setup.js']
  }
}))
