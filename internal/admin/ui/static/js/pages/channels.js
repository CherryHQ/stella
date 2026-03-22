import { api } from '/static/js/api.js'
import QRCode from 'https://esm.sh/qrcode@1.5.4'

/**
 * Parses a comma-separated string into an array.
 * When numeric is true, values are converted to numbers (for Telegram IDs).
 *
 * @param {string} str
 * @param {boolean} numeric
 * @returns {Array}
 */
function parseAllowedIds(str, numeric = false) {
  const parts = str.split(',').map(s => s.trim()).filter(Boolean)
  return numeric ? parts.map(Number) : parts
}

/**
 * Formats an array of IDs into a comma-separated display string.
 *
 * @param {Array} arr
 * @returns {string}
 */
function formatAllowedIds(arr) {
  return (arr || []).join(', ')
}

/**
 * Registers the channelsPage Alpine.data component.
 *
 * @param {import('alpinejs').Alpine} Alpine
 */
export function register(Alpine) {
  Alpine.data('channelsPage', () => ({
    channelData: {
      telegram: {
        enabled: false, enable_notify: false,
        token: '', notify_chat: '', channel_id: '',
        group_mode: '', allowed_ids: [],
      },
      qq: {
        enabled: false, enable_notify: false,
        app_id: '', app_secret: '',
        group_mode: '', allowed_ids: [],
      },
      feishu: {
        enabled: false, enable_notify: false,
        app_id: '', app_secret: '', encrypt_key: '',
        verification_token: '', notify_chat: '',
        group_mode: '', allowed_ids: [],
      },
      weixin: {
        enabled: false, enable_notify: false,
        notify_chat: '', allowed_ids: [],
        bot_token: '', base_url: '', bot_id: '', user_id: '',
      },
    },

    // QR login state
    qrUrl: '',
    qrStatus: '',
    qrCode: '',
    qrPolling: false,
    _qrInterval: null,

    // Expose helpers to templates
    parseAllowedIds,
    formatAllowedIds,

    async init() {
      await this.loadChannels()
    },

    async loadChannels() {
      try {
        const channels = await api('GET', '/api/channels') || []
        for (const ch of channels) {
          let cfg = {}
          try { cfg = JSON.parse(ch.config || '{}') } catch (_) { /* ignore */ }
          if (ch.id === 'telegram') {
            this.channelData.telegram = {
              enabled: ch.enabled,
              enable_notify: cfg.enable_notify || false,
              token: cfg.token || '',
              notify_chat: cfg.notify_chat || '',
              channel_id: cfg.channel_id || '',
              group_mode: cfg.group_mode || '',
              allowed_ids: cfg.allowed_ids || [],
            }
          } else if (ch.id === 'qq') {
            this.channelData.qq = {
              enabled: ch.enabled,
              enable_notify: cfg.enable_notify || false,
              app_id: cfg.app_id || '',
              app_secret: cfg.app_secret || '',
              group_mode: cfg.group_mode || '',
              allowed_ids: cfg.allowed_ids || [],
            }
          } else if (ch.id === 'feishu') {
            this.channelData.feishu = {
              enabled: ch.enabled,
              enable_notify: cfg.enable_notify || false,
              app_id: cfg.app_id || '',
              app_secret: cfg.app_secret || '',
              encrypt_key: cfg.encrypt_key || '',
              verification_token: cfg.verification_token || '',
              notify_chat: cfg.notify_chat || '',
              group_mode: cfg.group_mode || '',
              allowed_ids: cfg.allowed_ids || [],
            }
          } else if (ch.id === 'weixin') {
            this.channelData.weixin = {
              enabled: ch.enabled,
              enable_notify: cfg.enable_notify || false,
              notify_chat: cfg.notify_chat || '',
              allowed_ids: cfg.allowed_ids || [],
              bot_token: cfg.bot_token || '',
              base_url: cfg.base_url || '',
              bot_id: cfg.bot_id || '',
              user_id: cfg.user_id || '',
            }
          }
        }
      } catch (e) {
        console.error(e)
      }
    },

    async saveChannel(platform) {
      try {
        const data = { ...this.channelData[platform] }
        const enabled = data.enabled
        delete data.enabled
        await api('PUT', '/api/channels/' + platform, {
          enabled,
          config: JSON.stringify(data),
        })
        await this.loadChannels()
        this.$store.toast.show(platform + ' saved')
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      }
    },

    async startQR() {
      this.qrUrl = ''
      this.qrStatus = ''
      this.qrCode = ''
      this.qrPolling = true
      if (this._qrInterval) {
        clearInterval(this._qrInterval)
        this._qrInterval = null
      }
      try {
        const result = await api('POST', '/api/channels/weixin/qr')
        this.qrCode = result.qrcode || ''
        // Generate QR code image client-side from the URL
        const imgContent = result.qrcode_img_content || ''
        if (imgContent) {
          this.qrUrl = await QRCode.toDataURL(imgContent, { width: 256, margin: 2 })
        }
        this.qrStatus = 'waiting'
        this._qrInterval = setInterval(() => this.pollQRStatus(), 3000)
      } catch (e) {
        this.$store.toast.show('QR code request failed: ' + e.message, 'error')
        this.qrPolling = false
      }
    },

    async pollQRStatus() {
      if (!this.qrCode) return
      try {
        const result = await api('GET', '/api/channels/weixin/qr/status?qrcode=' + encodeURIComponent(this.qrCode))
        if (result.status) {
          this.qrStatus = result.status
        }
        if (result.status === 'confirmed') {
          clearInterval(this._qrInterval)
          this._qrInterval = null
          this.qrPolling = false
          this.$store.toast.show('Weixin login successful')
          await this.loadChannels()
        } else if (result.status === 'expired') {
          clearInterval(this._qrInterval)
          this._qrInterval = null
          this.qrPolling = false
        }
      } catch (e) {
        console.error('QR status poll error:', e)
      }
    },
  }))
}
