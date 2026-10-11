import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { downloadSessionsArchive } from './api'
import { postAPI } from '@/lib/api'

vi.mock('@/lib/api', () => ({ postAPI: vi.fn(), APIError: class extends Error {} }))
const objects: Blob[] = []
beforeEach(() => {
  vi.clearAllMocks(); objects.length = 0
  vi.stubGlobal('URL', class extends URL { static createObjectURL = vi.fn((blob: Blob) => { objects.push(blob); return 'blob:test' }); static revokeObjectURL = vi.fn() })
  vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})
})
afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals() })
async function entries(blob: Blob) {
  const buffer = await new Promise<ArrayBuffer>((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => resolve(reader.result as ArrayBuffer)
    reader.onerror = () => reject(reader.error)
    reader.readAsArrayBuffer(blob)
  })
  const view = new DataView(buffer)
  const decoder = new TextDecoder(), result: Record<string, string> = {}
  let offset = 0
  while (view.getUint32(offset, true) === 0x04034b50) {
    expect(view.getUint16(offset + 8, true)).toBe(0)
    const size = view.getUint32(offset + 18, true), nameLength = view.getUint16(offset + 26, true)
    const start = offset + 30 + nameLength + view.getUint16(offset + 28, true)
    const name = decoder.decode(buffer.slice(offset + 30, offset + 30 + nameLength))
    result[name] = decoder.decode(buffer.slice(start, start + size)); offset = start + size
  }
  expect(view.getUint32(offset, true)).toBe(0x02014b50)
  return result
}
it.each(['native', 'openai', 'codex'] as const)('downloads one %s archive with distinct files for duplicate labels across channels', async format => {
  vi.mocked(postAPI).mockResolvedValue({ messages: [{ role: 'user', content: '中文与 emoji 🐒' }], model: 'mock' })
  const result = await downloadSessionsArchive([{ channel: 'web', chatID: 'same', label: '../重复' }, { channel: 'cli', chatID: 'same', label: '../重复' }], format)
  expect(vi.mocked(postAPI).mock.calls.map(call => (call[1] as { params: unknown }).params)).toEqual([{ channel: 'web', chat_id: 'same' }, { channel: 'cli', chat_id: 'same' }])
  expect(result).toEqual({ succeeded: ['web:same', 'cli:same'], failed: [] })
  expect(HTMLAnchorElement.prototype.click).toHaveBeenCalledTimes(1)
  const files = await entries(objects[0])
  expect(Object.keys(files)).toHaveLength(2)
  expect(Object.keys(files).every(name => !name.includes('/') && !name.startsWith('.'))).toBe(true)
  Object.values(files).forEach(content => expect(content).toContain('中文与 emoji 🐒'))
})
it('downloads successful exports and reports only failed session keys', async () => {
  vi.mocked(postAPI).mockRejectedValueOnce(new Error('network')).mockResolvedValueOnce({ messages: [] })
  const result = await downloadSessionsArchive([{ channel: 'web', chatID: 'a' }, { channel: 'cli', chatID: 'b' }], 'native')
  expect(result).toEqual({ succeeded: ['cli:b'], failed: ['web:a'] })
  expect(Object.keys(await entries(objects[0]))).toHaveLength(1)
})
it('does not download an empty archive when every request fails', async () => {
  vi.mocked(postAPI).mockRejectedValue(new Error('network'))
  expect(await downloadSessionsArchive([{ channel: 'web', chatID: 'a' }], 'native')).toEqual({ succeeded: [], failed: ['web:a'] })
  expect(HTMLAnchorElement.prototype.click).not.toHaveBeenCalled()
})
