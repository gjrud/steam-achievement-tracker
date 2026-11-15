import {existsSync, readFileSync} from 'node:fs'
import {join} from 'node:path'
import {describe, expect, test} from 'vitest'

// The test alias swaps the generated app.js binding for wails-app-mock.js. These checks keep the hand-written
// mock, App.svelte's imports, and the sync event name in step with the Go backend.
// frontend/bindings is gitignored. Locally, the generated-bindings checks are skipped when that file is absent.
// In CI (CI set, as GitHub Actions does) a missing file fails the suite instead of passing silently.
// To run them, regenerate with `wails3 generate bindings` from the repository root.
const bindingsImportPath = '../bindings/github.com/gjrud/steam-achievement-tracker/app.js'
const regenerate = 'Regenerate the bindings with `wails3 generate bindings` and update wails-app-mock.js to match.'
const isCI = ['1', 'true'].includes((process.env.CI ?? '').toLowerCase())
const mockSource = readFileSync(join(__dirname, 'wails-app-mock.js'), 'utf8')
const appSource = readFileSync(join(__dirname, '..', 'App.svelte'), 'utf8')
const goSource = readFileSync(join(__dirname, '..', '..', '..', 'app.go'), 'utf8')
const generatedPath = join(__dirname, '..', '..', 'bindings', 'github.com', 'gjrud', 'steam-achievement-tracker', 'app.js')
const bindingsPresent = existsSync(generatedPath)
const generatedSource = bindingsPresent ? readFileSync(generatedPath, 'utf8') : ''
const describeWithBindings = bindingsPresent ? describe : describe.skip
const missingBindings = `Generated Wails bindings not found at ${generatedPath}. Run \`wails3 generate bindings\` from the repository root before running the frontend tests in CI.`

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
const syncEventPattern = /const syncChangedEvent = ["']([^"']+)["']/

describe('generated Wails bindings', () => {
  test.skipIf(!isCI)('are present when CI is set', () => {
    expect(bindingsPresent, missingBindings).toBe(true)
  })
})

describeWithBindings('wails mock matches generated bindings', () => {
  test('every mock export exists in the generated bindings with the same argument count', () => {
    for (const [name, mockParams] of mock) {
      expect(generated.has(name), `mock exports ${name}, but generated App.js does not. ${regenerate}`).toBe(true)

      // Rest parameters hide argument-count drift, so the mock must spell out its parameters.
      expect(mockParams.some((param) => param.startsWith('...')), `mock ${name} uses rest parameters; list explicit parameters instead. ${regenerate}`).toBe(false)
      expect(mockParams.length, `argument count for ${name}. ${regenerate}`).toBe(generated.get(name).length)
    }
  })

  test('every generated binding has a mock export', () => {
    for (const name of generated.keys()) {
      expect(mock.has(name), `generated App.js exports ${name}, but wails-app-mock.js does not mock it. ${regenerate}`).toBe(true)
    }
  })

  test('every name App.svelte imports exists in the generated bindings', () => {
    const escapedPath = bindingsImportPath.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
    const importMatch = appSource.match(new RegExp(`import\\s*\\{([^}]+)\\}\\s*from '${escapedPath}'`))
    expect(importMatch, `App.svelte imports from ${bindingsImportPath}`).not.toBeNull()

    const imported = importMatch[1].split(',').map((name) => name.trim()).filter(Boolean)
    for (const name of imported) {
      expect(generated.has(name), `App.svelte imports ${name}, but generated App.js does not export it. ${regenerate}`).toBe(true)
    }
  })
})

describe('sync:changed event name', () => {
  test('Go backend and App.svelte declare the same event name', () => {
    const goMatch = goSource.match(syncEventPattern)
    const svelteMatch = appSource.match(syncEventPattern)
    expect(goMatch, 'app.go declares const syncChangedEvent = "..."').not.toBeNull()
    expect(svelteMatch, 'App.svelte declares const syncChangedEvent = \'...\'').not.toBeNull()
    expect(svelteMatch[1], 'App.svelte syncChangedEvent must match app.go').toBe(goMatch[1])
  })
})
