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
    mode: 'empty', // 'empty' | 'detail' | 'new'
    draft: {
      description: '',
      status: 'active',
      disable_model_invocation: false,
      skill_md: '',
    },
    form: {
      name: '',
      scope: 'system',
      user_id: 0,
      agent_id: '',
      description: '',
      status: 'active',
      disable_model_invocation: false,
      skill_md: '',
    },

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
      this.mode = 'detail'
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

    openNew() {
      this.mode = 'new'
      this.selected = null
      this.form = {
        name: '',
        scope: 'system',
        user_id: 0,
        agent_id: '',
        description: '',
        status: 'active',
        disable_model_invocation: false,
        skill_md: '',
      }
    },

    cancelNew() {
      this.mode = this.selected ? 'detail' : 'empty'
    },

    async createSkill() {
      if (!this.form.name.trim()) {
        this.$store.toast.show('Name is required', 'error')
        return
      }
      this.saving = true
      try {
        const body = {
          name: this.form.name.trim(),
          scope: this.form.scope,
          description: this.form.description,
          status: this.form.status,
          disable_model_invocation: this.form.disable_model_invocation,
          files: { 'SKILL.md': this.form.skill_md },
        }
        if (this.form.scope === 'agent') {
          body.agent_id = this.form.agent_id
        } else if (this.form.scope === 'user') {
          body.user_id = Number(this.form.user_id)
        }
        const res = await api('POST', '/api/skills', body)
        this.$store.toast.show('Skill created')
        await this.loadSkills()
        const created = this.skills.find(s => s.id === res.id)
        if (created) {
          await this.selectSkill(created)
        } else {
          this.mode = 'empty'
        }
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      } finally {
        this.saving = false
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
        this.mode = 'empty'
        await this.loadSkills()
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      }
    },
  }))
}
