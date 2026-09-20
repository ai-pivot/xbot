import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, screen, waitFor } from '@testing-library/react'
import '@testing-library/jest-dom/vitest'

// 面板与服务端只经 postAPI('/api/rpc', {method, params}) 通信。
const postAPIMock = vi.hoisted(() => vi.fn())
vi.mock('@/lib/api', () => ({ postAPI: postAPIMock }))

import { changeLocale } from '@/i18n'
import { renderWithProviders } from '@/test-utils'
import { SettingsStorage } from './SettingsStorage'

// 与 channel.StorageSchema() 同形（服务端是唯一来源，这里只做形状替身）。
const schema = JSON.stringify([
  {
    key: 'provider',
    label: 'Storage backend',
    type: 'select',
    default_value: 'local',
    options: [
      { label: 'Local static (this server)', value: 'local' },
      { label: 'Qiniu Kodo', value: 'qiniu' },
      { label: 'S3 compatible', value: 's3' },
    ],
  },
  { key: 'qiniu_access_key', label: 'Access key', type: 'text', depends_on_key: 'provider', depends_on_values: 'qiniu' },
  { key: 'qiniu_secret_key', label: 'Secret key', type: 'password', depends_on_key: 'provider', depends_on_values: 'qiniu' },
  { key: 'qiniu_bucket', label: 'Bucket', type: 'text', depends_on_key: 'provider', depends_on_values: 'qiniu' },
  { key: 's3_bucket', label: 'Bucket', type: 'text', depends_on_key: 'provider', depends_on_values: 's3' },
  { key: 's3_use_path_style', label: 'Path-style addressing', type: 'toggle', depends_on_key: 'provider', depends_on_values: 's3' },
])

/** get_storage_config 的响应（secret 已被服务端打码）。 */
function fixture(over: Record<string, string> = {}) {
  return {
    provider: 'local',
    qiniu_access_key: '',
    qiniu_secret_key: '',
    qiniu_bucket: '',
    s3_bucket: '',
    s3_use_path_style: 'false',
    _schema: schema,
    _active: 'local',
    ...over,
  }
}

function mockRPC(get = fixture(), set?: unknown) {
  postAPIMock.mockImplementation((_endpoint: string, body: { method: string }) => {
    if (body?.method === 'get_storage_config') return Promise.resolve(get)
    if (body?.method === 'set_storage_config') {
      return set ? Promise.resolve(set) : Promise.resolve({ ...get, _active: 'qiniu' })
    }
    return Promise.resolve({})
  })
}

describe('SettingsStorage（设置 → 存储）', () => {
  beforeEach(() => {
    postAPIMock.mockReset()
    void changeLocale('zh-CN')
  })

  it('加载后渲染 provider 选择器与「当前生效」后端', async () => {
    mockRPC()
    renderWithProviders(<SettingsStorage />)

    await waitFor(() => expect(screen.getByTestId('storage-provider')).toBeInTheDocument())
    const select = screen.getByTestId('storage-provider') as HTMLSelectElement
    expect(select.value).toBe('local')
    expect(Array.from(select.options).map((o) => o.value)).toEqual(['local', 'qiniu', 's3'])
    expect(screen.getByTestId('storage-active').textContent).toBe('local')
    // local 时云端字段不可见（条件显示）
    expect(screen.queryByTestId('storage-qiniu_bucket')).toBeNull()
    expect(screen.queryByTestId('storage-s3_bucket')).toBeNull()
  })

  it('切到 qiniu 后显示该后端凭据（条件显示），secret 为 password 且值来自服务端（已打码）', async () => {
    mockRPC(fixture({ qiniu_access_key: 'AKID****', qiniu_secret_key: 'SUPE****', qiniu_bucket: 'b1', _active: 'qiniu' }))
    renderWithProviders(<SettingsStorage />)

    await waitFor(() => expect(screen.getByTestId('storage-provider')).toBeInTheDocument())
    fireEvent.change(screen.getByTestId('storage-provider'), { target: { value: 'qiniu' } })

    const secret = screen.getByTestId('storage-qiniu_secret_key') as HTMLInputElement
    expect(secret.type).toBe('password')
    expect(secret.value).toBe('SUPE****') // 服务端已打码，前端不解释
    expect((screen.getByTestId('storage-qiniu_bucket') as HTMLInputElement).value).toBe('b1')
    // s3 字段仍不可见
    expect(screen.queryByTestId('storage-s3_bucket')).toBeNull()
  })

  it('改动后才可保存；保存把整份 values 交给 set_storage_config，并用响应刷新（含 _active）', async () => {
    mockRPC(fixture(), { ...fixture({ provider: 's3', s3_bucket: 'mybucket' }), _active: 's3' })
    renderWithProviders(<SettingsStorage />)

    await waitFor(() => expect(screen.getByTestId('storage-save')).toBeInTheDocument())
    const saveBtn = screen.getByTestId('storage-save') as HTMLButtonElement
    expect(saveBtn.disabled).toBe(true) // 未改动

    fireEvent.change(screen.getByTestId('storage-provider'), { target: { value: 's3' } })
    fireEvent.change(screen.getByTestId('storage-s3_bucket'), { target: { value: 'mybucket' } })
    expect(saveBtn.disabled).toBe(false)

    fireEvent.click(saveBtn)

    await waitFor(() => expect(screen.getByTestId('storage-saved')).toBeInTheDocument())
    const setCall = postAPIMock.mock.calls.find((c) => (c[1] as { method: string }).method === 'set_storage_config')
    expect(setCall, 'set_storage_config 必须被调用').toBeTruthy()
    const values = (setCall![1] as { params: { values: Record<string, string> } }).params.values
    expect(values.provider).toBe('s3')
    expect(values.s3_bucket).toBe('mybucket')
    // 未触及字段也随整份 draft 提交（服务端会跳过掩码值，不会误清凭据）
    expect(values.qiniu_access_key).toBe('')
    // 响应刷新后「当前生效」变为 s3
    expect(screen.getByTestId('storage-active').textContent).toBe('s3')
  })

  it('保存失败时展示服务端错误（如凭据不全），不改动「当前生效」', async () => {
    mockRPC(fixture({ _active: 'local' }))
    postAPIMock.mockImplementation((_endpoint: string, body: { method: string }) => {
      if (body?.method === 'get_storage_config') return Promise.resolve(fixture({ _active: 'local' }))
      return Promise.reject(new Error('qiniu storage requires: qiniu_bucket'))
    })
    renderWithProviders(<SettingsStorage />)

    await waitFor(() => expect(screen.getByTestId('storage-provider')).toBeInTheDocument())
    fireEvent.change(screen.getByTestId('storage-provider'), { target: { value: 'qiniu' } })
    fireEvent.click(screen.getByTestId('storage-save'))

    await waitFor(() => expect(screen.getByTestId('storage-error')).toBeInTheDocument())
    expect(screen.getByTestId('storage-error').textContent).toContain('qiniu_bucket')
    expect(screen.getByTestId('storage-active').textContent).toBe('local')
  })
})
