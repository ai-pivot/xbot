/**
 * Escape regex special characters in a string.
 */
export function escapeRegex(str: string): string {
  return str.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
}

/**
 * Split text by search query, returning parts with match flags.
 * Used for search result highlighting.
 */
export function splitByQuery(text: string, query: string): { text: string; isMatch: boolean }[] {
  if (!query.trim()) return [{ text, isMatch: false }]
  const escaped = escapeRegex(query)
  const regex = new RegExp(`(${escaped})`, 'gi')
  const parts = text.split(regex)
  if (parts.length <= 1) return [{ text, isMatch: false }]
  return parts.map(part => ({
    text: part,
    isMatch: part.toLowerCase() === query.toLowerCase(),
  }))
}

/**
 * Count occurrences of query in text (case-insensitive).
 */
export function countMatches(text: string, query: string): number {
  if (!query.trim()) return 0
  const escaped = escapeRegex(query)
  const regex = new RegExp(escaped, 'gi')
  const matches = text.match(regex)
  return matches ? matches.length : 0
}
