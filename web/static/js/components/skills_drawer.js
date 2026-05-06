import { api } from '/static/js/api.js'

/**
 * skillsDrawerMixin returns Alpine data fields and methods for a
 * reusable slide-over skills drawer. Merge into page Alpine.data():
 *
 *   Alpine.data('myPage', () => ({
 *     ...skillsDrawerMixin(),
 *     ...
 *   }))
 *
 * Open via `openSkillsDrawer(opts)` where opts = {
 *   title, subtitle,
 *   scope: 'agent' | 'user' | 'system',
 *   agentID, userID,       // one of the two depending on scope
 *   useAdminAPI: boolean,  // true when admin manages via /api/skills
 *   canEdit: boolean,      // false for read-only
 * }.
 *
 * For the admin-managing-user case set useAdminAPI: true and scope: 'user'
 * + userID so the list is filtered client-side.
 */
export function skillsDrawerMixin() {
  return {
    skillsDrawer: {
      open: false,
      title: '',
      subtitle: '',
      scope: 'agent',
      agentID: '',
      userID: 0,
      useAdminAPI: false,
      canEdit: true,

      loading: false,
      saving: false,
      skills: [],
      selected: null,

      activeFile: '',
      fileContent: '',
      fileLoading: false,
      dirty: false,

      addingFile: false,
      newFileName: '',

      // Install sub-modal.
      installModalOpen: false,
      installStage: 'search',
      searchQuery: '',
      searchResults: [],
      searching: false,
      installing: false,
      installSource: '',
    },

    skillsDrawerListURL() {
      const d = this.skillsDrawer
      if (d.useAdminAPI) return '/api/skills'
      if (d.scope === 'agent') return '/api/agents/' + encodeURIComponent(d.agentID) + '/skills'
      if (d.scope === 'user') return '/api/auth/profile/skills'
      return '/api/skills'
    },

    skillsDrawerItemURL(id) {
      const d = this.skillsDrawer
      if (d.useAdminAPI) return '/api/skills/' + id
      if (d.scope === 'agent') return '/api/agents/' + encodeURIComponent(d.agentID) + '/skills/' + id
      if (d.scope === 'user') return '/api/auth/profile/skills/' + id
      return '/api/skills/' + id
    },

    skillsDrawerInstallURL() {
      const d = this.skillsDrawer
      if (d.useAdminAPI) return '/api/skills/install'
      if (d.scope === 'agent') return '/api/agents/' + encodeURIComponent(d.agentID) + '/skills/install'
      if (d.scope === 'user') return '/api/auth/profile/skills/install'
      return '/api/skills/install'
    },

    async openSkillsDrawer(opts) {
      this.skillsDrawer = {
        ...this.skillsDrawer,
        open: true,
        title: opts.title || 'Skills',
        subtitle: opts.subtitle || '',
        scope: opts.scope || 'agent',
        agentID: opts.agentID || '',
        userID: opts.userID || 0,
        useAdminAPI: !!opts.useAdminAPI,
        canEdit: opts.canEdit !== false,
        skills: [],
        selected: null,
        activeFile: '',
        fileContent: '',
        dirty: false,
        addingFile: false,
        newFileName: '',
        installModalOpen: false,
        installStage: 'search',
        searchQuery: '',
        searchResults: [],
      }
      await this.reloadSkillsDrawer()
    },

    closeSkillsDrawer() {
      if (this.skillsDrawer.dirty) {
        if (!confirm('Discard unsaved changes?')) return
      }
      this.skillsDrawer.open = false
      this.skillsDrawer.selected = null
    },

    async reloadSkillsDrawer() {
      const d = this.skillsDrawer
      d.loading = true
      try {
        let items = (await api('GET', this.skillsDrawerListURL())) || []
        // For admin managing specific user: filter client-side.
        if (d.useAdminAPI && d.scope === 'user' && d.userID) {
          items = items.filter(s => s.scope === 'user' && s.user_id === d.userID)
        }
        if (d.useAdminAPI && d.scope === 'system') {
          items = items.filter(s => s.scope === 'system')
        }
        d.skills = items
        // Keep selection if still present.
        if (d.selected) {
          const match = d.skills.find(s => s.id === d.selected.id)
          d.selected = match || null
        }
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
        d.skills = []
      } finally {
        d.loading = false
      }
    },

    async selectDrawerSkill(sk) {
      if (this.skillsDrawer.dirty) {
        if (!confirm('Discard unsaved changes?')) return
      }
      const d = this.skillsDrawer
      // Fetch full detail (has files list).
      try {
        const full = await api('GET', this.skillsDrawerItemURL(sk.id))
        d.selected = full
        d.dirty = false
        d.addingFile = false
        d.newFileName = ''
        const files = full.files || ['SKILL.md']
        const initial = files.includes('SKILL.md') ? 'SKILL.md' : files[0]
        await this.selectDrawerFile(initial)
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      }
    },

    async selectDrawerFile(path) {
      if (!path) return
      if (this.skillsDrawer.dirty) {
        if (!confirm('Discard unsaved changes?')) return
      }
      const d = this.skillsDrawer
      d.activeFile = path
      d.fileLoading = true
      d.fileContent = ''
      try {
        const res = await api('GET', this.skillsDrawerItemURL(d.selected.id) + '/file?path=' + encodeURIComponent(path))
        d.fileContent = res?.content || ''
        d.dirty = false
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      } finally {
        d.fileLoading = false
      }
    },

    markDrawerDirty() {
      this.skillsDrawer.dirty = true
    },

    confirmAddDrawerFile() {
      const d = this.skillsDrawer
      d.addingFile = true
      d.newFileName = ''
    },

    async commitAddDrawerFile() {
      const d = this.skillsDrawer
      const name = (d.newFileName || '').trim()
      if (!name) return
      if (name === 'SKILL.md' || (d.selected.files || []).includes(name)) {
        this.$store.toast.show('File already exists', 'error')
        return
      }
      // Locally add; content saved via Save.
      d.selected.files = [...(d.selected.files || []), name]
      d.activeFile = name
      d.fileContent = ''
      d.addingFile = false
      d.newFileName = ''
      d.dirty = true
    },

    async deleteDrawerFile(path) {
      const d = this.skillsDrawer
      if (path === 'SKILL.md') {
        this.$store.toast.show('Cannot delete SKILL.md', 'error')
        return
      }
      if (!confirm('Delete file "' + path + '"?')) return
      try {
        await api('DELETE', this.skillsDrawerItemURL(d.selected.id) + '/file?path=' + encodeURIComponent(path))
        d.selected.files = (d.selected.files || []).filter(f => f !== path)
        if (d.activeFile === path) {
          await this.selectDrawerFile('SKILL.md')
        }
        this.$store.toast.show('File removed')
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      }
    },

    async saveDrawerSkill() {
      const d = this.skillsDrawer
      if (!d.selected) return
      d.saving = true
      try {
        const body = {
          description: d.selected.description,
          status: d.selected.status,
          disable_model_invocation: d.selected.disable_model_invocation,
          files: { [d.activeFile]: d.fileContent },
        }
        await api('PUT', this.skillsDrawerItemURL(d.selected.id), body)
        d.dirty = false
        this.$store.toast.show('Saved')
        // Refresh selected detail to sync files.
        const full = await api('GET', this.skillsDrawerItemURL(d.selected.id))
        d.selected = full
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      } finally {
        d.saving = false
      }
    },

    async deleteDrawerSkill() {
      const d = this.skillsDrawer
      if (!d.selected) return
      if (!confirm('Delete skill "' + d.selected.name + '"? This cannot be undone.')) return
      try {
        await api('DELETE', this.skillsDrawerItemURL(d.selected.id))
        this.$store.toast.show('Skill deleted')
        d.selected = null
        d.dirty = false
        await this.reloadSkillsDrawer()
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      }
    },

    // --- Install modal ---

    openDrawerInstallModal() {
      const d = this.skillsDrawer
      d.installModalOpen = true
      d.installStage = 'search'
      d.searchQuery = ''
      d.searchResults = []
      d.searching = false
      d.installSource = ''
      d.installing = false
    },

    async doDrawerSearch() {
      const d = this.skillsDrawer
      const q = (d.searchQuery || '').trim()
      if (!q) {
        d.searchResults = []
        return
      }
      d.searching = true
      try {
        const results = await api('GET', '/api/skills/search?q=' + encodeURIComponent(q) + '&limit=20')
        d.searchResults = results || []
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
        d.searchResults = []
      } finally {
        d.searching = false
      }
    },

    pickDrawerSearchResult(s) {
      const d = this.skillsDrawer
      d.installSource = s.source + '@' + s.skillId
      d.installStage = 'confirm'
    },

    async doDrawerInstall() {
      const d = this.skillsDrawer
      if (d.installing) return
      const body = { source: d.installSource }
      if (d.useAdminAPI) {
        body.scope = d.scope
        if (d.scope === 'user') body.user_id = Number(d.userID)
        if (d.scope === 'agent') body.agent_id = d.agentID
      }
      d.installing = true
      try {
        const res = await api('POST', this.skillsDrawerInstallURL(), body)
        this.$store.toast.show('Installed: ' + (res?.name || 'skill'))
        d.installModalOpen = false
        await this.reloadSkillsDrawer()
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      } finally {
        d.installing = false
      }
    },
  }
}
