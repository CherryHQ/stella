import { api } from '/static/js/api.js'

/**
 * Registers the pluginsPage Alpine.data component.
 *
 * @param {import('alpinejs').Alpine} Alpine
 */
export function register(Alpine) {
  Alpine.data('pluginsPage', () => ({
    plugins: [],
    mcpServers: [],

    get toolPlugins() {
      return this.plugins.filter(p => p.kind === 'tool')
    },

    get channelPlugins() {
      return this.plugins.filter(p => p.kind === 'channel')
    },

    get hookPlugins() {
      return this.plugins.filter(p => p.kind === 'hook')
    },

    get memoryPlugins() {
      return this.plugins.filter(p => p.kind === 'memory')
    },

    get standalonePlugins() {
      return this.plugins.filter(p => !['tool', 'channel', 'hook', 'memory', 'provider'].includes(p.kind))
    },

    async init() {
      await this.loadPlugins()
    },

    async loadPlugins() {
      try {
        this.plugins = await api('GET', '/api/plugins') || []
        const mcpPlugin = this.plugins.find(p => p.id === 'tool/mcp')
        this.mcpServers = this.normalizeMcpServers(mcpPlugin?.config?.servers || [])
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

    normalizeMcpServers(servers) {
      return (servers || []).map(server => ({
        name: server.name || '',
        enabled: server.enabled !== false,
        transport: server.transport || 'stdio',
        command: server.command || '',
        url: server.url || '',
        timeout_seconds: Number(server.timeout_seconds || 30),
        args_text: JSON.stringify(server.args || []),
        env_text: JSON.stringify(server.env || {}, null, 2),
        headers_text: JSON.stringify(server.headers || {}, null, 2),
      }))
    },

    addMcpServer() {
      this.mcpServers.push({
        name: '',
        enabled: true,
        transport: 'stdio',
        command: '',
        url: '',
        timeout_seconds: 30,
        args_text: '[]',
        env_text: '{}',
        headers_text: '{}',
      })
    },

    removeMcpServer(index) {
      this.mcpServers.splice(index, 1)
    },

    parseJSONArray(label, raw) {
      if (!raw || !raw.trim()) {
        return []
      }
      const parsed = JSON.parse(raw)
      if (!Array.isArray(parsed)) {
        throw new Error(label + ' must be a JSON array')
      }
      return parsed
    },

    parseJSONObject(label, raw) {
      if (!raw || !raw.trim()) {
        return {}
      }
      const parsed = JSON.parse(raw)
      if (!parsed || Array.isArray(parsed) || typeof parsed !== 'object') {
        throw new Error(label + ' must be a JSON object')
      }
      return parsed
    },

    buildMcpConfig() {
      return {
        servers: this.mcpServers.map(server => ({
          name: server.name.trim(),
          enabled: !!server.enabled,
          transport: server.transport,
          command: (server.command || '').trim(),
          url: (server.url || '').trim(),
          timeout_seconds: Number(server.timeout_seconds || 0),
          args: this.parseJSONArray('args', server.args_text),
          env: this.parseJSONObject('env', server.env_text),
          headers: this.parseJSONObject('headers', server.headers_text),
        })),
      }
    },

    async saveMcpConfig() {
      try {
        const config = this.buildMcpConfig()
        await api('PUT', '/api/plugin-config/tool/mcp', { config })
        await this.loadPlugins()
        this.$store.toast.show('tool/mcp config saved')
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      }
    },

    descriptionFor(name) {
      const descriptions = {
        mcp: 'Connect and proxy configured MCP servers',
        webfetch: 'Fetch and extract web page content',
        telegram: 'Telegram bot integration',
        qq: 'QQ bot integration',
        feishu: 'Feishu (Lark) bot integration',
        weixin: 'WeChat bot integration',
        trace: 'Trace LLM calls and tool executions',
        rtk: 'Rewrite bash commands via rtk',
        lcm: 'Lossless context management with hierarchical summarisation',
        simple: 'Sliding window — no compaction, drops old messages',
        reflect: 'Background conversation review — extracts skills and profile updates',
      }
      return descriptions[name] || ''
    },
  }))
}
