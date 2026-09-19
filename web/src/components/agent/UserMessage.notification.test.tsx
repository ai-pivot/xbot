import { render } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { UserMessage } from './UserMessage'

/** 手机溢出真实根因：通知正文是 shell 命令，命令里成对 `$` 会被 remark-math 当公式。
 *  KaTeX 的 .katex-html 是 nowrap ⇒ 内容不换行 ⇒ 整页横向溢出（用户截图实证）。 */
const CMD = `[System Notification] Background task 7cffe5ac completed.
Command: ssh ubuntu@host 'echo CARGO_RC=$?; ls -l --time-style=+%H:%M:%S t; D=/tmp; S=$(ls /a/*.bin | paste -sd,) && env X=$D/x ./bin --shards "$S" > $D/o.log 2>&1'
Output:
CARGO_RC=127`

describe('UserMessage · 系统通知正文', () => {
  it('原样呈现：不得把 shell 命令送进 KaTeX/markdown（nowrap ⇒ 手机横向溢出）', () => {
    const { container } = render(
      <UserMessage content={CMD} isNotification />,
    )
    expect(container.querySelector('.katex')).toBeNull()
    expect(container.querySelector('.katex-html')).toBeNull()
    expect(container.textContent).toContain('echo CARGO_RC=$?')
    expect(container.textContent).toContain('$D/o.log')
  })

  it('普通用户消息仍走 markdown（数学/富文本不受影响）', () => {
    const { container } = render(
      <UserMessage content={'公式 $a^2+b^2=c^2$ 与 **粗体**'} />,
    )
    expect(container.querySelector('strong')).not.toBeNull()
  })
})
