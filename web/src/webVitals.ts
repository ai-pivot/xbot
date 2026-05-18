/**
 * Web Vitals — lightweight performance metrics collection (dev-only logging).
 * FCP, LCP, CLS.
 */
interface VitalMetric {
  name: string
  value: number
  rating: 'good' | 'needs-improvement' | 'poor'
}

const isDev = import.meta.env?.DEV ?? false

function getRating(name: string, value: number): 'good' | 'needs-improvement' | 'poor' {
  const t: Record<string, [number, number]> = { FCP: [1800, 3000], LCP: [2500, 4000], CLS: [0.1, 0.25] }
  const [g, p] = t[name] ?? [Infinity, Infinity]
  return value <= g ? 'good' : value <= p ? 'needs-improvement' : 'poor'
}

export function initWebVitals(): void {
  if (!isDev) return
  const log = (m: VitalMetric) => {
    const e = m.rating === 'good' ? '✅' : m.rating === 'needs-improvement' ? '⚠️' : '❌'
    console.log(`[WebVitals] ${e} ${m.name}: ${m.value.toFixed(0)}ms (${m.rating})`)
  }
  try {
    const fcp = (performance.getEntriesByName('first-contentful-paint') as PerformancePaintTiming[])[0]
    if (fcp) log({ name: 'FCP', value: fcp.startTime, rating: getRating('FCP', fcp.startTime) })
    new PerformanceObserver((list) => {
      const last = list.getEntries().at(-1)
      if (last) log({ name: 'LCP', value: last.startTime, rating: getRating('LCP', last.startTime) })
    }).observe({ type: 'largest-contentful-paint', buffered: true })
    let cls = 0
    new PerformanceObserver((list) => {
      for (const e of list.getEntries()) {
        if (!(e as { hadRecentInput?: boolean }).hadRecentInput) {
          cls += (e as { value?: number }).value ?? 0
          log({ name: 'CLS', value: cls, rating: getRating('CLS', cls) })
        }
      }
    }).observe({ type: 'layout-shift', buffered: true })
  } catch { /* noop */ }
}

/** Dev-only render time measurement. Usage: const done = measureRender('X'); ... done(); */
export function measureRender(name: string): () => void {
  if (!isDev) return () => {}
  const t0 = performance.now()
  return () => { const ms = performance.now() - t0; if (ms > 16) console.warn(`[Perf] ${name}: ${ms.toFixed(1)}ms`) }
}
