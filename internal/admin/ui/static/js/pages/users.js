import { api } from '/static/js/api.js'

/**
 * Registers the usersPage Alpine.data component.
 *
 * @param {import('alpinejs').Alpine} Alpine
 */
export function register(Alpine) {
  Alpine.data('usersPage', () => ({
    users: [],
    agents: [],
    userMemories: {},

    confirmMsg: '',
    confirmAction: () => {},

    async init() {
      await Promise.all([
        this.loadUsers(),
        this.loadAgents(),
      ])
    },

    async loadUsers() {
      try {
        const list = await api('GET', '/api/users') || []
        this.users = list.map(u => ({
          ...u,
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

    async loadAgents() {
      try {
        this.agents = await api('GET', '/api/agents') || []
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
        const u = this.users.find(u => u.id === userId)
        if (u) await this.loadUserMemories(u)
        this.$store.toast.show('Saved')
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      }
    },

    async doDeleteUserMemory(userId, agentId) {
      try {
        await api('DELETE', '/api/users/' + userId + '/memories/' + agentId)
        const u = this.users.find(u => u.id === userId)
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
