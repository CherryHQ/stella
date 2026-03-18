import { api } from '/static/js/api.js'

/**
 * Registers the settingsPage Alpine.data component.
 *
 * @param {import('alpinejs').Alpine} Alpine
 */
export function register(Alpine) {
  Alpine.data('settingsPage', () => ({
    settingsKeys: ['runner', 'compaction', 'heartbeat', 'scheduler', 'plugins'],
    settingsEditors: {},

    async init() {
      await this.loadSettings()
    },

    async loadSettings() {
      for (const key of this.settingsKeys) {
        try {
          const r = await api('GET', '/api/settings/' + key)
          this.settingsEditors[key] =
            typeof r.value === 'object'
              ? JSON.stringify(r.value, null, 2)
              : (r.value || '')
        } catch (_) {
          this.settingsEditors[key] = ''
        }
      }
    },

    async saveSetting(key) {
      try {
        let val = this.settingsEditors[key]
        try { val = JSON.parse(val) } catch (_) { /* keep as string */ }
        await api('PUT', '/api/settings/' + key, { value: val })
        this.$store.toast.show(key + ' saved')
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      }
    },
  }))
}
