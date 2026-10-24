/**
 * HumanError —— 把后端/LLM 的原始报错翻成「人话」再展示。
 *
 * 现象（用户 2026-10-22 截图）：聊天气泡里直接印着
 *   `dial tcp 127.0.0.1:9999: connect: connection refused #1 #2 #3 #4 #5`
 * —— 普通用户看不出这是「模型服务连不上」，也不知道该做什么。
 *
 * 规则：错误气泡 = 一句人话（发生了什么 + 已经重试几次）+ 一个「重试」按钮 +
 * **默认折叠**的技术详情（原始报错原文留在里面，排障时能看）。
 * 同类原始报错都要走这个格式，不在 UI 上直接暴露底层串。
 */
import { useRetryLastTurn } from './retryContext'

export type ErrorKind =
  | 'unavailable'
  | 'timeout'
  | 'rate_limited'
  | 'auth'
  | 'network'
  | 'unknown'

export interface HumanError {
  kind: ErrorKind
  /** 一句人话标题（不含技术串）。 */
  title: string
  /** 下一步建议（普通人能执行）。 */
  hint: string
  /** 后端已经自动重试过几次（识别不到就是 0）。 */
  attempts: number
  /** 原始报错（只在「技术详情」里展示）。 */
  raw: string
}

interface Rule {
  kind: ErrorKind
  test: RegExp
  title: string
  hint: string
}

/** 顺序有意义：先具体（连不上）再笼统（5xx）。 */
const RULES: Rule[] = [
  {
    kind: 'network',
    test: /connection refused|ECONNREFUSED|dial tcp|no route to host|network is unreachable/i,
    title: '模型服务暂不可用',
    hint: '请求没有到达模型服务，通常是服务没起来或地址/端口不对。稍后点「重试」；一直这样请联系管理员检查服务状态。',
  },
  {
    kind: 'timeout',
    test: /context deadline exceeded|i\/o timeout|timeout awaiting response|ETIMEDOUT|deadline_exceeded/i,
    title: '模型服务响应超时',
    hint: '请求发出去了但等不到回应。可能是负载过高，稍后重试；连续超时请联系管理员扩容或换模型。',
  },
  {
    kind: 'rate_limited',
    test: /rate limit|too many requests|quota|429/i,
    title: '模型服务限流了',
    hint: '短时间内请求太多或额度用尽。等一会儿再试，或换一个订阅/模型。',
  },
  {
    kind: 'auth',
    test: /401|403|unauthorized|invalid api key|invalid_api_key|permission denied/i,
    title: '模型服务鉴权失败',
    hint: '当前订阅的密钥无效或没有权限。请在设置里检查订阅配置。',
  },
  {
    kind: 'unavailable',
    test: /\b(500|502|503|504)\b|service unavailable|bad gateway|internal server error|overloaded/i,
    title: '模型服务暂不可用',
    hint: '服务端处理失败。稍后点「重试」；持续失败请联系管理员。',
  },
  {
    kind: 'unknown',
    test: /(^|\n)\s*(error|failed|exception)\b|模型调用失败|llm.*(error|failed)/i,
    title: '这一轮没能完成',
    hint: '可以点「重试」重新发一次；如果反复失败，展开下面的技术详情反馈给管理员。',
  },
]

/** 从原始报错里抠出「已重试 N 次」：兼容 `#1 … #5`、`重试 5 次`、`retry 5`。 */
export function retryCountOf(raw: string): number {
  const explicit = raw.match(/(?:已重试|重试|retr(?:y|ies))[^\d]{0,6}(\d+)/i)
  if (explicit) return Math.min(99, Number(explicit[1]))
  const hashes = [...raw.matchAll(/#(\d+)/g)].map((m) => Number(m[1])).filter(Number.isFinite)
  if (hashes.length > 0) return Math.min(99, Math.max(...hashes))
  return 0
}

/**
 * 这段文本是不是一条「可以直接给人看」的错误？
 *
 * 只有**整段**基本就是报错时才拦截 —— 正常回复里引用了 `connection refused`
 * 这类字样不能被误伤（否则会把一段正常的技术讨论变成错误卡片）。
 */
/**
 * 这段文本像不像「人写的正文」？
 *
 * 只看特征串会把「我把 connection refused 加进了重试规则，现在会退避重试。」
 * 这种正常回复误判成错误卡。判据：中文占比高 + 带句子标点（。！？；）
 * ⇒ 是正文。带 `#1` / `已重试` 的仍然按报错处理（那是机器拼出来的）。
 */
function looksLikeProse(raw: string): boolean {
  const cjk = raw.match(/[\u4e00-\u9fa5]/g) ?? []
  if (cjk.length === 0) return false
  const ratio = cjk.length / Math.max(1, raw.length)
  if (ratio <= 0.25) return false
  if (!/[。！？；]/.test(raw)) return false
  return !/(#\d|已重试|retr(?:y|ies)\s*\d)/i.test(raw)
}

export function classifyError(text: string): HumanError | null {
  const raw = (text ?? '').trim()
  if (!raw) return null
  // 长文/多段正文 = 正常回复，不拦。
  if (raw.length > 1200) return null
  const paragraphs = raw.split(/\n\s*\n/).filter((p) => p.trim())
  if (paragraphs.length > 2) return null
  // 正常回复常见的 Markdown 结构（标题/列表/代码块）说明不是纯报错。
  if (/^#{1,6}\s|\n[-*]\s|\n```/m.test(raw)) return null
  // 中文长句 + 句子标点 = 人在写正文，不是机器报错。
  if (looksLikeProse(raw)) return null

  const rule = RULES.find((r) => r.test.test(raw))
  if (!rule) return null

  const attempts = retryCountOf(raw)
  const suffix = attempts > 1 ? `（已重试 ${attempts} 次）` : ''
  return {
    kind: rule.kind,
    title: `${rule.title}${suffix}`,
    hint: rule.hint,
    attempts,
    raw,
  }
}

interface Props {
  error: HumanError
  /** 覆盖默认的重试动作（默认取 context）。 */
  onRetry?: () => void
}

export function HumanErrorNotice({ error, onRetry }: Props) {
  const ctxRetry = useRetryLastTurn()
  const retry = onRetry ?? ctxRetry
  return (
    <div
      data-testid="human-error"
      data-error-kind={error.kind}
      className="rounded-lg border border-lane-waiting/40 bg-lane-waiting/8 px-3 py-2"
    >
      <div className="flex items-start gap-2">
        <span aria-hidden className="mt-[1px] text-[13px]">⚠️</span>
        <div className="min-w-0 flex-1">
          <div className="text-[13px] font-medium text-text-primary">{error.title}</div>
          <div className="mt-0.5 text-[11px] leading-5 text-text-secondary">{error.hint}</div>
        </div>
        {retry ? (
          <button
            type="button"
            data-testid="human-error-retry"
            onClick={retry}
            title="重新发送上一条消息"
            className="shrink-0 rounded-md bg-app-accent px-2.5 py-1 text-[11px] font-medium text-app-accent-foreground hover:bg-app-accent-hover"
          >
            重试
          </button>
        ) : null}
      </div>
      <details data-testid="human-error-detail" className="mt-1.5">
        <summary className="cursor-pointer select-none text-[10px] text-text-muted hover:text-text-secondary">
          技术详情
        </summary>
        <pre className="mt-1 max-h-40 overflow-auto whitespace-pre-wrap rounded-md bg-bg-tertiary p-2 font-mono text-[9.5px] leading-4 text-text-secondary">
          {error.raw}
        </pre>
      </details>
    </div>
  )
}
