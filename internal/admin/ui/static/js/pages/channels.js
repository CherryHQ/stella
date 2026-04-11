import { api } from '/static/js/api.js'
import QRCode from 'https://esm.sh/qrcode@1.5.4'

const channelTypes = [
  { id: 'telegram', label: 'Telegram' },
  { id: 'qq', label: 'QQ' },
  { id: 'feishu', label: 'Feishu' },
  { id: 'weixin', label: 'Weixin' },
]

function parseConfig(raw) {
  try {
    return JSON.parse(raw || '{}')
  } catch {
    return {}
  }
}

function platformDefaults(type) {
  switch (type) {
    case 'telegram':
      return {
        enable_notify: false,
        token: '',
        channel_id: '',
        group_mode: '',
      }
    case 'qq':
      return {
        enable_notify: false,
        app_id: '',
        app_secret: '',
        group_mode: '',
      }
    case 'feishu':
      return {
        enable_notify: false,
        app_id: '',
        app_secret: '',
        encrypt_key: '',
        verification_token: '',
        group_mode: '',
      }
    case 'weixin':
      return {
        enable_notify: false,
      }
    default:
      return {}
  }
}

function serializePlatformConfig(type, data) {
  switch (type) {
    case 'telegram':
      return {
        enable_notify: !!data.enable_notify,
        token: data.token || '',
        channel_id: data.channel_id || '',
        group_mode: data.group_mode || '',
      }
    case 'qq':
      return {
        enable_notify: !!data.enable_notify,
        app_id: data.app_id || '',
        app_secret: data.app_secret || '',
        group_mode: data.group_mode || '',
      }
    case 'feishu':
      return {
        enable_notify: !!data.enable_notify,
        app_id: data.app_id || '',
        app_secret: data.app_secret || '',
        encrypt_key: data.encrypt_key || '',
        verification_token: data.verification_token || '',
        group_mode: data.group_mode || '',
      }
    case 'weixin':
      return {
        enable_notify: !!data.enable_notify,
      }
    default:
      return {}
  }
}

function hasConfig(type, data) {
  const cfg = serializePlatformConfig(type, data)
  return Object.values(cfg).some(value => {
    if (typeof value === 'boolean') return value
    return String(value || '').trim() !== ''
  })
}

function newInstanceDraft(type = 'telegram', id = '') {
  return {
    id,
    type,
    ...platformDefaults(type),
  }
}

function normalizeChannel(ch) {
  const type = ch.type || ch.id
  const cfg = parseConfig(ch.config)
  return {
    ...ch,
    type,
    agent_id: ch.agent_id || '',
    _collapsed: true,
    ...platformDefaults(type),
    ...cfg,
  }
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
    publicChannels: [],
    linkedIdentities: [],
    instances: [],
    loadingPlatforms: false,
    loadingInstances: false,
    platformLabel: { telegram: 'Telegram', qq: 'QQ', feishu: 'Feishu', weixin: 'Weixin' },

    linkCode: '',
    linkPlatform: '',
    generating: false,
    wxQrUrl: '',
    wxQrStatus: '',
    wxQrCode: '',
    wxQrPolling: false,
    _wxQrInterval: null,

    creatingInstance: false,
    showNewInstanceForm: false,
    newChannel: newInstanceDraft(),

    confirmMsg: '',
    confirmAction: () => {},

    get visibleChannelTypes() {
      return this.channelTypes.filter(type => this.enabledChannelTypeIDs.includes(type.id))
    },

    async init() {
      await this.loadCurrentUser()
      if (this.isAdmin) {
        await Promise.all([
          this.loadChannelPlugins(),
          this.loadPlatformsAndIdentities(),
          this.loadInstances(),
        ])
        this.syncNewChannelType(this.newChannel.type)
        return
      }
      await this.loadPlatformsAndIdentities()
    },

    async loadCurrentUser() {
      try {
        const me = await api('GET', '/api/auth/me')
        this.isAdmin = Boolean(me?.is_admin)
      } catch (e) {
        console.error(e)
      }
    },

    async loadPlatformsAndIdentities() {
      await Promise.all([this.loadPublicChannels(), this.loadIdentities()])
    },

    async loadPublicChannels() {
      this.loadingPlatforms = true
      try {
        this.publicChannels = await api('GET', '/api/channels/public') || []
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      } finally {
        this.loadingPlatforms = false
      }
    },

    async loadIdentities() {
      try {
        this.linkedIdentities = await api('GET', '/api/auth/profile/identities') || []
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
      } catch (e) {
        console.error(e)
        this.enabledChannelTypeIDs = this.channelTypes.map(type => type.id)
      }
    },

    async loadInstances() {
      this.loadingInstances = true
      try {
        const channels = await api('GET', '/api/channels') || []
        this.instances = channels
          .map(normalizeChannel)
          .filter(ch => ch.id !== ch.type && this.enabledChannelTypeIDs.includes(ch.type) && ch.enabled)
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      } finally {
        this.loadingInstances = false
      }
    },

    syncNewChannelType(type) {
      const nextType = this.enabledChannelTypeIDs.includes(type)
        ? type
        : this.visibleChannelTypes[0]?.id || ''
      this.newChannel = newInstanceDraft(nextType, this.newChannel.id || '')
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

    platformDescription(channel) {
      if (!channel) return ''
      if (this.isLinked(channel.type)) {
        return 'Your ' + channel.label + ' account is linked and ready to use with Anna.'
      }
      if (channel.type === 'weixin') {
        return 'Link your Weixin account by scanning a QR code.'
      }
      return 'Link your ' + channel.label + ' account once to chat with Anna on this platform.'
    },

    linkedAgentLabel(channel) {
      if (!channel?.agent_id) return ''
      return channel.agent_name ? channel.agent_name + ' (' + channel.agent_id + ')' : channel.agent_id
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

    clearWeixinQRInterval() {
      if (!this._wxQrInterval) return
      clearInterval(this._wxQrInterval)
      this._wxQrInterval = null
    },

    stopWeixinQRPolling() {
      this.clearWeixinQRInterval()
      this.wxQrPolling = false
    },

    async startWeixinQR() {
      this.linkCode = ''
      this.wxQrUrl = ''
      this.wxQrStatus = ''
      this.wxQrCode = ''
      this.wxQrPolling = true
      this.clearWeixinQRInterval()
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
          this.stopWeixinQRPolling()
          this.wxQrUrl = ''
          this.$store.toast.show('Weixin account linked successfully')
          await this.loadIdentities()
        } else if (result.status === 'expired') {
          this.stopWeixinQRPolling()
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

    toggleNewInstanceForm() {
      this.showNewInstanceForm = !this.showNewInstanceForm
      if (this.showNewInstanceForm) {
        this.syncNewChannelType(this.newChannel.type)
      }
    },

    instanceStatus(ch) {
      return hasConfig(ch.type, ch) ? 'Configured' : 'Needs config'
    },

    instanceStatusClass(ch) {
      return hasConfig(ch.type, ch) ? 'badge-success' : 'badge-ghost'
    },

    async createChannel() {
      const id = this.newChannel.id.trim()
      if (!id || !this.newChannel.type) {
        this.$store.toast.show('ID and platform are required', 'error')
        return
      }
      if (id === this.newChannel.type) {
        this.$store.toast.show('Dedicated instance ID must not match the platform ID', 'error')
        return
      }

      this.creatingInstance = true
      try {
        const saved = await api('POST', '/api/channels', {
          id,
          type: this.newChannel.type,
          agent_id: '',
          config: JSON.stringify(serializePlatformConfig(this.newChannel.type, this.newChannel)),
        })
        this.newChannel = newInstanceDraft(saved.type || this.visibleChannelTypes[0]?.id || '')
        this.showNewInstanceForm = false
        await this.loadInstances()
        this.$store.toast.show(saved.id + ' created')
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      } finally {
        this.creatingInstance = false
      }
    },

    async saveInstance(ch) {
      try {
        const saved = await api('PUT', '/api/channels/' + encodeURIComponent(ch.id), {
          type: ch.type,
          agent_id: ch.agent_id || '',
          config: JSON.stringify(serializePlatformConfig(ch.type, ch)),
        })
        Object.assign(ch, normalizeChannel(saved))
        ch._collapsed = false
        this.$store.toast.show(ch.id + ' saved')
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      }
    },

    async doDeleteChannel(id) {
      try {
        await api('DELETE', '/api/channels/' + encodeURIComponent(id))
        await this.loadInstances()
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
