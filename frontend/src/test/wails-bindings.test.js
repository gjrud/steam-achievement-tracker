import {existsSync, readFileSync} from 'node:fs'
import {join} from 'node:path'
import {describe, expect, test} from 'vitest'

// The test alias swaps wailsjs/go/main/App.js for wails-app-mock.js. These checks keep the hand-written
// mock and App.svelte's imports in step with the bindings Wails generates.
// frontend/wailsjs is gitignored, so the whole block is skipped when that file is absent.
// To run it, regenerate with `wails generate module`.
const mockSource = readFileSync(join(__dirname, 'wails-app-mock.js'), 'utf8')
const appSource = readFileSync(join(__dirname, '..', 'App.svelte'), 'utf8')
const generatedPath = join(__dirname, '..', '..', 'wailsjs', 'go', 'main', 'App.js')
const generatedSource = existsSync(generatedPath) ? readFileSync(generatedPath, 'utf8') : ''
const describeWithBindings = generatedSource ? describe : describe.skip
const regenerate = 'Regenerate the bindings with `wails generate module` and update wails-app-mock.js to match.'

// parseExports maps each exported binding name to its parameter list.
function parseExports(source, pattern) {
  const exports = new Map()
  for (const match of source.matchAll(pattern)) {
    exports.set(match[1], match[2].split(',').map((param) => param.trim()).filter(Boolean))
  }
  return exports
}

const generated = parseExports(generatedSource, /export function (\w+)\(([^)]*)\)/g)
const mock = parseExports(mockSource, /export const (\w+) = \(([^)]*)\) =>/g)

describeWithBindings('wails mock matches generated bindings', () => {
  test('every mock export exists in the generated bindings with the same argument count', () => {
    for (const [name, mockParams] of mock) {
      expect(generated.has(name), `mock exports ${name}, but generated App.js does not. ${regenerate}`).toBe(true)

      // Rest-argument mocks forward whatever they receive, so they cannot drift on count.
      if (mockParams.some((param) => param.startsWith('...'))) continue
      expect(mockParams.length, `argument count for ${name}. ${regenerate}`).toBe(generated.get(name).length)
    }
  })

  test('every name App.svelte imports exists in the generated bindings', () => {
    const importMatch = appSource.match(/import\s*\{([^}]+)\}\s*from '\.\.\/wailsjs\/go\/main\/App\.js'/)
    expect(importMatch, 'App.svelte imports from wailsjs/go/main/App.js').not.toBeNull()

    const imported = importMatch[1].split(',').map((name) => name.trim()).filter(Boolean)
    for (const name of imported) {
      expect(generated.has(name), `App.svelte imports ${name}, but generated App.js does not export it. ${regenerate}`).toBe(true)
    }
  })
})
