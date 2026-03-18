import { api } from '/static/js/api.js'
import { allProviderModels, filteredProviderModels } from '/static/js/utils.js'

/**
 * Registers the agentsPage Alpine.data component.
 *
 * @param {import('alpinejs').Alpine} Alpine
 */
export function register(Alpine) {
  Alpine.data('agentsPage', () => ({
    agents: [],
    providers: [],
    providerModels: {},

    showForm: false,
    editingId: null,
    form: {
      id: '', name: '', model: '', model_strong: '', model_fast: '',
      system_prompt: '', workspace: '', enabled: true,
    },

    confirmMsg: '',
    confirmAction: () => {},

    async init() {
      await Promise.all([
        this.loadAgents(),
        this.loadProvidersAndModels(),
      ])
    },

    // --- Model helpers (available to nested x-data scopes) ---

    allModels() {
      return allProviderModels(this.providers, this.providerModels)
    },

    filteredModels(search) {
      return filteredProviderModels(this.allModels(), search)
    },

    // --- Provider model loading ---

    async loadProvidersAndModels() {
      try {
        const list = await api('GET', '/api/providers') || []
        this.providers = list
        // Fetch models for each provider in parallel
        await Promise.all(list.map(async (p) => {
          try {
            const models = await api('POST', '/api/providers/' + p.id + '/models', {
              api_key: p.api_key,
              base_url: p.base_url,
            })
            this.providerModels[p.id] = models || []
          } catch (_) {
            // Silently skip providers that fail model fetch
          }
        }))
      } catch (e) {
        console.error(e)
      }
    },

    // --- Agent CRUD ---

    async loadAgents() {
      try {
        this.agents = await api('GET', '/api/agents') || []
      } catch (e) {
        console.error(e)
      }
    },

    resetForm() {
      this.form = {
        id: '', name: '', model: '', model_strong: '', model_fast: '',
        system_prompt: '', workspace: '', enabled: true,
      }
      this.editingId = null
      this.showForm = false
    },

    editAgent(a) {
      this.form = { ...a }
      this.editingId = a.id
      this.showForm = true
    },

    async saveAgent() {
      try {
        if (this.editingId) {
          await api('PUT', '/api/agents/' + this.editingId, this.form)
        } else {
          await api('POST', '/api/agents', this.form)
        }
        this.resetForm()
        await this.loadAgents()
        this.$store.toast.show('Saved')
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      }
    },

    async doDeleteAgent(id) {
      try {
        await api('DELETE', '/api/agents/' + id)
        await this.loadAgents()
        this.$store.toast.show('Deleted')
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      }
    },

    confirmDelete(msg, action) {
      this.confirmMsg = msg
      this.confirmAction = action
    },
  }))
}
