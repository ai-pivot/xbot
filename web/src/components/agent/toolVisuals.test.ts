/**
 * 分类色映射守护（用户 2026-09-19：「折叠版本的 icon 搞好看点」）。
 *
 * share_file 是**内置**工具，此前既没有专属 glyph（落到 `Wrench` 兜底 = "未知工具"）
 * 也没有分类（`toolCategory` 落到 `sys` 兜底 = 灰蓝静音色）—— 在一排工具 pill 里
 * 看起来像"未分类/不认识"。内置工具必须两者都有。
 */
import { describe, expect, it } from 'vitest'

import { CATEGORY_COLOR, toolCategory } from '@/components/agent/toolVisuals'

describe('toolCategory · share_file', () => {
  it('归类为 write（产出可分享的文件产物），不再落到 sys 兜底', () => {
    const cat = toolCategory('share_file')
    expect(cat).toBe('write')
    // sys 是"未分类"兜底色（灰蓝静音）—— 落到那里等于没有分类
    expect(CATEGORY_COLOR[cat]).not.toBe(CATEGORY_COLOR.sys)
  })

  it('未知工具仍回落 sys（兜底语义不变）', () => {
    expect(toolCategory('some_unknown_mcp_tool')).toBe('sys')
  })
})
