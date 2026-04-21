import { api } from '/static/js/api.js'

function parseChannelConfig(raw) {
  try { return JSON.parse(raw || '{}') } catch { return {} }
}

function normalizeChannel(ch) {
  return {
    ...ch,
    type: ch.type || ch.id,
    agent_id: ch.agent_id || '',
    config: ch.config || '{}',
    _config: parseChannelConfig(ch.config),
  }
}

export function register(Alpine) {
  Alpine.data('agentsPage', () => ({
    agents: [],
    channels: [],
    cachedModels: [],
    isAdmin: false,
    currentUserId: 0,
    allUsers: [],

    showForm: false,
    editingId: null,
    activeTab: 'config',
    showTemplateModal: false,

    form: {
      name: '', model: '', model_strong: '', model_fast: '',
      system_prompt: '', soul: '', scope: 'system', enabled: true,
      enabled_builtin_skills: [], template_id: '',
      sandbox: { network: { mode: 'disabled', allowlist: [] } },
    },

    selectedSoulID: '',
    builtinTemplates: [],
    builtinSouls: [],
    builtinSkills: [],

    // User assignment (Users tab)
    assignedUsers: [],
    addUserId: '',

    // Channel binding
    selectedChannelIDs: [],

    confirmMsg: '',
    confirmAction: () => {},

    // Inline agent skills
    agentSkills: [],
    agentSkillsLoading: false,
    skillInstallModalOpen: false,
    skillInstallStage: 'search',
    skillSearchQuery: '',
    skillSearchResults: [],
    skillSearching: false,
    skillInstallSource: '',
    skillInstalling: false,

    // Personalisation panel (per currently-edited agent, per current user)
    personalisation: { soul: '', soulDraft: '', profile: '', profileDraft: '', loaded: false },

    async init() {
      await Promise.all([
        this.loadAgents(),
        this.loadCachedModels(),
        this.loadCurrentUser(),
        this.loadBuiltinCatalog(),
      ])
      if (this.isAdmin) await this.loadChannels()
      this.focusAgentFromURL()
    },

    async loadBuiltinCatalog() {
      const fetchKind = async (kind) => {
        try { return (await api('GET', '/api/builtin/' + kind)) || [] } catch { return [] }
      }
      const [templates, souls, skills] = await Promise.all([
        fetchKind('template'), fetchKind('soul'), fetchKind('skill'),
      ])
      this.builtinTemplates = templates
      this.builtinSouls = souls
      this.builtinSkills = skills
    },

    // --- Create / template flow ---

    startCreate() {
      this.resetForm()
      if (this.builtinTemplates.length > 0) {
        this.showTemplateModal = true
      } else {
        this.showForm = true
      }
    },

    cancelForm() { this.resetForm() },

    pickBlank() {
      this.showTemplateModal = false
      this.showForm = true
      this.activeTab = 'config'
    },

    async pickTemplate(tmpl) {
      try {
        const full = await api('GET', '/api/builtin/template/' + tmpl.id)
        const meta = full.metadata || {}
        let soulContent = ''
        if (meta.soul_id) {
          try {
            const soul = await api('GET', '/api/builtin/soul/' + meta.soul_id)
            soulContent = soul.content || ''
          } catch (_) {}
        }
        this.form = {
          ...this.form,
          name: this.form.name || tmpl.name || '',
          model: meta.model || this.form.model || '',
          system_prompt: full.content || '',
          soul: soulContent,
          enabled_builtin_skills: Array.isArray(meta.skills) ? meta.skills : [],
          template_id: tmpl.id,
        }
        this.showTemplateModal = false
        this.showForm = true
        this.activeTab = 'config'
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      }
    },

    // --- Soul presets ---

    applySoul(soulID) {
      if (!soulID) return
      api('GET', '/api/builtin/soul/' + soulID)
        .then(full => { this.form.soul = full.content || '' })
        .catch(e => { this.selectedSoulID = ''; this.$store.toast.show(e.message, 'error') })
    },

    // --- Builtin skill toggles ---

    toggleBuiltinSkill(name) {
      const list = this.form.enabled_builtin_skills || []
      const idx = list.indexOf(name)
      this.form.enabled_builtin_skills = idx >= 0
        ? list.filter((_, i) => i !== idx)
        : [...list, name]
    },

    isBuiltinSkillEnabled(name) {
      return (this.form.enabled_builtin_skills || []).includes(name)
    },

    // --- Auth / users ---

    async loadCurrentUser() {
      try {
        const me = await api('GET', '/api/auth/me')
        this.isAdmin = me.is_admin || false
        this.currentUserId = me.user_id || 0
        if (this.isAdmin) await this.loadAllUsers()
      } catch { this.isAdmin = false }
    },

    async loadAllUsers() {
      try { this.allUsers = (await api('GET', '/api/auth/users')) || [] } catch {}
    },

    canEditAgent(a) {
      return this.isAdmin || (a.creator_id !== 0 && a.creator_id === this.currentUserId)
    },

    // --- Channels ---

    async loadChannels() {
      try {
        this.channels = ((await api('GET', '/api/channels')) || []).map(normalizeChannel)
      } catch { this.channels = [] }
    },

    dedicatedChannelsForAgent(agentId) {
      return this.channels.filter(ch => ch.id !== ch.type && ch.agent_id === agentId)
    },

    availableDedicatedChannels(agentId) {
      return this.channels.filter(ch => ch.id !== ch.type && ch.enabled && (!ch.agent_id || ch.agent_id === agentId))
    },

    // --- Models autocomplete ---

    async loadCachedModels() {
      try {
        this.cachedModels = ((await api('GET', '/api/models')) || []).map(m => m.provider + '/' + m.model)
      } catch {}
    },

    filteredModels(search) {
      if (!search) return this.cachedModels
      const q = search.toLowerCase()
      return this.cachedModels.filter(m => m.toLowerCase().includes(q))
    },

    // --- Agent list ---

    async loadAgents() {
      try {
        const agents = (await api('GET', '/api/agents')) || []
        const requestedAgent = this.requestedAgentID()
        this.agents = agents.map(a => ({
          ...a,
          sandbox: this.normalizeSandbox(a.sandbox),
          _highlight: a.id === requestedAgent,
        }))
      } catch (e) { console.error(e) }
    },

    requestedAgentID() {
      return new URLSearchParams(window.location.search).get('agent') || ''
    },

    focusAgentFromURL() {
      const id = this.requestedAgentID()
      if (!id) return
      const agent = this.agents.find(a => a.id === id)
      if (agent) this.editAgent(agent)
    },

    resetForm() {
      this.form = {
        name: '', model: '', model_strong: '', model_fast: '',
        system_prompt: '', soul: '', scope: 'system', enabled: true,
        enabled_builtin_skills: [], template_id: '',
        sandbox: { network: { mode: 'disabled', allowlist: [] } },
      }
      this.selectedSoulID = ''
      this.editingId = null
      this.showForm = false
      this.activeTab = 'config'
      this.agentSkills = []
      this.assignedUsers = []
      this.addUserId = ''
      this.selectedChannelIDs = []
      this.personalisation = { soul: '', soulDraft: '', profile: '', profileDraft: '', loaded: false }
      this.skillInstallModalOpen = false
      this.skillInstallStage = 'search'
      this.skillSearchQuery = ''
      this.skillSearchResults = []
      this.skillInstallSource = ''
    },

    // --- Agent CRUD ---

    async editAgent(a) {
      this.form = {
        ...a,
        scope: a.scope || 'system',
        enabled_builtin_skills: Array.isArray(a.enabled_builtin_skills) ? a.enabled_builtin_skills : [],
        template_id: '',
        sandbox: this.normalizeSandbox(a.sandbox),
      }
      this.selectedSoulID = ''
      this.editingId = a.id
      this.activeTab = 'config'
      this.personalisation = { soul: '', soulDraft: '', profile: '', profileDraft: '', loaded: false }
      this.agentSkills = []
      this.assignedUsers = []
      await Promise.all([
        this.loadChannels(),
        this.loadAgentSkills(a.id),
        this.loadPersonalisation(a.id),
      ])
      this.selectedChannelIDs = this.dedicatedChannelsForAgent(a.id).map(c => c.id)
      this.showForm = true
    },

    async saveAgent() {
      try {
        const payload = {
          ...this.form,
          sandbox: this.normalizeSandbox(this.form.sandbox),
        }
        if (payload.sandbox.network.mode !== 'whitelist') {
          payload.sandbox.network.allowlist = []
        }
        if (this.editingId) {
          await api('PUT', '/api/agents/' + this.editingId, payload)
          await this._saveChannelBindings(this.editingId)
        } else {
          const created = await api('POST', '/api/agents', payload)
          this.editingId = created.id
          await Promise.all([
            this._saveChannelBindings(created.id),
            this.loadAgentSkills(created.id),
            this.loadPersonalisation(created.id),
          ])
          this.selectedChannelIDs = this.dedicatedChannelsForAgent(created.id).map(c => c.id)
        }
        await this.loadAgents()
        this.$store.toast.show('Saved')
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      }
    },

    async _saveChannelBindings(agentID) {
      if (!this.isAdmin) return
      const selected = new Set(this.selectedChannelIDs)
      for (const ch of this.availableDedicatedChannels(agentID)) {
        const wantsAgent = selected.has(ch.id)
        const nextAgentID = wantsAgent ? agentID : ''
        if ((ch.agent_id || '') === nextAgentID) continue
        await api('PUT', '/api/channels/' + encodeURIComponent(ch.id), {
          type: ch.type,
          agent_id: nextAgentID,
          config: JSON.stringify(ch._config || {}),
        })
      }
      await this.loadChannels()
    },

    normalizeSandbox(sandbox) {
      const mode = sandbox?.network?.mode || 'disabled'
      const allowlist = Array.isArray(sandbox?.network?.allowlist)
        ? sandbox.network.allowlist
        : typeof sandbox?.network?.allowlist === 'string'
          ? sandbox.network.allowlist.split(/\r?\n|,/).map(v => v.trim()).filter(Boolean)
          : []
      return { network: { mode, allowlist } }
    },

    sandboxAllowlistText(sandbox = this.form.sandbox) {
      return (sandbox?.network?.allowlist || []).join('\n')
    },

    updateSandboxAllowlist(value) {
      this.form.sandbox = this.normalizeSandbox({
        network: {
          mode: this.form.sandbox?.network?.mode || 'disabled',
          allowlist: value.split(/\r?\n|,/).map(v => v.trim()).filter(Boolean),
        },
      })
    },

    async doDeleteAgent(id) {
      try {
        await api('DELETE', '/api/agents/' + id)
        if (this.editingId === id) this.resetForm()
        await this.loadAgents()
        this.$store.toast.show('Deleted')
      } catch (e) { this.$store.toast.show(e.message, 'error') }
    },

    confirmDelete(msg, action) {
      this.confirmMsg = msg
      this.confirmAction = action
    },

    // --- User assignment (Users tab) ---

    get availableUsers() {
      const ids = new Set(this.assignedUsers.map(u => u.id))
      return this.allUsers.filter(u => !ids.has(u.id))
    },

    async loadAssignedUsers(agentId) {
      try {
        this.assignedUsers = (await api('GET', '/api/agents/' + agentId + '/users')) || []
      } catch { this.assignedUsers = [] }
    },

    async addUser() {
      if (!this.addUserId) return
      try {
        await api('POST', '/api/agents/' + this.editingId + '/users', { user_id: Number(this.addUserId) })
        this.addUserId = ''
        await this.loadAssignedUsers(this.editingId)
        this.$store.toast.show('User assigned')
      } catch (e) { this.$store.toast.show(e.message, 'error') }
    },

    async removeUser(userId) {
      try {
        await api('DELETE', '/api/agents/' + this.editingId + '/users/' + userId)
        await this.loadAssignedUsers(this.editingId)
        this.$store.toast.show('User removed')
      } catch (e) { this.$store.toast.show(e.message, 'error') }
    },

    // --- Custom skills ---

    async loadAgentSkills(agentId) {
      if (!agentId) return
      this.agentSkillsLoading = true
      try {
        this.agentSkills = (await api('GET', '/api/agents/' + agentId + '/skills')) || []
      } catch { this.agentSkills = [] }
      finally { this.agentSkillsLoading = false }
    },

    async deleteAgentSkill(skillId) {
      if (!this.editingId) return
      try {
        await api('DELETE', '/api/agents/' + this.editingId + '/skills/' + skillId)
        await this.loadAgentSkills(this.editingId)
        this.$store.toast.show('Skill removed')
      } catch (e) { this.$store.toast.show(e.message, 'error') }
    },

    openSkillInstallModal() {
      this.skillInstallModalOpen = true
      this.skillInstallStage = 'search'
      this.skillSearchQuery = ''
      this.skillSearchResults = []
      this.skillSearching = false
      this.skillInstallSource = ''
      this.skillInstalling = false
    },

    async doSkillSearch() {
      const q = this.skillSearchQuery.trim()
      if (!q) { this.skillSearchResults = []; return }
      this.skillSearching = true
      try {
        this.skillSearchResults = (await api('GET', '/api/skills/search?q=' + encodeURIComponent(q) + '&limit=20')) || []
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
        this.skillSearchResults = []
      } finally { this.skillSearching = false }
    },

    pickSkillResult(s) {
      this.skillInstallSource = s.source + '@' + s.skillId
      this.skillInstallStage = 'config'
    },

    async doSkillInstall() {
      if (this.skillInstalling || !this.editingId) return
      this.skillInstalling = true
      try {
        const res = await api('POST', '/api/agents/' + this.editingId + '/skills/install', { source: this.skillInstallSource })
        this.$store.toast.show('Installed: ' + (res?.name || 'skill'))
        this.skillInstallModalOpen = false
        await this.loadAgentSkills(this.editingId)
      } catch (e) { this.$store.toast.show(e.message, 'error') }
      finally { this.skillInstalling = false }
    },

    // --- Personalisation ---

    async loadPersonalisation(agentId) {
      if (!agentId) return
      this.personalisation = { soul: '', soulDraft: '', profile: '', profileDraft: '', loaded: false }
      try {
        const mems = (await api('GET', '/api/auth/profile/memories')) || []
        const mem = mems.find(m => m.agent_id === agentId)
        const soul = mem?.soul || ''
        const profile = mem?.content || ''
        this.personalisation = { soul, soulDraft: soul, profile, profileDraft: profile, loaded: true }
      } catch {
        this.personalisation = { soul: '', soulDraft: '', profile: '', profileDraft: '', loaded: true }
      }
    },

    async savePersonalisationSoul() {
      try {
        await api('PUT', '/api/auth/profile/soul/' + this.editingId, { soul: this.personalisation.soulDraft })
        this.personalisation.soul = this.personalisation.soulDraft
        this.$store.toast.show('Soul saved')
      } catch (e) { this.$store.toast.show(e.message, 'error') }
    },

    async savePersonalisationProfile() {
      try {
        await api('PUT', '/api/auth/profile/memories/' + this.editingId, { content: this.personalisation.profileDraft })
        this.personalisation.profile = this.personalisation.profileDraft
        this.$store.toast.show('Profile saved')
      } catch (e) { this.$store.toast.show(e.message, 'error') }
    },
  }))
}
