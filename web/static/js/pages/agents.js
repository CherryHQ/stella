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
      template_id: '',
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

    // Skills tab
    agentSkills: [],
    agentSkillsLoading: false,
    userSkills: [],
    skillViewFilter: 'enabled',
    skillScopeFilter: 'all',
    skillListQuery: '',
    selectedSkillKey: '',
    selectedSkill: null,
    selectedSkillLoading: false,
    selectedSkillSaving: false,
    selectedSkillDirty: false,
    selectedSkillEditMode: false,
    selectedSkillShowAdvanced: false,
    selectedSkillActiveFile: 'SKILL.md',
    selectedSkillFileContent: '',
    selectedSkillFileLoading: false,
    selectedSkillFileCache: {},
    selectedSkillAddingFile: false,
    selectedSkillNewFileName: '',
    skillInstallModalOpen: false,
    skillInstallScope: 'user',
    skillSearchQuery: '',
    skillSearchResults: [],
    skillSearching: false,
    skillInstallSource: '',
    skillInstalling: false,
    skillUploadFile: null,
    skillUploading: false,

    // Personalisation panel (per currently-edited agent, per current user)
    personalisation: { soul: '', soulDraft: '', profile: '', profileDraft: '', loaded: false },

    async init() {
      await Promise.all([
        this.loadAgents(),
        this.loadCachedModels(),
        this.loadCurrentUser(),
        this.loadBuiltinCatalog(),
        this.loadUserSkills(),
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
      this.syncSkillSelection()
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
        template_id: '',
        sandbox: { network: { mode: 'disabled', allowlist: [] } },
      }
      this.selectedSoulID = ''
      this.editingId = null
      this.showForm = false
      this.activeTab = 'config'
      this.agentSkills = []
      this.skillViewFilter = 'enabled'
      this.skillScopeFilter = 'all'
      this.skillListQuery = ''
      this.selectedSkillKey = ''
      this.selectedSkill = null
      this.selectedSkillDirty = false
      this.selectedSkillEditMode = false
      this.selectedSkillShowAdvanced = false
      this.selectedSkillActiveFile = 'SKILL.md'
      this.selectedSkillFileContent = ''
      this.selectedSkillFileCache = {}
      this.selectedSkillAddingFile = false
      this.selectedSkillNewFileName = ''
      this.assignedUsers = []
      this.addUserId = ''
      this.selectedChannelIDs = []
      this.personalisation = { soul: '', soulDraft: '', profile: '', profileDraft: '', loaded: false }
      this.skillInstallModalOpen = false
      this.skillSearchQuery = ''
      this.skillSearchResults = []
      this.skillInstallSource = ''
    },

    // --- Agent CRUD ---

    async editAgent(a) {
      this.form = {
        ...a,
        scope: a.scope || 'system',
        template_id: '',
        sandbox: this.normalizeSandbox(a.sandbox),
      }
      this.selectedSoulID = ''
      this.editingId = a.id
      this.activeTab = 'config'
      this.personalisation = { soul: '', soulDraft: '', profile: '', profileDraft: '', loaded: false }
      this.agentSkills = []
      this.selectedSkillKey = ''
      this.selectedSkill = null
      this.selectedSkillDirty = false
      this.selectedSkillEditMode = false
      this.selectedSkillShowAdvanced = false
      this.selectedSkillFileCache = {}
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

    // --- Skills ---

    skillKey(sk) {
      return sk ? sk.scope + ':' + sk.id : ''
    },

    skillScopeLabel(scope) {
      return {
        system: 'Built-in',
        user: 'User',
        agent: 'This agent',
      }[scope] || scope
    },

    skillScopeClass(scope) {
      return {
        system: 'badge-ghost',
        user: 'badge-success badge-soft',
        agent: 'badge-primary badge-soft',
      }[scope] || 'badge-ghost'
    },

    skillStatusClass(status) {
      return {
        active: 'badge-success badge-soft',
        draft: 'badge-warning badge-soft',
        deprecated: 'badge-error badge-soft',
      }[status] || 'badge-ghost'
    },

    skillCanEdit(sk = this.selectedSkill) {
      return !!sk && sk.scope !== 'system'
    },

    skillCanDelete(sk = this.selectedSkill) {
      return !!sk && sk.scope !== 'system'
    },

    skillCanToggle(sk) {
      return !!sk && sk.scope !== 'system'
    },

    canInstallAgentSkills() {
      return this.isAdmin && !!this.editingId
    },

    startEditingSelectedSkill() {
      if (!this.skillCanEdit()) return
      this.selectedSkillEditMode = true
    },

    stopEditingSelectedSkill() {
      if (this.selectedSkillDirty && !confirm('Discard unsaved changes?')) return
      this.selectedSkillEditMode = false
      this.selectedSkillDirty = false
      if (this.selectedSkill) this.selectSkill(this.selectedSkill)
    },

    allSkills() {
      const system = this.builtinSkills.map(sk => ({
        id: sk.id,
        scope: 'system',
        name: sk.name,
        description: sk.description || '',
        status: 'active',
        disable_model_invocation: false,
      }))
      const user = this.userSkills.map(sk => ({ ...sk, scope: 'user' }))
      const agent = this.agentSkills.map(sk => ({ ...sk, scope: 'agent' }))
      const ordered = { system: 0, user: 1, agent: 2 }
      return [...system, ...user, ...agent].sort((a, b) => {
        const scopeDiff = (ordered[a.scope] ?? 99) - (ordered[b.scope] ?? 99)
        if (scopeDiff !== 0) return scopeDiff
        return (a.name || '').localeCompare(b.name || '')
      })
    },

    filteredSkills() {
      const q = this.skillListQuery.trim().toLowerCase()
      return this.allSkills().filter(sk => {
        if (this.skillViewFilter === 'enabled' && sk.status !== 'active') return false
        if (this.skillViewFilter === 'modified' && sk.scope === 'system') return false
        if (this.skillScopeFilter !== 'all' && sk.scope !== this.skillScopeFilter) return false
        if (!q) return true
        return [sk.name, sk.description, sk.scope, sk.status].some(v => (v || '').toLowerCase().includes(q))
      })
    },

    syncSkillSelection() {
      const rows = this.allSkills()
      if (rows.length === 0) {
        this.selectedSkillKey = ''
        this.selectedSkill = null
        return
      }
      if (!this.selectedSkillKey) return
      const exists = rows.some(sk => this.skillKey(sk) === this.selectedSkillKey)
      if (!exists) {
        this.selectedSkillKey = ''
        this.selectedSkill = null
      }
    },

    async ensureSelectedSkill() {
      this.syncSkillSelection()
      if (this.selectedSkillKey || this.allSkills().length === 0) return
      await this.selectSkill(this.allSkills()[0])
    },

    skillItemURL(scope, id) {
      if (scope === 'user') return '/api/auth/profile/skills/' + id
      if (scope === 'agent') return '/api/agents/' + encodeURIComponent(this.editingId) + '/skills/' + id
      return '/api/builtin/skill/' + id
    },

    skillFileURL(scope, id, path) {
      if (scope === 'user') return '/api/auth/profile/skills/' + id + '/file?path=' + encodeURIComponent(path)
      if (scope === 'agent') return '/api/agents/' + encodeURIComponent(this.editingId) + '/skills/' + id + '/file?path=' + encodeURIComponent(path)
      return ''
    },

    async selectSkill(sk) {
      if (!sk) return
      if (this.selectedSkillDirty && !confirm('Discard unsaved changes?')) return
      this.selectedSkillKey = this.skillKey(sk)
      this.selectedSkillLoading = true
      this.selectedSkill = null
      this.selectedSkillDirty = false
      this.selectedSkillEditMode = false
      this.selectedSkillShowAdvanced = false
      this.selectedSkillFileCache = {}
      this.selectedSkillAddingFile = false
      this.selectedSkillNewFileName = ''
      try {
        if (sk.scope === 'system') {
          const full = await api('GET', this.skillItemURL(sk.scope, sk.id))
          this.selectedSkill = {
            ...sk,
            name: full.name || sk.name,
            description: full.description || sk.description || '',
            files: ['SKILL.md'],
            disable_model_invocation: false,
          }
          this.selectedSkillActiveFile = 'SKILL.md'
          this.selectedSkillFileContent = full.content || ''
          this.selectedSkillFileCache = { 'SKILL.md': full.content || '' }
        } else {
          const full = await api('GET', this.skillItemURL(sk.scope, sk.id))
          this.selectedSkill = { ...full, scope: sk.scope }
          const files = full.files || ['SKILL.md']
          const initialFile = files.includes('SKILL.md') ? 'SKILL.md' : files[0]
          await this.selectSkillFile(initialFile, true)
        }
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      } finally {
        this.selectedSkillLoading = false
      }
    },

    async selectSkillFile(path, skipDirtyCheck = false) {
      if (!this.selectedSkill || !path) return
      if (!skipDirtyCheck && this.selectedSkillDirty && !confirm('Discard unsaved changes?')) return
      this.selectedSkillActiveFile = path
      if (this.selectedSkill.scope === 'system') {
        this.selectedSkillFileContent = this.selectedSkillFileCache[path] || ''
        this.selectedSkillDirty = false
        return
      }
      if (Object.hasOwn(this.selectedSkillFileCache, path)) {
        this.selectedSkillFileContent = this.selectedSkillFileCache[path]
        this.selectedSkillDirty = false
        return
      }
      this.selectedSkillFileLoading = true
      try {
        const res = await api('GET', this.skillFileURL(this.selectedSkill.scope, this.selectedSkill.id, path))
        this.selectedSkillFileContent = res?.content || ''
        this.selectedSkillFileCache[path] = this.selectedSkillFileContent
        this.selectedSkillDirty = false
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      } finally {
        this.selectedSkillFileLoading = false
      }
    },

    markSelectedSkillDirty() {
      this.selectedSkillDirty = true
    },

    selectedSkillFiles() {
      return this.selectedSkill?.files || ['SKILL.md']
    },

    confirmAddSkillFile() {
      this.selectedSkillAddingFile = true
      this.selectedSkillNewFileName = ''
    },

    commitAddSkillFile() {
      const skill = this.selectedSkill
      const name = (this.selectedSkillNewFileName || '').trim()
      if (!skill || !name) return
      if (name === 'SKILL.md' || this.selectedSkillFiles().includes(name)) {
        this.$store.toast.show('File already exists', 'error')
        return
      }
      skill.files = [...this.selectedSkillFiles(), name]
      this.selectedSkillFileCache[name] = ''
      this.selectedSkillAddingFile = false
      this.selectedSkillNewFileName = ''
      this.selectedSkillActiveFile = name
      this.selectedSkillFileContent = ''
      this.selectedSkillDirty = true
    },

    async deleteSelectedSkillFile(path) {
      if (!this.selectedSkill || this.selectedSkill.scope === 'system' || path === 'SKILL.md') return
      if (!confirm('Delete file "' + path + '"?')) return
      try {
        await api('DELETE', this.skillFileURL(this.selectedSkill.scope, this.selectedSkill.id, path))
        this.selectedSkill.files = this.selectedSkillFiles().filter(f => f !== path)
        delete this.selectedSkillFileCache[path]
        if (this.selectedSkillActiveFile === path) {
          await this.selectSkillFile('SKILL.md', true)
        }
        this.$store.toast.show('File removed')
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      }
    },

    async loadUserSkills() {
      try {
        this.userSkills = (await api('GET', '/api/auth/profile/skills')) || []
      } catch { this.userSkills = [] }
      this.syncSkillSelection()
    },

    async loadAgentSkills(agentId) {
      if (!agentId) {
        this.agentSkills = []
        this.syncSkillSelection()
        return
      }
      this.agentSkillsLoading = true
      try {
        this.agentSkills = (await api('GET', '/api/agents/' + agentId + '/skills')) || []
      } catch { this.agentSkills = [] }
      finally {
        this.agentSkillsLoading = false
        this.syncSkillSelection()
      }
    },

    async toggleSkillStatus(sk) {
      if (!this.skillCanToggle(sk)) return
      const next = sk.status === 'active' ? 'draft' : 'active'
      try {
        await api('PUT', this.skillItemURL(sk.scope, sk.id), { status: next })
        sk.status = next
        if (this.selectedSkillKey === this.skillKey(sk) && this.selectedSkill) this.selectedSkill.status = next
      } catch (e) { this.$store.toast.show(e.message, 'error') }
    },

    async saveSelectedSkill() {
      if (!this.selectedSkill || !this.skillCanEdit()) return
      this.selectedSkillSaving = true
      try {
        this.selectedSkillFileCache[this.selectedSkillActiveFile] = this.selectedSkillFileContent
        await api('PUT', this.skillItemURL(this.selectedSkill.scope, this.selectedSkill.id), {
          description: this.selectedSkill.description,
          status: this.selectedSkill.status,
          disable_model_invocation: !!this.selectedSkill.disable_model_invocation,
          files: { [this.selectedSkillActiveFile]: this.selectedSkillFileContent },
        })
        this.selectedSkillDirty = false
        const full = await api('GET', this.skillItemURL(this.selectedSkill.scope, this.selectedSkill.id))
        this.selectedSkill = { ...full, scope: this.selectedSkill.scope }
        if (this.selectedSkill.scope === 'user') await this.loadUserSkills()
        if (this.selectedSkill.scope === 'agent') await this.loadAgentSkills(this.editingId)
        this.$store.toast.show('Saved')
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      } finally {
        this.selectedSkillSaving = false
      }
    },

    async deleteSkill(sk = this.selectedSkill) {
      if (!this.skillCanDelete(sk)) return
      if (!confirm('Delete skill "' + sk.name + '"? This cannot be undone.')) return
      try {
        await api('DELETE', this.skillItemURL(sk.scope, sk.id))
        if (this.selectedSkillKey === this.skillKey(sk)) {
          this.selectedSkillKey = ''
          this.selectedSkill = null
          this.selectedSkillDirty = false
        }
        if (sk.scope === 'user') await this.loadUserSkills()
        if (sk.scope === 'agent') await this.loadAgentSkills(this.editingId)
        await this.ensureSelectedSkill()
        this.$store.toast.show('Skill removed')
      } catch (e) { this.$store.toast.show(e.message, 'error') }
    },

    async duplicateSelectedBuiltinSkillToAgent() {
      if (!this.selectedSkill || this.selectedSkill.scope !== 'system' || !this.canInstallAgentSkills()) return
      try {
        const res = await api('POST', '/api/agents/' + this.editingId + '/skills/from-builtin/' + this.selectedSkill.id)
        this.$store.toast.show('Installed: ' + (res?.name || this.selectedSkill.name))
        await this.loadAgentSkills(this.editingId)
        const created = this.agentSkills.find(sk => sk.name === (res?.name || this.selectedSkill.name))
        if (created) await this.selectSkill({ ...created, scope: 'agent' })
      } catch (e) { this.$store.toast.show(e.message, 'error') }
    },

    openSkillInstallModal(scope = null) {
      this.skillInstallModalOpen = true
      this.skillInstallScope = scope || (this.canInstallAgentSkills() ? 'agent' : 'user')
      this.skillSearchQuery = ''
      this.skillSearchResults = []
      this.skillSearching = false
      this.skillInstallSource = ''
      this.skillInstalling = false
      this.skillUploadFile = null
      this.skillUploading = false
    },

    setSkillInstallScope(scope) {
      if (scope === 'agent' && !this.canInstallAgentSkills()) return
      this.skillInstallScope = scope
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

    async installSkillResult(s) {
      if (!s) return
      this.skillInstallSource = s.source + '@' + s.skillId
      await this.doSkillInstall(this.skillInstallSource)
    },

    async doSkillInstall(source = null) {
      if (this.skillInstalling) return
      const scope = this.skillInstallScope || 'user'
      if (scope === 'agent' && !this.canInstallAgentSkills()) return
      const installSource = source || this.skillInstallSource
      if (!installSource) {
        this.$store.toast.show('Choose a skill first', 'error')
        return
      }
      this.skillInstalling = true
      try {
        const url = scope === 'agent'
          ? '/api/agents/' + this.editingId + '/skills/install'
          : '/api/auth/profile/skills/install'
        const res = await api('POST', url, { source: installSource })
        this.$store.toast.show('Installed: ' + (res?.name || 'skill'))
        this.skillInstallModalOpen = false
        if (scope === 'agent') {
          await this.loadAgentSkills(this.editingId)
          const created = this.agentSkills.find(sk => sk.name === (res?.name || ''))
          if (created) await this.selectSkill({ ...created, scope: 'agent' })
        } else {
          await this.loadUserSkills()
          const created = this.userSkills.find(sk => sk.name === (res?.name || ''))
          if (created) await this.selectSkill({ ...created, scope: 'user' })
        }
        await this.ensureSelectedSkill()
      } catch (e) { this.$store.toast.show(e.message, 'error') }
      finally { this.skillInstalling = false }
    },

    setSkillUploadFile(event) {
      this.skillUploadFile = event?.target?.files?.[0] || null
    },

    async doSkillUpload() {
      if (this.skillUploading) return
      const scope = this.skillInstallScope || 'user'
      if (scope === 'agent' && !this.canInstallAgentSkills()) return
      if (!this.skillUploadFile) {
        this.$store.toast.show('Choose a .zip file first', 'error')
        return
      }
      this.skillUploading = true
      try {
        const url = scope === 'agent'
          ? '/api/agents/' + this.editingId + '/skills/upload'
          : '/api/auth/profile/skills/upload'
        const form = new FormData()
        form.append('file', this.skillUploadFile)
        const res = await api('POST', url, form)
        this.$store.toast.show('Uploaded: ' + (res?.name || 'skill'))
        this.skillInstallModalOpen = false
        this.skillUploadFile = null
        if (scope === 'agent') {
          await this.loadAgentSkills(this.editingId)
          const created = this.agentSkills.find(sk => sk.id === res?.id)
          if (created) await this.selectSkill({ ...created, scope: 'agent' })
        } else {
          await this.loadUserSkills()
          const created = this.userSkills.find(sk => sk.id === res?.id)
          if (created) await this.selectSkill({ ...created, scope: 'user' })
        }
        await this.ensureSelectedSkill()
      } catch (e) { this.$store.toast.show(e.message, 'error') }
      finally { this.skillUploading = false }
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
