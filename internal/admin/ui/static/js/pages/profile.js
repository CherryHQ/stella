import { api } from '/static/js/api.js'
import QRCode from 'https://esm.sh/qrcode@1.5.4'

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

    // Link code (telegram/qq/feishu)
    linkCode: '',
    linkPlatform: '',
    generating: false,

    // Weixin QR linking
    wxQrUrl: '',
    wxQrStatus: '',
    wxQrCode: '',
    wxQrPolling: false,
    _wxQrInterval: null,

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

    platformLabel: { telegram: 'Telegram', qq: 'QQ', feishu: 'Feishu', weixin: 'Weixin' },

    isLinked(platform) {
      return this.identities.some(i => i.platform === platform)
    },

    async generateCode(platform) {
      this.generating = true
      this.linkPlatform = platform
      this.linkCode = ''
      // Clear weixin QR if showing
      this.wxQrUrl = ''
      this.wxQrStatus = ''
      try {
        const result = await api('POST', '/api/auth/profile/link-code', { platform })
        this.linkCode = result.code
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      } finally {
        this.generating = false
      }
    },

    async startWeixinQR() {
      // Clear link code if showing
      this.linkCode = ''
      this.wxQrUrl = ''
      this.wxQrStatus = ''
      this.wxQrCode = ''
      this.wxQrPolling = true
      if (this._wxQrInterval) {
        clearInterval(this._wxQrInterval)
        this._wxQrInterval = null
      }
      try {
        const result = await api('POST', '/api/channels/weixin/qr')
        this.wxQrCode = result.qrcode || ''
        const imgContent = result.qrcode_img_content || ''
        if (imgContent) {
          this.wxQrUrl = await QRCode.toDataURL(imgContent, { width: 256, margin: 2 })
        }
        this.wxQrStatus = 'waiting'
        this._wxQrInterval = setInterval(() => this.pollWeixinQRStatus(), 3000)
      } catch (e) {
        this.$store.toast.show('QR request failed: ' + e.message, 'error')
        this.wxQrPolling = false
      }
    },

    async pollWeixinQRStatus() {
      if (!this.wxQrCode) return
      try {
        const result = await api('GET', '/api/channels/weixin/qr/status?qrcode=' + encodeURIComponent(this.wxQrCode))
        if (result.status) {
          this.wxQrStatus = result.status
        }
        if (result.status === 'confirmed') {
          clearInterval(this._wxQrInterval)
          this._wxQrInterval = null
          this.wxQrPolling = false
          this.wxQrUrl = ''
          this.$store.toast.show('Weixin account linked successfully')
          await this.loadIdentities()
        } else if (result.status === 'expired') {
          clearInterval(this._wxQrInterval)
          this._wxQrInterval = null
          this.wxQrPolling = false
        }
      } catch (e) {
        console.error('QR status poll error:', e)
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
