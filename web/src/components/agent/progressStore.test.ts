import { describe, expect, it } from 'vitest'
import { continuousIterations } from './progressStore'
import type { WebIteration } from '@/types/shared'

function iters(nums: number[]): WebIteration[] {
  return nums.map((n) => ({ iteration: n, thinking: '', reasoning: '', content: '', tools: [], toolCount: 0 }))
}

describe('continuousIterations — linear-consistency guard', () => {
  it('keeps a fully contiguous sequence as-is', () => {
    expect(continuousIterations(iters([1, 2, 3])).map((i) => i.iteration)).toEqual([1, 2, 3])
  })

  it('truncates at the first gap (weak network dropped iteration 2)', () => {
    // delta for iteration 2 lost before restoreActiveProgress backfills it
    expect(continuousIterations(iters([1, 3, 4])).map((i) => i.iteration)).toEqual([1])
  })

  it('renders nothing when the sequence does not start at 1', () => {
    expect(continuousIterations(iters([2, 3])).map((i) => i.iteration)).toEqual([])
  })

  it('handles empty and single-iteration input', () => {
    expect(continuousIterations([])).toEqual([])
    expect(continuousIterations(iters([1])).map((i) => i.iteration)).toEqual([1])
  })

  it('is order-independent (sorts before truncating)', () => {
    expect(continuousIterations(iters([3, 1, 2])).map((i) => i.iteration)).toEqual([1, 2, 3])
  })
})
