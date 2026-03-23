import { api } from '/static/js/api.js'

/**
 * Registers the usersPage Alpine.data component.
 *
 * @param {import('alpinejs').Alpine} Alpine
 */
export function register(Alpine) {
  Alpine.data('usersPage', () => ({
    tab: 'auth',
    currentUserId: 0,

    // Auth users state.
    authUsers: [],
    selectedUser: null,
    userAgentIds: [],
    agents: [],
    addAgentId: '',

    // Legacy memory tab state.
    legacyUsers: [],
    userMemories: {},

    confirmMsg: '',
    confirmAction: () => {},

    async init() {
      await Promise.all([
        this.loadCurrentUser(),
        this.loadAuthUsers(),
        this.loadAgents(),
      ])
      this.$watch('tab', (val) => {
        if (val === 'memory') this.loadLegacyUsers()
      })
    },

    async loadCurrentUser() {
      try {
        const me = await api('GET', '/api/auth/me')
        this.currentUserId = me.id || 0
      } catch (_) {
        // ignore
      }
    },

    // --- Auth Users Tab ---

    async loadAuthUsers() {
      try {
        this.authUsers = await api('GET', '/api/auth/users') || []
      } catch (e) {
        console.error(e)
      }
    },

    async selectUser(u) {
      // Reload fresh data for the user.
      try {
        this.selectedUser = await api('GET', '/api/auth/users/' + u.id)
        await this.loadUserAgents(u.id)
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      }
    },

    async loadUserAgents(userId) {
      try {
        this.userAgentIds = await api('GET', '/api/auth/users/' + userId + '/agents') || []
        this.addAgentId = ''
      } catch (e) {
        this.userAgentIds = []
      }
    },

    get availableAgents() {
      const assigned = new Set(this.userAgentIds)
      return this.agents.filter(a => !assigned.has(a.id))
    },

    async setRole(role) {
      if (!this.selectedUser) return
      try {
        await api('PUT', '/api/auth/users/' + this.selectedUser.id + '/role', { role })
        this.selectedUser = await api('GET', '/api/auth/users/' + this.selectedUser.id)
        await this.loadAuthUsers()
        this.$store.toast.show('Role updated to ' + role)
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      }
    },

    async toggleActive() {
      if (!this.selectedUser) return
      const newActive = !this.selectedUser.is_active
      try {
        await api('PUT', '/api/auth/users/' + this.selectedUser.id + '/active', {
          is_active: newActive,
        })
        this.selectedUser = await api('GET', '/api/auth/users/' + this.selectedUser.id)
        await this.loadAuthUsers()
        this.$store.toast.show(newActive ? 'User activated' : 'User deactivated')
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      }
    },

    async addAgentToUser() {
      if (!this.selectedUser || !this.addAgentId) return
      const newIds = [...this.userAgentIds, this.addAgentId]
      try {
        await api('PUT', '/api/auth/users/' + this.selectedUser.id + '/agents', {
          agent_ids: newIds,
        })
        await this.loadUserAgents(this.selectedUser.id)
        this.$store.toast.show('Agent assigned')
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      }
    },

    async unlinkIdentity(identityId) {
      if (!this.selectedUser) return
      try {
        await api('DELETE', '/api/auth/users/' + this.selectedUser.id + '/identities/' + identityId)
        // Refresh user detail.
        this.selectedUser = await api('GET', '/api/auth/users/' + this.selectedUser.id)
        await this.loadAuthUsers()
        this.$store.toast.show('Identity unlinked')
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      }
    },

    async setNotifyIdentity(identityId) {
      if (!this.selectedUser) return
      try {
        const val = identityId ? parseInt(identityId, 10) : null
        await api('PUT', '/api/users/' + this.selectedUser.id + '/notify-identity', {
          notify_identity_id: val,
        })
        this.selectedUser = await api('GET', '/api/auth/users/' + this.selectedUser.id)
        this.$store.toast.show('Notify channel updated')
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      }
    },

    async removeAgentFromUser(agentId) {
      if (!this.selectedUser) return
      const newIds = this.userAgentIds.filter(id => id !== agentId)
      try {
        await api('PUT', '/api/auth/users/' + this.selectedUser.id + '/agents', {
          agent_ids: newIds,
        })
        await this.loadUserAgents(this.selectedUser.id)
        this.$store.toast.show('Agent removed')
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      }
    },

    // --- Memory Tab ---

    async loadAgents() {
      try {
        this.agents = await api('GET', '/api/agents') || []
      } catch (e) {
        console.error(e)
      }
    },

    async loadLegacyUsers() {
      if (this.legacyUsers.length > 0) return // already loaded
      try {
        const list = await api('GET', '/api/auth/users') || []
        this.legacyUsers = list.map(u => ({
          ...u,
          name: u.username,
          _defaultAgent: u.default_agent_id || '',
          _showMemory: false,
          _memoryCount: 0,
          _showAddMemory: false,
          _newMemoryAgent: '',
          _newMemoryContent: '',
        }))
      } catch (e) {
        console.error(e)
      }
    },

    async toggleUserMemory(u) {
      u._showMemory = !u._showMemory
      if (u._showMemory) await this.loadUserMemories(u)
    },

    async loadUserMemories(u) {
      try {
        const mems = await api('GET', '/api/users/' + u.id + '/memories') || []
        this.userMemories[u.id] = mems.map(m => ({ ...m, _content: m.content }))
        u._memoryCount = mems.length
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      }
    },

    async saveUserDefaultAgent(u) {
      try {
        await api('PUT', '/api/users/' + u.id, { default_agent_id: u._defaultAgent })
        u.default_agent_id = u._defaultAgent
        this.$store.toast.show('Saved')
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      }
    },

    async saveUserMemory(userId, agentId, content) {
      try {
        await api('PUT', '/api/users/' + userId + '/memories/' + agentId, { content })
        const u = this.legacyUsers.find(u => u.id === userId)
        if (u) await this.loadUserMemories(u)
        this.$store.toast.show('Saved')
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      }
    },

    async doDeleteUserMemory(userId, agentId) {
      try {
        await api('DELETE', '/api/users/' + userId + '/memories/' + agentId)
        const u = this.legacyUsers.find(u => u.id === userId)
        if (u) await this.loadUserMemories(u)
        this.$store.toast.show('Deleted')
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      }
    },

    async addUserMemory(u) {
      if (!u._newMemoryAgent || !u._newMemoryContent) return
      try {
        await api('PUT', '/api/users/' + u.id + '/memories/' + u._newMemoryAgent, {
          content: u._newMemoryContent,
        })
        u._showAddMemory = false
        u._newMemoryAgent = ''
        u._newMemoryContent = ''
        await this.loadUserMemories(u)
        this.$store.toast.show('Added')
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
