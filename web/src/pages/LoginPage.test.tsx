/**
 * LoginPage — the register entry mirrors the deployment state:
 *   - bootstrap: visible, labelled as the one-time setup
 *   - registered: hidden entirely (no "create account" temptation)
 */
import { render, screen } from '@testing-library/react'
import '@testing-library/jest-dom'
import { MemoryRouter } from 'react-router-dom'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { LoginPage } from './LoginPage'

const auth = {
  login: vi.fn(),
  inviteOnly: true,
  bootstrap: true,
}

vi.mock('@/hooks/useAuth', () => ({ useAuth: () => auth }))

const DICT: Record<string, string> = {
  'auth.loginTitle': 'xbot',
  'auth.loginSubtitle': 'Sign in to xbot',
  'auth.username': 'Username',
  'auth.password': 'Password',
  'auth.loginButton': 'Login',
  'auth.noAccount': "Don't have an account?",
  'auth.createAdmin': 'Create account',
  'auth.register': 'Register',
  'auth.bootstrapBadge': 'First-time setup · one-time only',
}
vi.mock('@/providers/i18n', () => ({
  useI18n: () => ({ t: (k: string) => DICT[k] ?? k }),
}))

function renderPage() {
  return render(
    <MemoryRouter>
      <LoginPage />
    </MemoryRouter>,
  )
}

beforeEach(() => {
  auth.inviteOnly = true
  auth.bootstrap = true
})

describe('LoginPage register entry', () => {
  it('shows the one-time setup hint during bootstrap', () => {
    renderPage()
    expect(screen.getByTestId('login-bootstrap-hint')).toHaveTextContent('one-time only')
    expect(screen.getByRole('link', { name: 'Create account' })).toBeInTheDocument()
  })

  it('hides the register entry entirely once an account exists', () => {
    auth.bootstrap = false
    renderPage()
    expect(screen.queryByTestId('login-bootstrap-hint')).toBeNull()
    expect(screen.queryByRole('link', { name: /register|create account/i })).toBeNull()
  })
})
