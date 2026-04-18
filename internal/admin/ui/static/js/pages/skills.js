import { api } from '/static/js/api.js'

/**
 * Registers the skillsPage Alpine.data component.
 *
 * @param {import('alpinejs').Alpine} Alpine
 */
export function register(Alpine) {
  Alpine.data('skillsPage', () => ({
    skills: [],
    loading: false,
    saving: false,
    fileLoading: false,
    selected: null,
    draft: {
      description: '',
      status: 'active',
      disable_model_invocation: false,
      skill_md: '',
    },

    // Install modal state.
    installModalOpen: false,
    installStage: 'search', // 'search' | 'config'
    searchQuery: '',
    searchResults: [],
    searching: false,
    installSource: '',
    installScope: 'system',
    installUserID: 0,
    installAgentID: '',
    installing: false,
    installAgents: [],
    installUsers: [],

    async init() {
      await this.loadSkills()
    },

    async loadSkills() {
      this.loading = true
      try {
        this.skills = (await api('GET', '/api/skills')) || []
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      } finally {
        this.loading = false
      }
    },

    skillsByScope(scope) {
      return this.skills.filter(s => s.scope === scope)
    },

    async selectSkill(sk) {
      this.selected = sk
      this.draft = {
        description: sk.description || '',
        status: sk.status || 'active',
        disable_model_invocation: !!sk.disable_model_invocation,
        skill_md: '',
      }
      this.fileLoading = true
      try {
        const res = await api('GET', '/api/skills/' + sk.id + '/file?path=SKILL.md')
        this.draft.skill_md = res?.content || ''
      } catch {
        this.draft.skill_md = ''
      } finally {
        this.fileLoading = false
      }
    },

    async saveSkill() {
      if (!this.selected) {
        return
      }
      this.saving = true
      try {
        await api('PUT', '/api/skills/' + this.selected.id, {
          description: this.draft.description,
          status: this.draft.status,
          disable_model_invocation: this.draft.disable_model_invocation,
          files: { 'SKILL.md': this.draft.skill_md },
        })
        this.$store.toast.show('Saved')
        await this.loadSkills()
        const refreshed = this.skills.find(s => s.id === this.selected.id)
        if (refreshed) {
          this.selected = refreshed
        }
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      } finally {
        this.saving = false
      }
    },

    async confirmDelete() {
      if (!this.selected) {
        return
      }
      if (!confirm('Delete skill "' + this.selected.name + '"? This cannot be undone.')) {
        return
      }
      try {
        await api('DELETE', '/api/skills/' + this.selected.id)
        this.$store.toast.show('Skill deleted')
        this.selected = null
        await this.loadSkills()
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      }
    },

    // --- Install modal ---

    async openInstallModal() {
      this.installModalOpen = true
      this.installStage = 'search'
      this.searchQuery = ''
      this.searchResults = []
      this.searching = false
      this.installSource = ''
      this.installScope = 'system'
      this.installUserID = 0
      this.installAgentID = ''
      this.installing = false
      // Fetch agents and users in parallel; silently ignore errors.
      const [agents, users] = await Promise.allSettled([
        api('GET', '/api/agents'),
        api('GET', '/api/auth/users'),
      ])
      this.installAgents = agents.status === 'fulfilled' ? (agents.value || []) : []
      this.installUsers = users.status === 'fulfilled' ? (users.value || []) : []
    },

    async doSearch() {
      const q = this.searchQuery.trim()
      if (!q) {
        this.searchResults = []
        return
      }
      this.searching = true
      try {
        const results = await api('GET', '/api/skills/search?q=' + encodeURIComponent(q) + '&limit=20')
        this.searchResults = results || []
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
        this.searchResults = []
      } finally {
        this.searching = false
      }
    },

    pickSkill(s) {
      this.installSource = s.source + '@' + s.skillId
      this.installStage = 'config'
    },

    async doInstall() {
      if (this.installing) return
      const body = {
        source: this.installSource,
        scope: this.installScope,
      }
      if (this.installScope === 'user') {
        body.user_id = Number(this.installUserID)
      } else if (this.installScope === 'agent') {
        body.agent_id = this.installAgentID
      }
      this.installing = true
      try {
        const res = await api('POST', '/api/skills/install', body)
        this.$store.toast.show('Installed: ' + (res?.name || 'skill'))
        this.installModalOpen = false
        await this.loadSkills()
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      } finally {
        this.installing = false
      }
    },
  }))
}
