import { api } from '/static/js/api.js'
import { formatTime } from '/static/js/utils.js'
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

    // Vault
    vaultEntries: [],
    vaultLoading: false,
    vaultSaving: false,
    newSecretName: '',
    newSecretValue: '',

    // OAuth CLI
    oauthStatus: { github: 'checking', lark: 'checking' },
    oauthFlow: { github: null, lark: null },
    oauthFlowActive: { github: false, lark: false },

    async init() {
      await this.loadMySkillsCount()
      await this.loadVaultEntries()
      await Promise.all([
        this.checkOAuthConnected('github'),
        this.checkOAuthConnected('lark'),
      ])
    },

    formatTime,

    async loadVaultEntries() {
      this.vaultLoading = true
      try {
        this.vaultEntries = (await api('GET', '/api/auth/profile/vault')) || []
      } catch (_) {
        this.vaultEntries = []
      } finally {
        this.vaultLoading = false
      }
    },

    async addVaultEntry() {
      if (!this.newSecretName) {
        this.$store.toast.show('Secret name is required', 'error')
        return
      }
      if (!this.newSecretValue) {
        this.$store.toast.show('Secret value is required', 'error')
        return
      }
      this.vaultSaving = true
      try {
        await api('PUT', `/api/auth/profile/vault/${encodeURIComponent(this.newSecretName)}`, { value: this.newSecretValue })
        this.$store.toast.show('Secret saved')
        this.newSecretName = ''
        this.newSecretValue = ''
        await this.loadVaultEntries()
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      } finally {
        this.vaultSaving = false
      }
    },

    async deleteVaultEntry(name) {
      if (!confirm(`Delete secret "${name}"?`)) return
      try {
        await api('DELETE', `/api/auth/profile/vault/${encodeURIComponent(name)}`)
        this.$store.toast.show('Secret deleted')
        await this.loadVaultEntries()
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      }
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

    // --- OAuth CLI helpers ---

    async checkOAuthConnected(provider) {
      this.oauthStatus[provider] = 'checking'
      try {
        const data = await api('GET', `/api/auth/profile/oauth/${provider}/connected`)
        this.oauthStatus[provider] = data && data.connected ? 'connected' : 'disconnected'
      } catch (_) {
        this.oauthStatus[provider] = 'disconnected'
      }
    },

    async connectOAuth(provider) {
      this.oauthFlowActive[provider] = true
      this.oauthFlow[provider] = null
      try {
        const flow = await api('POST', `/api/auth/profile/oauth/${provider}/start`)
        this.oauthFlow[provider] = flow
        // Poll until terminal state.
        await this._pollUntilDone(provider, flow.flow_id)
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      } finally {
        this.oauthFlowActive[provider] = false
        this.oauthFlow[provider] = null
        await this.checkOAuthConnected(provider)
      }
    },

    async _pollUntilDone(provider, flowID) {
      const interval = provider === 'github' ? 5000 : 3000
      while (true) {
        await new Promise(r => setTimeout(r, interval))
        let status
        try {
          status = await api('GET', `/api/auth/profile/oauth/${provider}/status/${flowID}`)
        } catch (_) {
          break
        }
        if (!status || status.state !== 'pending') {
          if (status && status.state === 'authorized') {
            this.$store.toast.show(`${provider} connected successfully`)
          } else if (status) {
            this.$store.toast.show(`${provider} authorization ${status.state}`, 'error')
          }
          break
        }
      }
    },

    async disconnectOAuth(provider) {
      if (!confirm(`Disconnect ${provider} credentials?`)) return
      try {
        await api('DELETE', `/api/auth/profile/oauth/${provider}`)
        this.$store.toast.show(`${provider} disconnected`)
        await this.checkOAuthConnected(provider)
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      }
    },

    // --- End OAuth CLI helpers ---

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
