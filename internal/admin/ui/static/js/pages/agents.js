import { api } from '/static/js/api.js'
import { skillsDrawerMixin } from '/static/js/components/skills_drawer.js'

/**
 * Registers the agentsPage Alpine.data component.
 *
 * @param {import('alpinejs').Alpine} Alpine
 */
function parseChannelConfig(raw) {
  try {
    return JSON.parse(raw || '{}')
  } catch {
    return {}
  }
}

function normalizeChannel(channel) {
  return {
    ...channel,
    type: channel.type || channel.id,
    agent_id: channel.agent_id || '',
    config: channel.config || '{}',
    _config: parseChannelConfig(channel.config),
  }
}

export function register(Alpine) {
  Alpine.data('agentsPage', () => ({
    ...skillsDrawerMixin(),
    agents: [],
    channels: [],
    cachedModels: [],
    isAdmin: false,
    currentUserId: 0,
    allUsers: [],

    showForm: false,
    editingId: null,
    // formStep is 'pick-template' for a fresh create flow (shows builtin
    // templates + a Blank option) or 'editing' for the normal form.
    formStep: 'editing',
    form: {
      name: '', model: '', model_strong: '', model_fast: '',
      system_prompt: '', soul: '', scope: 'system', enabled: true,
      enabled_builtin_skills: [],
      template_id: '',
      sandbox: { network: { mode: 'disabled', allowlist: [] } },
    },

    selectedSoulID: '',

    // Builtin catalog — fetched once on init.
    builtinTemplates: [],
    builtinSouls: [],
    builtinSkills: [],

    // User assignment modal state.
    showUserModal: false,
    userModalAgent: '',
    assignedUsers: [],
    addUserId: '',

    // Dedicated channel binding modal state.
    showChannelModal: false,
    channelModalAgent: '',
    selectedChannelIDs: [],
    savingChannels: false,

    confirmMsg: '',
    confirmAction: () => {},

    async init() {
      await Promise.all([
        this.loadAgents(),
        this.loadCachedModels(),
        this.loadCurrentUser(),
        this.loadBuiltinCatalog(),
      ])
      if (this.isAdmin) {
        await this.loadChannels()
      }
      this.focusAgentFromURL()
    },

    async loadBuiltinCatalog() {
      const fetchKind = async (kind) => {
        try {
          return (await api('GET', '/api/builtin/' + kind)) || []
        } catch (_) {
          return []
        }
      }
      const [templates, souls, skills] = await Promise.all([
        fetchKind('template'),
        fetchKind('soul'),
        fetchKind('skill'),
      ])
      this.builtinTemplates = templates
      this.builtinSouls = souls
      this.builtinSkills = skills
    },

    // startCreate opens the form in template-picker mode. If no templates are
    // available the picker is skipped and we go straight to the blank form.
    startCreate() {
      this.resetForm()
      this.showForm = true
      this.formStep = this.builtinTemplates.length > 0 ? 'pick-template' : 'editing'
    },

    // cancelForm closes the form and resets state.
    cancelForm() {
      this.resetForm()
    },

    // pickBlank proceeds to the editing form without loading a template.
    pickBlank() {
      this.formStep = 'editing'
    },

    // pickTemplate pre-fills the form from a builtin template.
    async pickTemplate(tmpl) {
      try {
        const full = await api('GET', '/api/builtin/template/' + tmpl.id)
        const meta = full.metadata || {}
        const soulID = meta.soul_id || ''
        let soulContent = ''
        if (soulID) {
          try {
            const soul = await api('GET', '/api/builtin/soul/' + soulID)
            soulContent = soul.content || ''
          } catch (_) {}
        }
        const tmplSkills = Array.isArray(meta.skills) ? meta.skills : []
        this.form = {
          ...this.form,
          name: this.form.name || tmpl.name || '',
          model: meta.model || this.form.model || '',
          system_prompt: full.content || '',
          soul: soulContent,
          enabled_builtin_skills: tmplSkills,
          template_id: tmpl.id,
        }
        this.formStep = 'editing'
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      }
    },

    applySoul(soulID) {
      if (!soulID) return
      api('GET', '/api/builtin/soul/' + soulID)
        .then(full => { this.form.soul = full.content || '' })
        .catch(e => { this.selectedSoulID = ''; this.$store.toast.show(e.message, 'error') })
    },

    toggleBuiltinSkill(name) {
      const list = this.form.enabled_builtin_skills || []
      const idx = list.indexOf(name)
      if (idx >= 0) {
        this.form.enabled_builtin_skills = list.filter((_, i) => i !== idx)
      } else {
        this.form.enabled_builtin_skills = [...list, name]
      }
    },

    isBuiltinSkillEnabled(name) {
      return (this.form.enabled_builtin_skills || []).includes(name)
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

    async loadChannels() {
      try {
        const channels = await api('GET', '/api/channels') || []
        this.channels = channels.map(normalizeChannel)
      } catch (_) {
        this.channels = []
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
          sandbox: this.normalizeSandbox(a.sandbox),
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
        system_prompt: '', soul: '', scope: 'system', enabled: true,
        enabled_builtin_skills: [],
        template_id: '',
        sandbox: { network: { mode: 'disabled', allowlist: [] } },
      }
      this.selectedSoulID = ''
      this.editingId = null
      this.showForm = false
      this.formStep = 'editing'
    },

    editAgent(a) {
      this.form = {
        ...a,
        scope: a.scope || 'system',
        enabled_builtin_skills: Array.isArray(a.enabled_builtin_skills) ? a.enabled_builtin_skills : [],
        template_id: '',
        sandbox: this.normalizeSandbox(a.sandbox),
      }
      this.selectedSoulID = ''
      this.editingId = a.id
      this.showForm = true
      this.formStep = 'editing'
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
        } else {
          await api('POST', '/api/agents', payload)
        }
        this.resetForm()
        await this.loadAgents()
        this.$store.toast.show('Saved')
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      }
    },

    normalizeSandbox(sandbox) {
      const mode = sandbox?.network?.mode || 'disabled'
      const allowlist = Array.isArray(sandbox?.network?.allowlist)
        ? sandbox.network.allowlist
        : typeof sandbox?.network?.allowlist === 'string'
          ? sandbox.network.allowlist
              .split(/\r?\n|,/)
              .map(v => v.trim())
              .filter(Boolean)
          : []
      return {
        network: { mode, allowlist },
      }
    },

    sandboxAllowlistText(sandbox = this.form.sandbox) {
      return (sandbox?.network?.allowlist || []).join('\n')
    },

    updateSandboxAllowlist(value) {
      this.form.sandbox = this.normalizeSandbox({
        network: {
          mode: this.form.sandbox?.network?.mode || 'disabled',
          allowlist: value
            .split(/\r?\n|,/)
            .map(v => v.trim())
            .filter(Boolean),
        },
      })
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

    dedicatedChannelsForAgent(agentId) {
      return this.channels.filter(channel => channel.id !== channel.type && channel.agent_id === agentId)
    },

    availableDedicatedChannels(agentId) {
      return this.channels.filter(channel => channel.id !== channel.type && channel.enabled && (!channel.agent_id || channel.agent_id === agentId))
    },

    async manageChannels(agent) {
      this.channelModalAgent = agent.id
      await this.loadChannels()
      this.selectedChannelIDs = this.dedicatedChannelsForAgent(agent.id).map(channel => channel.id)
      this.showChannelModal = true
    },

    async manageAgentSkills(agent) {
      await this.openSkillsDrawer({
        title: 'Skills · ' + agent.name,
        subtitle: 'Agent scope · ' + agent.id,
        scope: 'agent',
        agentID: agent.id,
        canEdit: this.canEditAgent(agent),
      })
    },

    async saveChannelBindings() {
      this.savingChannels = true
      try {
        const selected = new Set(this.selectedChannelIDs)
        const current = this.availableDedicatedChannels(this.channelModalAgent)
        for (const channel of current) {
          const wantsAgent = selected.has(channel.id)
          const nextAgentID = wantsAgent ? this.channelModalAgent : ''
          if ((channel.agent_id || '') === nextAgentID) {
            continue
          }
          await api('PUT', '/api/channels/' + encodeURIComponent(channel.id), {
            type: channel.type,
            agent_id: nextAgentID,
            config: JSON.stringify(channel._config || {}),
          })
        }
        await this.loadChannels()
        await this.loadAgents()
        this.showChannelModal = false
        this.$store.toast.show('Dedicated channels updated')
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      } finally {
        this.savingChannels = false
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
