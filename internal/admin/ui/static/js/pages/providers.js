import { api } from '/static/js/api.js'

const providerDefaults = {
  'anthropic': { base_url: 'https://api.anthropic.com', name: 'Anthropic' },
  'openai': { base_url: 'https://api.openai.com/v1', name: 'OpenAI' },
  'openai-response': { base_url: 'https://api.openai.com/v1', name: 'OpenAI Response' },
}

/**
 * Registers the providersPage Alpine.data component.
 *
 * @param {import('alpinejs').Alpine} Alpine
 */
export function register(Alpine) {
  Alpine.data('providersPage', () => ({
    providers: [],
    providerModels: {},
    newProviderType: '',
    providerDefaults,

    confirmMsg: '',
    confirmAction: () => {},

    get addableProviders() {
      const existing = new Set(this.providers.map(p => p.id))
      return Object.keys(providerDefaults).filter(p => !existing.has(p))
    },

    async init() {
      await this.loadProviders()
      this.newProviderType = this.addableProviders[0] || ''
    },

    async loadProviders() {
      try {
        const list = await api('GET', '/api/providers') || []
        this.providers = list.map(p => ({
          ...p,
          _fetching: false,
          _modelCount: 0,
          _showModels: false,
        }))
      } catch (e) {
        console.error(e)
      }
    },

    async addProvider() {
      if (!this.newProviderType) return
      const d = providerDefaults[this.newProviderType] || {}
      try {
        await api('POST', '/api/providers', {
          id: this.newProviderType,
          name: d.name || this.newProviderType,
          api_key: '',
          base_url: '',
        })
        await this.loadProviders()
        this.newProviderType = this.addableProviders[0] || ''
        this.$store.toast.show('Provider added')
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      }
    },

    async saveProvider(p) {
      try {
        await api('PUT', '/api/providers/' + p.id, {
          id: p.id,
          name: p.name,
          api_key: p.api_key,
          base_url: p.base_url,
        })
        await this.loadProviders()
        this.$store.toast.show('Saved')
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      }
    },

    async doDeleteProvider(id) {
      try {
        await api('DELETE', '/api/providers/' + id)
        await this.loadProviders()
        this.newProviderType = this.addableProviders[0] || ''
        this.$store.toast.show('Deleted')
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      }
    },

    async fetchModels(p) {
      p._fetching = true
      try {
        const models = await api('POST', '/api/providers/' + p.id + '/models', {
          api_key: p.api_key,
          base_url: p.base_url,
        })
        this.providerModels[p.id] = models || []
        p._modelCount = this.providerModels[p.id].length
        this.$store.toast.show(p._modelCount + ' models fetched')
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      } finally {
        p._fetching = false
      }
    },

    confirmDelete(msg, action) {
      this.confirmMsg = msg
      this.confirmAction = action
    },
  }))
}
