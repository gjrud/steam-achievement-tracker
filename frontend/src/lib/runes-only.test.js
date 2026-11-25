import {readFileSync, readdirSync} from 'node:fs'
import {dirname, join, relative, resolve} from 'node:path'
import {fileURLToPath} from 'node:url'
import {compile} from 'svelte/compiler'
import {describe, expect, test} from 'vitest'

// Every project .svelte file must compile in runes mode with zero warnings. vite.config.js forces runes for the build;
// this test also catches on:, <slot> and state_referenced_locally, which only warn.
const srcRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const frontendRoot = resolve(srcRoot, '..')

function svelteFiles(dir) {
  return readdirSync(dir, {withFileTypes: true}).flatMap((entry) => {
    const path = join(dir, entry.name)
    if (entry.isDirectory()) return svelteFiles(path)
    return entry.name.endsWith('.svelte') ? [path] : []
  })
}

describe('runes-only project components', () => {
  test('every .svelte file compiles in runes mode with no compiler warnings', () => {
    const files = svelteFiles(srcRoot)
    expect(files.length).toBeGreaterThan(10)
    const offenders = []
    for (const file of files) {
      const rel = relative(frontendRoot, file)
      try {
        const {warnings} = compile(readFileSync(file, 'utf8'), {filename: file, runes: true, dev: true})
        for (const warning of warnings) offenders.push(`${rel}:${warning.start?.line ?? '?'}: ${warning.code}: ${warning.message}`)
      } catch (err) {
        offenders.push(`${rel}: compile error: ${err.message}`)
      }
    }
    expect(offenders, `compiler warnings or errors in runes mode:\n${offenders.join('\n')}`).toEqual([])
  })
})
