import { createRoot } from 'react-dom/client'
import './index.css'
import '@/i18n' // initialize i18next (side-effect import)
import App from '@/App'
import { AuthProvider } from '@/providers/AuthProvider'
import { ThemeProvider } from '@/providers/theme'
import { I18nProvider } from '@/providers/i18n'

createRoot(document.getElementById('root')!).render(
  <AuthProvider>
    <ThemeProvider>
      <I18nProvider>
        <App />
      </I18nProvider>
    </ThemeProvider>
  </AuthProvider>,
)
