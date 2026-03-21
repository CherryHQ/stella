import { api } from '/static/js/api.js'

/**
 * Registers the profilePage Alpine.data component.
 *
 * @param {import('alpinejs').Alpine} Alpine
 */
export function register(Alpine) {
  Alpine.data('profilePage', () => ({
    // Password change
    currentPassword: '',
    newPassword: '',
    confirmPassword: '',
    changingPassword: false,

    // Identities
    identities: [],
    loadingIdentities: false,

    // Link code
    linkCode: '',
    linkPlatform: '',
    generating: false,

    async init() {
      await this.loadIdentities()
    },

    async loadIdentities() {
      this.loadingIdentities = true
      try {
        this.identities = await api('GET', '/api/auth/profile/identities')
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      } finally {
        this.loadingIdentities = false
      }
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

    platformLabel: { telegram: 'Telegram', qq: 'QQ', feishu: 'Feishu' },

    isLinked(platform) {
      return this.identities.some(i => i.platform === platform)
    },

    async generateCode(platform) {
      this.generating = true
      this.linkPlatform = platform
      this.linkCode = ''
      try {
        const result = await api('POST', '/api/auth/profile/link-code', { platform })
        this.linkCode = result.code
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      } finally {
        this.generating = false
      }
    },

    async unlinkIdentity(id) {
      if (!confirm('Unlink this identity?')) return
      try {
        await api('DELETE', '/api/auth/profile/identities/' + id)
        this.$store.toast.show('Identity unlinked')
        await this.loadIdentities()
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      }
    },
  }))
}
