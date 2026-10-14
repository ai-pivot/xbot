import { fireEvent, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import '@testing-library/jest-dom'
import { renderWithProviders } from '@/test-utils'
import { NewSessionDialog } from './NewSessionDialog'

vi.mock('@/components/session/PathPicker', () => ({
  PathPicker: ({ value, onChange }: { value: string; onChange: (value: string) => void }) => (
    <input aria-label="working directory" value={value} onChange={(event) => onChange(event.target.value)} />
  ),
}))
vi.mock('@/lib/recent-workdirs', () => ({
  loadRecentWorkDirs: () => ['/recent/repo'], removeRecentWorkDir: () => [],
}))

describe('NewSessionDialog project defaults', () => {
  beforeEach(() => vi.clearAllMocks())
  it('prefers the project path, allows editing, and submits the selected directory', async () => {
    const onCreate = vi.fn().mockResolvedValue('new-chat')
    renderWithProviders(<NewSessionDialog open initialWorkPath="/project/repo" onOpenChange={vi.fn()} onCreate={onCreate} />)
    const input = screen.getByLabelText('working directory')
    expect(input).toHaveValue('/project/repo')
    fireEvent.change(input, { target: { value: '/edited/repo' } })
    fireEvent.click(screen.getByRole('button', { name: /Confirm|确认/ }))
    await waitFor(() => expect(onCreate).toHaveBeenCalledWith(undefined, '/edited/repo'))
  })
  it('resets on reopening and global creation retains the recent-directory default', () => {
    const props = { onOpenChange: vi.fn(), onCreate: vi.fn() }
    const { rerender } = renderWithProviders(<NewSessionDialog {...props} open initialWorkPath="/project/one" />)
    expect(screen.getByLabelText('working directory')).toHaveValue('/project/one')
    rerender(<NewSessionDialog {...props} open={false} />)
    rerender(<NewSessionDialog {...props} open initialWorkPath="/project/two" />)
    expect(screen.getByLabelText('working directory')).toHaveValue('/project/two')
    rerender(<NewSessionDialog {...props} open={false} />)
    rerender(<NewSessionDialog {...props} open />)
    expect(screen.getByLabelText('working directory')).toHaveValue('/recent/repo')
  })
})
