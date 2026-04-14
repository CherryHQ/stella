import { api } from '/static/js/api.js'
import QRCode from 'https://esm.sh/qrcode@1.5.4'

const platformMeta = {
  telegram: {
    label: 'Telegram',
    defaults: {
      enable_notify: false,
      token: '',
      channel_id: '',
      group_mode: '',
    },
  },
  qq: {
    label: 'QQ',
    defaults: {
      enable_notify: false,
      app_id: '',
      app_secret: '',
      group_mode: '',
    },
  },
  feishu: {
    label: 'Feishu',
    defaults: {
      enable_notify: false,
      app_id: '',
      app_secret: '',
      encrypt_key: '',
      verification_token: '',
      group_mode: '',
    },
  },
  weixin: {
    label: 'Weixin',
    defaults: {
      enable_notify: false,
    },
  },
}

const channelTypes = Object.entries(platformMeta).map(([id, meta]) => ({ id, label: meta.label }))
const defaultChannelType = channelTypes[0]?.id || ''

function parseConfig(raw) {
  try {
    return JSON.parse(raw || '{}')
  } catch {
    return {}
  }
}

function platformConfigDefaults(type) {
  return { ...(platformMeta[type]?.defaults || {}) }
}

function normalizeConfigValue(defaultValue, value) {
  if (typeof defaultValue === 'boolean') {
    return Boolean(value)
  }
  return value || ''
}

function serializePlatformConfig(type, data) {
  return Object.fromEntries(
    Object.entries(platformConfigDefaults(type)).map(([key, defaultValue]) => [
      key,
      normalizeConfigValue(defaultValue, data[key]),
    ]),
  )
}

function hasConfig(type, data) {
  return Object.values(serializePlatformConfig(type, data)).some(value => {
    if (typeof value === 'boolean') return value
    return String(value).trim() !== ''
  })
}

function newInstanceDraft(type = defaultChannelType, id = '') {
  return {
    id,
    type,
    ...platformConfigDefaults(type),
  }
}

function normalizeChannel(ch) {
  const type = ch.type || ch.id
  return {
    ...ch,
    type,
    agent_id: ch.agent_id || '',
    _collapsed: true,
    ...platformConfigDefaults(type),
    ...parseConfig(ch.config),
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

    get fallbackChannelType() {
      return this.visibleChannelTypes[0]?.id || defaultChannelType
    },

    configForPlatform(type) {
      return this.instances.find(ch => ch.type === type && ch.id === type) || null
    },

    dedicatedInstancesFor(type) {
      return this.instances.filter(ch => ch.type === type && ch.id !== type)
    },

    hasDedicatedInstancesFor(type) {
      return this.dedicatedInstancesFor(type).length > 0
    },

    isAddingInstanceFor(type) {
      return this.showNewInstanceForm && this.newChannel.type === type
    },

    async init() {
      await this.loadCurrentUser()
      if (this.isAdmin) {
        await Promise.all([
          this.loadChannelPlugins(),
          this.loadPlatformsAndIdentities(),
          this.loadInstances(),
        ])
        this.resetNewChannel(this.newChannel.type, this.newChannel.id)
        return
      }
      await this.loadPlatformsAndIdentities()
    },

    resetNewChannel(type = this.fallbackChannelType, id = '') {
      const nextType = this.enabledChannelTypeIDs.includes(type) ? type : this.fallbackChannelType
      this.newChannel = newInstanceDraft(nextType, id)
    },

    openNewInstanceForm(type = this.fallbackChannelType) {
      this.resetNewChannel(type)
      this.showNewInstanceForm = true
    },

    closeNewInstanceForm() {
      this.showNewInstanceForm = false
      this.resetNewChannel(this.newChannel.type, '')
    },

    resetLinkState() {
      this.linkCode = ''
      this.wxQrUrl = ''
      this.wxQrStatus = ''
      this.wxQrCode = ''
    },

    platformLabel(type) {
      return platformMeta[type]?.label || type
    },

    channelConfig(ch) {
      return JSON.stringify(serializePlatformConfig(ch.type, ch))
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
        this.enabledChannelTypeIDs = enabled
      } catch (e) {
        console.error(e)
        this.enabledChannelTypeIDs = channelTypes.map(type => type.id)
      }
    },

    async loadInstances() {
      this.loadingInstances = true
      try {
        const channels = await api('GET', '/api/channels') || []
        this.instances = channels
          .map(normalizeChannel)
          .filter(ch => this.enabledChannelTypeIDs.includes(ch.type) && ch.enabled)
          .sort((left, right) => {
            const leftDefault = left.id === left.type
            const rightDefault = right.id === right.type
            if (leftDefault !== rightDefault) return leftDefault ? -1 : 1
            if (left.type !== right.type) return left.type.localeCompare(right.type)
            return left.id.localeCompare(right.id)
          })
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      } finally {
        this.loadingInstances = false
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
      this.resetLinkState()
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
      this.resetLinkState()
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

    toggleNewInstanceForm(type = this.newChannel.type) {
      if (this.showNewInstanceForm && this.newChannel.type === type) {
        this.closeNewInstanceForm()
        return
      }
      this.openNewInstanceForm(type)
    },

    isDefaultPlatformInstance(ch) {
      return Boolean(ch) && ch.id === ch.type
    },

    instanceKindLabel(ch) {
      return this.isDefaultPlatformInstance(ch) ? 'platform default' : 'dedicated'
    },

    instanceDescription(ch) {
      if (this.isDefaultPlatformInstance(ch)) {
        return 'Default platform channel. Configure the shared bot/account credentials used for this platform.'
      }
      return 'Dedicated instance. Configure it here, then attach it from the agent page.'
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
          config: this.channelConfig(this.newChannel),
        })
        this.resetNewChannel(saved.type || this.fallbackChannelType)
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
          config: this.channelConfig(ch),
        })
        Object.assign(ch, normalizeChannel(saved))
        ch._collapsed = false
        this.$store.toast.show(ch.id + ' saved')
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      }
    },

    async doDeleteChannel(id) {
      const ch = this.instances.find(channel => channel.id === id)
      if (this.isDefaultPlatformInstance(ch)) {
        this.$store.toast.show('Default platform channels cannot be deleted', 'error')
        return
      }
      try {
        await api('DELETE', '/api/channels/' + encodeURIComponent(id))
        await this.loadInstances()
        this.$store.toast.show(id + ' deleted')
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      }
    },

    syncNewChannelType(type) {
      this.resetNewChannel(type, this.newChannel.id)
    },

    confirmDelete(message, action) {
      this.confirmMsg = message
      this.confirmAction = action
    },
  }))
}
