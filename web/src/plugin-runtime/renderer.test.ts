import { describe, expect, it } from 'vitest'

import type { MessageRendererContribution } from '@/plugin-api'
import { PluginRuntime, matchesTool, type ToolRenderInput } from './index'
import type { PluginRuntimeHost } from './index'
import type { RpcTransport } from './rpc'
import type { UIServices } from './ui'

/** Minimal PluginRuntime host for tests that don't exercise activation. */
function makeHost(): PluginRuntimeHost {
  const rpc: RpcTransport = {
    call: async () => {
      throw new Error('not implemented')
    },
  } as unknown as RpcTransport
  const ui = {} as UIServices
  return {
    rpcTransport: rpc,
    moduleBaseUrl: () => '',
    loadViewComponent: async () => null,
    ui,
    getSession: () => null,
    getMessagesRaw: () => [],
    getBackendPlugins: async () => [],
    mountView: () => () => {},
    mountRenderer: () => () => {},
    mountCommand: () => () => {},
  }
}

describe('matchesTool', () => {
  it('matches { tool } by tool name', () => {
    const tool: ToolRenderInput = { name: 'shell' }
    expect(matchesTool({ tool: 'shell' }, tool)).toBe(true)
    expect(matchesTool({ tool: 'web_search' }, tool)).toBe(false)
  })

  it('matches { uiMode } by UIDecl mode (metadata-driven)', () => {
    const tool: ToolRenderInput = { name: 'display_html', uiMode: 'genui' }
    expect(matchesTool({ uiMode: 'genui' }, tool)).toBe(true)
    // uiMode match does NOT depend on the tool name (hardcoding removed).
    expect(matchesTool({ uiMode: 'genui' }, { name: 'custom-genui', uiMode: 'genui' })).toBe(true)
    expect(matchesTool({ uiMode: 'genui' }, { name: 'display_html' })).toBe(false)
  })

  it('does not match {role} for a tool (tools are not message roles)', () => {
    expect(matchesTool({ role: 'assistant' }, { name: 'x' })).toBe(false)
  })

  it('empty matcher matches everything (generic fallback)', () => {
    expect(matchesTool({}, { name: 'anything' })).toBe(true)
  })
})

describe('PluginRuntime.renderTool', () => {
  function makeRenderer(
    id: string,
    priority: number,
    matches: MessageRendererContribution['matches'],
    render: MessageRendererContribution['render'],
  ): MessageRendererContribution {
    return { kind: 'messageRenderer', id, priority, matches, render }
  }

  it('dispatches to a matching builtin renderer and returns its node', () => {
    const runtime = new PluginRuntime(makeHost())
    runtime.registerBuiltinRenderer(
      makeRenderer('test.genui', 100, { uiMode: 'genui' }, (msg) => {
        const tool = (msg as { tool: { result: { name?: string } } }).tool.result
        return `rendered:${tool.name}`
      }),
    )

    const out = runtime.renderTool({ name: 'display_html', uiMode: 'genui' }, { chatID: 'c' })
    expect(out).toBe('rendered:display_html')
  })

  it('returns null when no renderer matches', () => {
    const runtime = new PluginRuntime(makeHost())
    runtime.registerBuiltinRenderer(
      makeRenderer('genui', 100, { uiMode: 'genui' }, () => 'x'),
    )

    expect(runtime.renderTool({ name: 'shell', uiMode: undefined }, { chatID: 'c' })).toBeNull()
  })

  it('falls through to the next renderer when a higher-priority one returns null', () => {
    const runtime = new PluginRuntime(makeHost())
    runtime.registerBuiltinRenderer(
      makeRenderer('high', 200, { uiMode: 'genui' }, () => null),
    )
    runtime.registerBuiltinRenderer(
      makeRenderer('low', 100, { uiMode: 'genui' }, () => 'fallback-node'),
    )

    expect(runtime.renderTool({ name: 'x', uiMode: 'genui' }, { chatID: 'c' })).toBe('fallback-node')
  })

  it('prefers higher priority when both match', () => {
    const runtime = new PluginRuntime(makeHost())
    runtime.registerBuiltinRenderer(
      makeRenderer('high', 200, { uiMode: 'genui' }, () => 'high-node'),
    )
    runtime.registerBuiltinRenderer(
      makeRenderer('low', 100, { uiMode: 'genui' }, () => 'low-node'),
    )

    expect(runtime.renderTool({ name: 'x', uiMode: 'genui' }, { chatID: 'c' })).toBe('high-node')
  })

  it('unregister removes the renderer (disposable)', () => {
    const runtime = new PluginRuntime(makeHost())
    const dispose = runtime.registerBuiltinRenderer(
      makeRenderer('genui', 100, { uiMode: 'genui' }, () => 'node'),
    )
    expect(runtime.renderTool({ name: 'x', uiMode: 'genui' }, { chatID: 'c' })).toBe('node')

    dispose()
    expect(runtime.renderTool({ name: 'x', uiMode: 'genui' }, { chatID: 'c' })).toBeNull()
  })
})