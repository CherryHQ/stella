import { api } from '/static/js/api.js'

/**
 * Registers the pluginsPage Alpine.data component.
 *
 * @param {import('alpinejs').Alpine} Alpine
 */
export function register(Alpine) {
  Alpine.data('pluginsPage', () => ({
    plugins: [],

    get toolPlugins() {
      return this.plugins.filter(p => p.kind === 'tool')
    },

    get channelPlugins() {
      return this.plugins.filter(p => p.kind === 'channel')
    },

    get hookPlugins() {
      return this.plugins.filter(p => p.kind === 'hook')
    },

    async init() {
      await this.loadPlugins()
    },

    async loadPlugins() {
      try {
        this.plugins = await api('GET', '/api/plugins') || []
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      }
    },

    async togglePlugin(id, enabled) {
      try {
        await api('PATCH', '/api/plugins/' + encodeURIComponent(id), { enabled })
        await this.loadPlugins()
        this.$store.toast.show(id + (enabled ? ' enabled' : ' disabled'))
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      }
    },

    descriptionFor(name) {
      const descriptions = {
        webfetch: 'Fetch and extract web page content',
        telegram: 'Telegram bot integration',
        qq: 'QQ bot integration',
        feishu: 'Feishu (Lark) bot integration',
        weixin: 'WeChat bot integration',
        trace: 'Trace LLM calls and tool executions',
        rtk: 'Rewrite bash commands via rtk',
      }
      return descriptions[name] || ''
    },
  }))
}
