import { api } from '/static/js/api.js'

/**
 * Registers the agentsPage Alpine.data component.
 *
 * @param {import('alpinejs').Alpine} Alpine
 */
export function register(Alpine) {
  Alpine.data('agentsPage', () => ({
    agents: [],

    showForm: false,
    editingId: null,
    form: {
      id: '', name: '', model: '', model_strong: '', model_fast: '',
      system_prompt: '', workspace: '', enabled: true,
    },

    confirmMsg: '',
    confirmAction: () => {},

    async init() {
      await this.loadAgents()
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
