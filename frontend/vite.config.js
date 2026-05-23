import {fileURLToPath, URL} from 'node:url'
import {defineConfig} from 'vite'
import {svelte} from '@sveltejs/vite-plugin-svelte'

// https://vitejs.dev/config/
export default defineConfig(({mode}) => ({
  plugins: [svelte()],
  resolve: {
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
