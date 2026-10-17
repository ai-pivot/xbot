import { describe, expect, it } from 'vitest'
import config from '../../vercel.json'

type Route = { handle?: string; src?: string; dest?: string; status?: number }
type Result = { status: number; target?: string }

// Model only the ordered route subset used by this static project. This is not
// a Vercel emulator: schema validation and deployed checks remain separate.
function resolve(pathname: string, files: ReadonlySet<string>): Result {
  for (const route of config.routes as Route[]) {
    if (route.handle === 'filesystem') {
      if (files.has(pathname)) return { status: 200, target: pathname }
      continue
    }
    if (route.src && new RegExp(route.src).test(pathname)) {
      return { status: route.status ?? 200, ...(route.dest ? { target: route.dest } : {}) }
    }
  }
  return { status: 404 }
}

const files = new Set(['/index.html', '/assets/app.hash.js', '/favicon.svg'])
const cases: [string, Result][] = [
  ['/index.html', { status: 200, target: '/index.html' }],
  ['/assets/app.hash.js', { status: 200, target: '/assets/app.hash.js' }],
  ['/favicon.svg', { status: 200, target: '/favicon.svg' }],
  ['/', { status: 200, target: '/index.html' }],
  ['/s/example-token', { status: 200, target: '/index.html' }],
  ['/sessions/abc', { status: 200, target: '/index.html' }],
  ['/settings', { status: 200, target: '/index.html' }],
  ['/apiary', { status: 200, target: '/index.html' }],
  ['/ws', { status: 404 }],
  ['/ws/terminal', { status: 404 }],
  ['/api', { status: 404 }],
  ['/api/health', { status: 404 }],
  ['/plugins', { status: 404 }],
  ['/plugins/example/module', { status: 404 }],
  ['/assets', { status: 404 }],
  ['/assets/chunk', { status: 404 }],
  ['/assets/missing.js', { status: 404 }],
  ['/missing.js', { status: 404 }],
  ['/missing.css', { status: 404 }],
  ['/sw2.js', { status: 404 }],
  ['/manifest.webmanifest', { status: 404 }],
  ['/static/missing', { status: 404 }],
  ['/images/missing', { status: 404 }],
  ['/fonts/missing', { status: 404 }],
  ['/icons/missing', { status: 404 }],
  ['/smg-new/missing', { status: 404 }],
  ['/_vercel/missing', { status: 404 }],
  ['/nested/file.woff2', { status: 404 }],
]

describe('Vercel static preview configuration', () => {
  it('uses the Vite build contract and one ordered routing scheme', () => {
    expect(config.$schema).toBe('https://openapi.vercel.sh/vercel.json')
    expect(config.framework).toBe('vite')
    expect(config.installCommand).toBe('npm ci')
    expect(config.buildCommand).toBe('npm run build')
    expect(config.outputDirectory).toBe('dist')
    expect(config).not.toHaveProperty('rootDirectory') // Set to web in project settings.
    expect(config).not.toHaveProperty('rewrites')
    expect(config).not.toHaveProperty('redirects')
    expect(config).not.toHaveProperty('headers') // Cannot mix these with low-level routes.
    expect(config.routes).toHaveLength(3)
    expect(config.routes[0]).toEqual({ handle: 'filesystem' })
    expect(config.routes[1]).toEqual({ src: expect.any(String), dest: '/index.html' })
    expect(config.routes[2]).toEqual({ src: '/.*', status: 404 })
  })

  it.each(cases)('resolves %s using filesystem → SPA → 404 ordering', (pathname, expected) => {
    expect(resolve(pathname, files)).toEqual(expected)
  })
})
