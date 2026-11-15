import {fileURLToPath, URL} from 'node:url'
import {defineConfig} from 'vite'
import {svelte} from '@sveltejs/vite-plugin-svelte'

// Game covers (/game-covers/...) are served by the Go asset middleware in main.go, which runs before the embedded
// assets and the `wails3 dev` proxy. Vite never sees those requests, so no dev-server rule is needed for them.

// https://vitejs.dev/config/
export default defineConfig(({mode}) => ({
  server: {
    host: '127.0.0.1',
    port: Number(process.env.WAILS_VITE_PORT) || 9245,
    strictPort: true
  },
  plugins: [svelte()],
  resolve: {
    ...(mode === 'test' ? {conditions: ['browser']} : {}),
    alias: mode === 'test'
      ? [
          {
            find: '../bindings/github.com/gjrud/steam-achievement-tracker/app.js',
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
