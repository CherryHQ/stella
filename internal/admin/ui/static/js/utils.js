/**
 * Shared ESM utilities for admin pages.
 */

/**
 * Formats a timestamp into a human-readable relative time string.
 *
 * @param {string} ts - ISO 8601 or "YYYY-MM-DD HH:MM" timestamp.
 * @returns {string} Relative time (e.g. "5m ago", "2h ago") or formatted date.
 */
export function formatTime(ts) {
  if (!ts) return ''
  try {
    let d = new Date(ts)
    if (isNaN(d.getTime()) && /^\d{4}-\d{2}-\d{2} \d{2}:\d{2}/.test(ts)) {
      d = new Date(ts.replace(' ', 'T') + 'Z')
    }
    if (isNaN(d.getTime())) return ts
    const now = new Date()
    const ms = now - d
    const min = Math.floor(ms / 60000)
    if (min < 1) return 'just now'
    if (min < 60) return min + 'm ago'
    const hr = Math.floor(min / 60)
    if (hr < 24) return hr + 'h ago'
    const day = Math.floor(hr / 24)
    if (day < 7) return day + 'd ago'
    return d.toLocaleDateString()
  } catch (_) {
    return ts
  }
}

/**
 * Builds a deduplicated list of "provider/model" strings from providers and
 * their fetched model lists.
 *
 * @param {Array<{id: string}>} providers - Provider objects.
 * @param {Object<string, string[]>} providerModels - Map of provider ID to model names.
 * @returns {string[]} Sorted list of "provider/model" references.
 */
export function allProviderModels(providers, providerModels) {
  const all = []
  const seen = new Set()
  for (const p of providers) {
    for (const m of (providerModels[p.id] || [])) {
      const ref = p.id + '/' + m
      if (!seen.has(ref)) {
        seen.add(ref)
        all.push(ref)
      }
    }
  }
  return all
}

/**
 * Filters a list of model references by a search query.
 *
 * @param {string[]} models - List of "provider/model" strings.
 * @param {string} search - Search query to filter by.
 * @returns {string[]} Filtered model references.
 */
export function filteredProviderModels(models, search) {
  if (!search) return models
  const q = search.toLowerCase()
  return models.filter(m => m.toLowerCase().includes(q))
}
