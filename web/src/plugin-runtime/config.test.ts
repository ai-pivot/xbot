import { describe, expect, it, vi } from 'vitest'

import type { RPCAPI } from '@/plugin-api'
import { PluginConfigService } from './config'

function makeRpc() {
  const rpc = {
    call: vi.fn(async (_method: string, _params: unknown) => {
      return { plugins: [{ values: { mode: 'auto' } }] }
    }),
    notify: vi.fn(),
  } as unknown as RPCAPI
  return { rpc }
}

describe('PluginConfigService', () => {
  it('forPlugin.get calls the core plugin_config RPC (no dot) with the plugin id', async () => {
    const { rpc } = makeRpc()
    const svc = new PluginConfigService(rpc)
    const api = svc.forPlugin('xbot.git')
    const values = await api.get()
    expect(rpc.call).toHaveBeenCalledWith('plugin_config', { id: 'xbot.git' })
    expect(values).toEqual({ mode: 'auto' })
  })

  it('forPlugin.set calls the core plugin_config_set RPC with id/key/value', async () => {
    const { rpc } = makeRpc()
    const svc = new PluginConfigService(rpc)
    const api = svc.forPlugin('xbot.git')
    await api.set('mode', 'manual')
    expect(rpc.call).toHaveBeenCalledWith('plugin_config_set', {
      id: 'xbot.git',
      key: 'mode',
      value: 'manual',
    })
  })

  it('notifyChanged dispatches only to the matching plugin', () => {
    const { rpc } = makeRpc()
    const svc = new PluginConfigService(rpc)
    const handlerA = vi.fn()
    const handlerB = vi.fn()
    svc.forPlugin('a').onConfigChange(handlerA)
    svc.forPlugin('b').onConfigChange(handlerB)
    svc.notifyChanged('a', { x: 1 })
    expect(handlerA).toHaveBeenCalledWith({ x: 1 })
    expect(handlerB).not.toHaveBeenCalled()
  })

  it('onConfigChange returns a disposable that unsubscribes', () => {
    const { rpc } = makeRpc()
    const svc = new PluginConfigService(rpc)
    const handler = vi.fn()
    const dispose = svc.forPlugin('a').onConfigChange(handler)
    svc.notifyChanged('a', { x: 1 })
    dispose()
    svc.notifyChanged('a', { x: 2 })
    expect(handler).toHaveBeenCalledTimes(1)
  })
})
