import { api } from '/static/js/api.js'

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
    },

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
  }))
}
