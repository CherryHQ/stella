/**
 * Registers the global theme Alpine store.
 *
 * Persists the selected daisyUI theme to localStorage and applies it
 * to the <html> element via the data-theme attribute.
 *
 * Usage in Alpine templates:
 *   $store.theme.set('dark')
 *   $store.theme.current  // e.g. "terra"
 *
 * @param {import('alpinejs').Alpine} Alpine
 */
export function registerTheme(Alpine) {
  const STORAGE_KEY = 'anna-theme'
  const DEFAULT_THEME = 'terra'

  Alpine.store('theme', {
    current: localStorage.getItem(STORAGE_KEY) || DEFAULT_THEME,

    set(name) {
      this.current = name
      document.documentElement.setAttribute('data-theme', name)
      localStorage.setItem(STORAGE_KEY, name)
    },
  })
}
