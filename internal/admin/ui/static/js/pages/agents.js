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
    currentUserId: 0,
    allUsers: [],

    showForm: false,
    editingId: null,
    form: {
      name: '', model: '', model_strong: '', model_fast: '',
      system_prompt: '', scope: 'system', enabled: true,
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
      this.focusAgentFromURL()
    },

    async loadCurrentUser() {
      try {
        const me = await api('GET', '/api/auth/me')
        this.isAdmin = me.is_admin || false
        this.currentUserId = me.user_id || 0
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

    canEditAgent(a) {
      return this.isAdmin || (a.creator_id !== 0 && a.creator_id === this.currentUserId)
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
        const agents = await api('GET', '/api/agents') || []
        const requestedAgent = this.requestedAgentID()
        this.agents = agents.map(a => ({
          ...a,
          _highlight: a.id === requestedAgent,
          _showMemory: false,
          _soul: '',
          _soulDraft: '',
          _profile: '',
          _profileDraft: '',
          _memoryLoaded: false,
        }))
      } catch (e) {
        console.error(e)
      }
    },

    requestedAgentID() {
      return new URLSearchParams(window.location.search).get('agent') || ''
    },

    focusAgentFromURL() {
      const requestedAgent = this.requestedAgentID()
      if (!requestedAgent) return
      requestAnimationFrame(() => {
        const el = Array.from(document.querySelectorAll('[data-agent-id]'))
          .find(node => node.dataset.agentId === requestedAgent)
        if (el) {
          el.scrollIntoView({ block: 'center' })
        }
      })
    },

    resetForm() {
      this.form = {
        name: '', model: '', model_strong: '', model_fast: '',
        system_prompt: '', scope: 'system', enabled: true,
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

    // --- Per-user personalisation (soul + profile for each agent) ---

    async toggleMemory(a) {
      a._showMemory = !a._showMemory
      if (a._showMemory && !a._memoryLoaded) {
        await this.loadMyMemory(a)
      }
    },

    async loadMyMemory(a) {
      try {
        const mems = await api('GET', '/api/auth/profile/memories') || []
        const mem = mems.find(m => m.agent_id === a.id)
        a._soul = mem ? mem.soul : ''
        a._soulDraft = a._soul
        a._profile = mem ? mem.content : ''
        a._profileDraft = a._profile
        a._memoryLoaded = true
      } catch (e) {
        a._soul = ''
        a._soulDraft = ''
        a._profile = ''
        a._profileDraft = ''
        a._memoryLoaded = true
      }
    },

    async saveMySoul(a) {
      try {
        await api('PUT', '/api/auth/profile/soul/' + a.id, { soul: a._soulDraft })
        a._soul = a._soulDraft
        this.$store.toast.show('Soul saved')
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      }
    },

    async saveMyProfile(a) {
      try {
        await api('PUT', '/api/auth/profile/memories/' + a.id, { content: a._profileDraft })
        a._profile = a._profileDraft
        this.$store.toast.show('Profile saved')
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      }
    },
  }))
}
