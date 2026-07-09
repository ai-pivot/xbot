/**
 * Test render helpers (Spec 4).
 *
 * Wraps rendered components in the providers they need (i18n) so tests do not
 * each repeat the boilerplate. i18next is initialized via the side-effect
 * import of '@/i18n' (same as main.tsx).
 */
import type { ReactElement, ReactNode } from 'react'
import { render } from '@testing-library/react'

import '@/i18n'
import { I18nProvider } from '@/providers/i18n'

function withProviders(node: ReactNode): ReactElement {
  return <I18nProvider>{node}</I18nProvider>
}

export function renderWithProviders(node: ReactNode) {
  return render(withProviders(node))
}
