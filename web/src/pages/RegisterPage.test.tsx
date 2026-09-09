/**
 * RegisterPage — first-user bootstrap messaging.
 *
 * A fresh deployment accepts exactly one account. The page must SAY so,
 * otherwise a visitor cannot tell whether the endpoint is open to everyone
 * (the "is this safe?" doubt this test guards).
 */
import { render, screen } from '@testing-library/react'
import '@testing-library/jest-dom'
import { MemoryRouter } from 'react-router-dom'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { RegisterPage } from './RegisterPage'

const auth = {
  register: vi.fn(),
  inviteOnly: true,
  bootstrap: true,
}

vi.mock('@/hooks/useAuth', () => ({ useAuth: () => auth }))

const DICT: Record<string, string> = {
  'auth.registerTitle': 'xbot',
  'auth.bootstrapSubtitle': 'Create the operator account — the only registration accepted',
  'auth.bootstrapBadge': 'First-time setup · one-time only',
  'auth.bootstrapNotice': 'Registration closes automatically afterwards — no one else can sign up.',
  'auth.registerSubtitle': 'Create a new account',
  'auth.inviteOnlyTitle': 'Registration is invite-only',
  'auth.inviteOnlyNotice': 'Contact the operator to get access.',
  'auth.backToLogin': 'Back to login',
  'auth.username': 'Username',
  'auth.password': 'Password',
  'auth.confirmPassword': 'Confirm Password',
  'auth.registerButton': 'Register',
  'auth.hasAccount': 'Already have an account?',
  'auth.login': 'Login',
}
vi.mock('@/providers/i18n', () => ({
  useI18n: () => ({ t: (k: string) => DICT[k] ?? k }),
}))

function renderPage() {
  return render(
    <MemoryRouter>
      <RegisterPage />
    </MemoryRouter>,
  )
}

beforeEach(() => {
  auth.inviteOnly = true
  auth.bootstrap = true
})

describe('RegisterPage bootstrap messaging', () => {
  it('states that this registration is a one-time setup', () => {
    renderPage()
    const notice = screen.getByTestId('bootstrap-notice')
    expect(notice).toHaveTextContent('First-time setup · one-time only')
    expect(notice).toHaveTextContent('Registration closes automatically afterwards')
  })

  it('renders the form during bootstrap (the only registration allowed)', () => {
    renderPage()
    expect(screen.getByLabelText('Username')).toBeInTheDocument()
    expect(screen.getByTestId('bootstrap-notice')).toBeInTheDocument()
  })

  it('drops the notice and closes the form once an account exists', () => {
    auth.bootstrap = false
    renderPage()
    expect(screen.queryByTestId('bootstrap-notice')).toBeNull()
    expect(screen.getByText('Registration is invite-only')).toBeInTheDocument()
  })
})
