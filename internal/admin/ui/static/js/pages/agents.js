import { api } from '/static/js/api.js'

/**
 * Registers the agentsPage Alpine.data component.
 *
 * @param {import('alpinejs').Alpine} Alpine
 */
export function register(Alpine) {
  Alpine.data('agentsPage', () => ({
    agents: [],
    cachedModels: [],
    isAdmin: false,
    allUsers: [],

    showForm: false,
    editingId: null,
    form: {
      id: '', name: '', model: '', model_strong: '', model_fast: '',
      system_prompt: '', workspace: '', scope: 'system', enabled: true,
    },

    // User assignment modal state.
    showUserModal: false,
    userModalAgent: '',
    assignedUsers: [],
    addUserId: '',

    confirmMsg: '',
    confirmAction: () => {},

    async init() {
      await Promise.all([
        this.loadAgents(),
        this.loadCachedModels(),
        this.loadCurrentUser(),
      ])
    },

    async loadCurrentUser() {
      try {
        const me = await api('GET', '/api/auth/me')
        this.isAdmin = me.is_admin || false
        if (this.isAdmin) {
          await this.loadAllUsers()
        }
      } catch (_) {
        this.isAdmin = false
      }
    },

    async loadAllUsers() {
      try {
        const users = await api('GET', '/api/auth/users')
        this.allUsers = users || []
      } catch (_) {
        // admin-only endpoint, silently fail for non-admins
      }
    },

    // --- Cached models for autocomplete (no live API calls) ---

    async loadCachedModels() {
      try {
        const models = await api('GET', '/api/models') || []
        this.cachedModels = models.map(m => m.provider + '/' + m.model)
      } catch (_) {
        // Cache may not exist yet — model fields still work as plain text
      }
    },

    filteredModels(search) {
      if (!search) return this.cachedModels
      const q = search.toLowerCase()
      return this.cachedModels.filter(m => m.toLowerCase().includes(q))
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
        system_prompt: '', workspace: '', scope: 'system', enabled: true,
      }
      this.editingId = null
      this.showForm = false
    },

    editAgent(a) {
      this.form = { ...a, scope: a.scope || 'system' }
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

    // --- User assignment management ---

    async manageUsers(agent) {
      this.userModalAgent = agent.id
      this.addUserId = ''
      await this.loadAssignedUsers(agent.id)
      this.showUserModal = true
    },

    async loadAssignedUsers(agentId) {
      try {
        this.assignedUsers = await api('GET', '/api/agents/' + agentId + '/users') || []
      } catch (e) {
        this.assignedUsers = []
        console.error(e)
      }
    },

    get availableUsers() {
      const assignedIds = new Set(this.assignedUsers.map(u => u.id))
      return this.allUsers.filter(u => !assignedIds.has(u.id))
    },

    async addUser() {
      if (!this.addUserId) return
      try {
        await api('POST', '/api/agents/' + this.userModalAgent + '/users', {
          user_id: Number(this.addUserId),
        })
        this.addUserId = ''
        await this.loadAssignedUsers(this.userModalAgent)
        this.$store.toast.show('User assigned')
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      }
    },

    async removeUser(userId) {
      try {
        await api('DELETE', '/api/agents/' + this.userModalAgent + '/users/' + userId)
        await this.loadAssignedUsers(this.userModalAgent)
        this.$store.toast.show('User removed')
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      }
    },
  }))
}
