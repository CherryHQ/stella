import { api } from '/static/js/api.js'
import { skillsDrawerMixin } from '/static/js/components/skills_drawer.js'

/**
 * Registers the profilePage Alpine.data component.
 *
 * @param {import('alpinejs').Alpine} Alpine
 */
export function register(Alpine) {
  Alpine.data('profilePage', () => ({
    ...skillsDrawerMixin(),
    // Password change
    currentPassword: '',
    newPassword: '',
    confirmPassword: '',
    changingPassword: false,

    // Skill count badge (loaded lazily).
    mySkillsCount: 0,

    async init() {
      await this.loadMySkillsCount()
    },

    async loadMySkillsCount() {
      try {
        const items = (await api('GET', '/api/auth/profile/skills')) || []
        this.mySkillsCount = items.length
      } catch (_) {
        this.mySkillsCount = 0
      }
    },

    async openMySkills() {
      await this.openSkillsDrawer({
        title: 'My skills',
        subtitle: 'User scope · yours only',
        scope: 'user',
        canEdit: true,
      })
    },

    // Refresh count when drawer closes (skills may have changed).
    closeSkillsDrawer() {
      const wasOpen = this.skillsDrawer.open
      if (this.skillsDrawer.dirty) {
        if (!confirm('Discard unsaved changes?')) return
      }
      this.skillsDrawer.open = false
      this.skillsDrawer.selected = null
      if (wasOpen) this.loadMySkillsCount()
    },

    async changePassword() {
      if (!this.currentPassword || !this.newPassword) {
        this.$store.toast.show('Please fill in all password fields', 'error')
        return
      }
      if (this.newPassword.length < 8) {
        this.$store.toast.show('New password must be at least 8 characters', 'error')
        return
      }
      if (this.newPassword !== this.confirmPassword) {
        this.$store.toast.show('New passwords do not match', 'error')
        return
      }

      this.changingPassword = true
      try {
        await api('PUT', '/api/auth/profile/password', {
          current_password: this.currentPassword,
          new_password: this.newPassword,
        })
        this.$store.toast.show('Password changed successfully')
        this.currentPassword = ''
        this.newPassword = ''
        this.confirmPassword = ''
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      } finally {
        this.changingPassword = false
      }
    },

  }))
}
