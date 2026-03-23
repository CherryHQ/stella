import { api } from '/static/js/api.js'

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
        token: '', channel_id: '', group_mode: '',
      },
      qq: {
        enabled: false, enable_notify: false,
        app_id: '', app_secret: '', group_mode: '',
      },
      feishu: {
        enabled: false, enable_notify: false,
        app_id: '', app_secret: '', encrypt_key: '',
        verification_token: '', group_mode: '',
      },
      weixin: {
        enabled: false, enable_notify: false,
      },
    },

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
              channel_id: cfg.channel_id || '',
              group_mode: cfg.group_mode || '',
            }
          } else if (ch.id === 'qq') {
            this.channelData.qq = {
              enabled: ch.enabled,
              enable_notify: cfg.enable_notify || false,
              app_id: cfg.app_id || '',
              app_secret: cfg.app_secret || '',
              group_mode: cfg.group_mode || '',
            }
          } else if (ch.id === 'feishu') {
            this.channelData.feishu = {
              enabled: ch.enabled,
              enable_notify: cfg.enable_notify || false,
              app_id: cfg.app_id || '',
              app_secret: cfg.app_secret || '',
              encrypt_key: cfg.encrypt_key || '',
              verification_token: cfg.verification_token || '',
              group_mode: cfg.group_mode || '',
            }
          } else if (ch.id === 'weixin') {
            this.channelData.weixin = {
              enabled: ch.enabled,
              enable_notify: cfg.enable_notify || false,
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
