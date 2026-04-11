import { api } from '/static/js/api.js'
import QRCode from 'https://esm.sh/qrcode@1.5.4'

const channelTypes = [
  { id: 'telegram', label: 'Telegram' },
  { id: 'qq', label: 'QQ' },
  { id: 'feishu', label: 'Feishu' },
  { id: 'weixin', label: 'Weixin' },
]

function defaultChannelData() {
  return {
    telegram: {
      enabled: false, agent_id: '', enable_notify: false,
      token: '', channel_id: '', group_mode: '',
    },
    qq: {
      enabled: false, agent_id: '', enable_notify: false,
      app_id: '', app_secret: '', group_mode: '',
    },
    feishu: {
      enabled: false, agent_id: '', enable_notify: false,
      app_id: '', app_secret: '', encrypt_key: '',
      verification_token: '', group_mode: '',
    },
    weixin: {
      enabled: false, agent_id: '', enable_notify: false,
    },
  }
}

function parseConfig(raw) {
  try {
    return JSON.parse(raw || '{}')
  } catch {
    return {}
  }
}

function configString(value) {
  return JSON.stringify(value || {}, null, 2)
}

function normalizeChannel(ch) {
  return {
    ...ch,
    type: ch.type || ch.id,
    agent_id: ch.agent_id || '',
    config: configString(parseConfig(ch.config)),
    _collapsed: ch.id === (ch.type || ch.id),
  }
}

function groupChannelsByType(channels) {
  const groups = []
  const byType = new Map()

  for (const channel of channels) {
    const type = channel.type || channel.id
    if (!byType.has(type)) {
      const group = { type, channels: [] }
      byType.set(type, group)
      groups.push(group)
    }
    byType.get(type).channels.push(channel)
  }

  for (const group of groups) {
    group.channels.sort((a, b) => a.id.localeCompare(b.id))
  }
  groups.sort((a, b) => a.type.localeCompare(b.type))
  return groups
}

function legacyConfig(platform, data) {
  const cfg = { ...data }
  delete cfg.enabled
  delete cfg.agent_id
  return cfg
}

/**
 * Registers the channelsPage Alpine.data component.
 *
 * @param {import('alpinejs').Alpine} Alpine
 */
export function register(Alpine) {
  Alpine.data('channelsPage', () => ({
    channelTypes,
    enabledChannelTypeIDs: channelTypes.map(type => type.id),
    isAdmin: false,
    agents: [],
    channels: [],
    linkablePlatforms: [],
    linkedIdentities: [],
    loadingLinks: false,
    linkCode: '',
    linkPlatform: '',
    generating: false,
    wxQrUrl: '',
    wxQrStatus: '',
    wxQrCode: '',
    wxQrPolling: false,
    _wxQrInterval: null,
    channelData: defaultChannelData(),
    newChannel: {
      id: '',
      type: 'telegram',
      agent_id: '',
      enabled: true,
      config: '{}',
    },

    confirmMsg: '',
    confirmAction: () => {},

    get groupedChannels() {
      return groupChannelsByType(this.channels)
    },

    get visibleChannelTypes() {
      return this.channelTypes.filter(type => this.channelTypeEnabled(type.id))
    },

    channelTypeEnabled(type) {
      return this.enabledChannelTypeIDs.includes(type)
    },

    async init() {
      await this.loadCurrentUser()
      await Promise.all([this.loadLinkOptions(), this.loadIdentities()])
      if (!this.isAdmin) return
      await this.loadChannelPlugins()
      await Promise.all([this.loadAgents(), this.loadChannels()])
    },

    async loadCurrentUser() {
      try {
        const me = await api('GET', '/api/auth/me')
        this.isAdmin = Boolean(me?.is_admin)
      } catch (e) {
        console.error(e)
      }
    },

    async loadLinkOptions() {
      this.loadingLinks = true
      try {
        this.linkablePlatforms = await api('GET', '/api/channels/link-options') || []
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      } finally {
        this.loadingLinks = false
      }
    },

    async loadIdentities() {
      try {
        this.linkedIdentities = await api('GET', '/api/auth/profile/identities') || []
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      }
    },

    identityFor(platform) {
      return this.linkedIdentities.find(identity => identity.platform === platform) || null
    },

    isLinked(platform) {
      return Boolean(this.identityFor(platform))
    },

    identityLabel(identity) {
      if (!identity) return ''
      const name = identity.name ? identity.name + ' · ' : ''
      return name + identity.external_id
    },

    linkHint(platform) {
      if (this.isLinked(platform)) {
        return 'Used for this platform and any agent-dedicated channels on it.'
      }
      if (platform === 'weixin') {
        return 'Scan a QR code to connect this account.'
      }
      return 'Generate a one-time code, then send /link to Anna on this platform.'
    },

    async generateCode(platform) {
      this.generating = true
      this.linkPlatform = platform
      this.linkCode = ''
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

    copyLinkCode() {
      navigator.clipboard.writeText('/link ' + this.linkCode)
      this.$store.toast.show('Copied')
    },

    async startWeixinQR() {
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
      if (!id || !confirm('Unlink this identity?')) return
      try {
        await api('DELETE', '/api/auth/profile/identities/' + id)
        this.$store.toast.show('Identity unlinked')
        await this.loadIdentities()
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      }
    },

    async loadChannelPlugins() {
      try {
        const plugins = await api('GET', '/api/plugins') || []
        const enabled = plugins
          .filter(p => p.kind === 'channel' && p.enabled)
          .map(p => p.name || String(p.id || '').replace(/^channel\//, ''))
        this.enabledChannelTypeIDs = enabled.length > 0 ? enabled : []
        if (!this.channelTypeEnabled(this.newChannel.type)) {
          this.newChannel.type = this.visibleChannelTypes[0]?.id || ''
        }
      } catch (e) {
        console.error(e)
        this.enabledChannelTypeIDs = this.channelTypes.map(type => type.id)
      }
    },

    async loadAgents() {
      try {
        this.agents = await api('GET', '/api/agents') || []
      } catch (e) {
        console.error(e)
      }
    },

    async loadChannels() {
      try {
        const channels = await api('GET', '/api/channels') || []
        this.channelData = defaultChannelData()
        this.channels = channels
          .map(normalizeChannel)
          .filter(ch => this.channelTypeEnabled(ch.type))

        for (const ch of this.channels) {
          const cfg = parseConfig(ch.config)
          if (ch.id === 'telegram') {
            this.channelData.telegram = {
              enabled: ch.enabled,
              agent_id: ch.agent_id || '',
              enable_notify: cfg.enable_notify || false,
              token: cfg.token || '',
              channel_id: cfg.channel_id || '',
              group_mode: cfg.group_mode || '',
            }
          } else if (ch.id === 'qq') {
            this.channelData.qq = {
              enabled: ch.enabled,
              agent_id: ch.agent_id || '',
              enable_notify: cfg.enable_notify || false,
              app_id: cfg.app_id || '',
              app_secret: cfg.app_secret || '',
              group_mode: cfg.group_mode || '',
            }
          } else if (ch.id === 'feishu') {
            this.channelData.feishu = {
              enabled: ch.enabled,
              agent_id: ch.agent_id || '',
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
              agent_id: ch.agent_id || '',
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
        await api('PUT', '/api/channels/' + platform, {
          type: platform,
          agent_id: data.agent_id || '',
          enabled: data.enabled,
          config: JSON.stringify(legacyConfig(platform, data)),
        })
        await this.loadChannels()
        this.$store.toast.show(platform + ' saved')
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      }
    },

    async createChannel() {
      try {
        JSON.parse(this.newChannel.config || '{}')
        const saved = await api('POST', '/api/channels', {
          id: this.newChannel.id.trim(),
          type: this.newChannel.type,
          agent_id: this.newChannel.agent_id || '',
          enabled: this.newChannel.enabled,
          config: this.newChannel.config || '{}',
        })
        this.newChannel = { id: '', type: saved.type || 'telegram', agent_id: '', enabled: true, config: '{}' }
        await this.loadChannels()
        this.$store.toast.show(saved.id + ' created')
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      }
    },

    async saveInstance(ch) {
      try {
        const cfg = JSON.parse(ch.config || '{}')
        const saved = await api('PUT', '/api/channels/' + encodeURIComponent(ch.id), {
          type: ch.type,
          agent_id: ch.agent_id || '',
          enabled: ch.enabled,
          config: JSON.stringify(cfg),
        })
        ch.config = configString(parseConfig(saved.config))
        ch.agent_id = saved.agent_id || ''
        ch.enabled = saved.enabled
        await this.loadChannels()
        this.$store.toast.show(ch.id + ' saved')
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      }
    },

    async doDeleteChannel(id) {
      try {
        await api('DELETE', '/api/channels/' + encodeURIComponent(id))
        await this.loadChannels()
        this.$store.toast.show(id + ' deleted')
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      }
    },

    confirmDelete(message, action) {
      this.confirmMsg = message
      this.confirmAction = action
    },
  }))
}
