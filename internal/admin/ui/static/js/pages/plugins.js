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
    mcpStatuses: [],
    mcpSaving: false,
    mcpSavedSignature: '{"servers":[]}',
    mcpLastSavedAt: '',
    mcpRowID: 0,

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

    get mcpPlugin() {
      return this.plugins.find(p => p.id === 'tool/mcp') || null
    },

    get mcpPluginEnabled() {
      return !!this.mcpPlugin?.enabled
    },

    get mcpValidation() {
      return this.validateMcpServers()
    },

    get mcpHasErrors() {
      return this.mcpValidation.global.length > 0 || this.mcpValidation.byIndex.some(errors => errors.length > 0)
    },

    get mcpIsDirty() {
      return this.snapshotMcpSignature() !== this.mcpSavedSignature
    },

    get mcpEnabledServerCount() {
      return this.mcpServers.filter(server => server.enabled).length
    },

    get mcpRunningCount() {
      return this.mcpStatuses.filter(status => status.state === 'running').length
    },

    get mcpSuppressedCount() {
      return this.mcpStatuses.filter(status => status.suppressed).length
    },

    get mcpDiscoveredToolCount() {
      return this.mcpStatuses.reduce((total, status) => total + Number(status.discovered_tool_count || 0), 0)
    },

    async init() {
      await this.loadPlugins()
    },

    async loadPlugins() {
      try {
        this.plugins = await api('GET', '/api/plugins') || []
        const mcpPlugin = this.plugins.find(p => p.id === 'tool/mcp')
        this.mcpServers = this.normalizeMcpServers(mcpPlugin?.config?.servers || [])
        this.mcpSavedSignature = this.snapshotMcpSignature()
        await this.loadMcpStatus()
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      }
    },

    async loadMcpStatus() {
      try {
        const response = await api('GET', '/api/plugin-status/tool/mcp') || {}
        this.mcpStatuses = Array.isArray(response.servers) ? response.servers : []
      } catch {
        this.mcpStatuses = []
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

    nextMcpRowID() {
      this.mcpRowID += 1
      return this.mcpRowID
    },

    createArgsRows(args) {
      if (!Array.isArray(args) || args.length === 0) {
        return [{ id: this.nextMcpRowID(), value: '' }]
      }
      return args.map(value => ({ id: this.nextMcpRowID(), value: String(value || '') }))
    },

    createKeyValueRows(entries) {
      const pairs = Object.entries(entries || {})
      if (pairs.length === 0) {
        return [{ id: this.nextMcpRowID(), key: '', value: '' }]
      }
      return pairs.map(([key, value]) => ({ id: this.nextMcpRowID(), key, value: String(value || '') }))
    },

    normalizeMcpServers(servers) {
      return (servers || []).map(server => ({
        id: this.nextMcpRowID(),
        expanded: true,
        name: server.name || '',
        enabled: server.enabled !== false,
        transport: server.transport || 'stdio',
        command: server.command || '',
        url: server.url || '',
        timeout_seconds: Number(server.timeout_seconds || 30),
        args: this.createArgsRows(server.args || []),
        env: this.createKeyValueRows(server.env || {}),
        headers: this.createKeyValueRows(server.headers || {}),
      }))
    },

    newMcpServer() {
      return {
        id: this.nextMcpRowID(),
        expanded: true,
        name: '',
        enabled: true,
        transport: 'stdio',
        command: '',
        url: '',
        timeout_seconds: 30,
        args: this.createArgsRows([]),
        env: this.createKeyValueRows({}),
        headers: this.createKeyValueRows({}),
      }
    },

    addMcpServer() {
      this.mcpServers.push(this.newMcpServer())
    },

    duplicateMcpServer(index) {
      const source = this.mcpServers[index]
      if (!source) {
        return
      }
      const copy = this.normalizeMcpServers([this.snapshotServer(source)])[0]
      copy.name = source.name ? source.name + '-copy' : ''
      this.mcpServers.splice(index + 1, 0, copy)
    },

    removeMcpServer(index) {
      this.mcpServers.splice(index, 1)
    },

    addMcpArg(server) {
      server.args.push({ id: this.nextMcpRowID(), value: '' })
    },

    removeMcpArg(server, rowIndex) {
      server.args.splice(rowIndex, 1)
      if (server.args.length === 0) {
        this.addMcpArg(server)
      }
    },

    addMcpKeyValue(server, field) {
      server[field].push({ id: this.nextMcpRowID(), key: '', value: '' })
    },

    removeMcpKeyValue(server, field, rowIndex) {
      server[field].splice(rowIndex, 1)
      if (server[field].length === 0) {
        this.addMcpKeyValue(server, field)
      }
    },

    snapshotServer(server) {
      return {
        name: (server.name || '').trim(),
        enabled: !!server.enabled,
        transport: server.transport,
        command: (server.command || '').trim(),
        url: (server.url || '').trim(),
        timeout_seconds: Number(server.timeout_seconds || 0),
        args: this.argsFromRows(server.args),
        env: this.objectFromRows(server.env),
        headers: this.objectFromRows(server.headers),
      }
    },

    snapshotMcpConfig() {
      return {
        servers: this.mcpServers.map(server => this.snapshotServer(server)),
      }
    },

    snapshotMcpSignature() {
      return JSON.stringify(this.snapshotMcpConfig())
    },

    argsFromRows(rows) {
      return (rows || [])
        .map(row => String(row.value || ''))
        .filter(value => value !== '')
    },

    objectFromRows(rows) {
      const result = {}
      for (const row of rows || []) {
        const key = (row.key || '').trim()
        if (!key) {
          continue
        }
        result[key] = String(row.value || '')
      }
      return result
    },

    validateKeyValueRows(label, rows) {
      const errors = []
      const seen = new Set()
      for (const row of rows || []) {
        const key = (row.key || '').trim()
        const value = String(row.value || '')
        if (!key && value === '') {
          continue
        }
        if (!key) {
          errors.push(label + ' key is required')
          continue
        }
        const normalized = key.toLowerCase()
        if (seen.has(normalized)) {
          errors.push('duplicate ' + label + ' key "' + key + '"')
          continue
        }
        seen.add(normalized)
      }
      return errors
    },

    validateMcpServers() {
      const global = []
      const byIndex = this.mcpServers.map(() => [])
      const names = new Map()

      this.mcpServers.forEach((server, index) => {
        const errors = byIndex[index]
        const name = (server.name || '').trim()
        const transport = server.transport
        const timeout = Number(server.timeout_seconds)

        if (!name) {
          errors.push('Server name is required')
        } else {
          const normalized = name.toLowerCase()
          if (!names.has(normalized)) {
            names.set(normalized, [])
          }
          names.get(normalized).push(index)
        }

        if (transport === 'stdio') {
          if (!(server.command || '').trim()) {
            errors.push('Command is required for stdio transport')
          }
        } else if (['sse', 'streamable_http', 'http'].includes(transport)) {
          if (!(server.url || '').trim()) {
            errors.push('URL is required for ' + transport + ' transport')
          }
        } else {
          errors.push('Unsupported transport "' + transport + '"')
        }

        if (Number.isNaN(timeout) || timeout < 0) {
          errors.push('Timeout must be 0 or greater')
        }

        errors.push(...this.validateKeyValueRows('Environment', server.env))
        errors.push(...this.validateKeyValueRows('Header', server.headers))
      })

      for (const [name, indexes] of names.entries()) {
        if (indexes.length > 1) {
          global.push('Duplicate server name "' + name + '"')
          indexes.forEach(index => byIndex[index].push('Server names must be unique'))
        }
      }

      return { global, byIndex }
    },

    buildMcpConfig() {
      const validation = this.mcpValidation
      if (validation.global.length > 0) {
        throw new Error(validation.global[0])
      }
      const firstServerError = validation.byIndex.find(errors => errors.length > 0)
      if (firstServerError) {
        throw new Error(firstServerError[0])
      }
      return this.snapshotMcpConfig()
    },

    async saveMcpConfig() {
      try {
        this.mcpSaving = true
        const config = this.buildMcpConfig()
        await api('PUT', '/api/plugin-config/tool/mcp', { config })
        this.mcpSavedSignature = JSON.stringify(config)
        this.mcpLastSavedAt = new Date().toISOString()
        await this.loadPlugins()
        this.$store.toast.show('tool/mcp config saved')
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      } finally {
        this.mcpSaving = false
      }
    },

    mcpStatusFor(serverName) {
      const key = (serverName || '').trim().toLowerCase()
      return this.mcpStatuses.find(status => String(status.name || '').trim().toLowerCase() === key) || null
    },

    mcpStatusTone(serverName) {
      const status = this.mcpStatusFor(serverName)
      if (!status) {
        return 'badge-ghost'
      }
      if (status.state === 'running') {
        return 'badge-success'
      }
      if (status.state === 'suppressed') {
        return 'badge-error'
      }
      if (status.state === 'backoff') {
        return 'badge-warning'
      }
      if (status.state === 'starting') {
        return 'badge-info'
      }
      return 'badge-ghost'
    },

    mcpStatusLabel(serverName, serverEnabled) {
      if (!serverEnabled) {
        return 'configured off'
      }
      if (!this.mcpPluginEnabled) {
        return 'plugin off'
      }
      const status = this.mcpStatusFor(serverName)
      if (!status) {
        return 'not connected'
      }
      return status.state || 'unknown'
    },

    mcpTransportLabel(transport) {
      const labels = {
        stdio: 'stdio · local process',
        sse: 'sse · remote server',
        streamable_http: 'streamable HTTP · remote server',
        http: 'http · remote server',
      }
      return labels[transport] || transport
    },

    mcpTransportHelp(transport) {
      const help = {
        stdio: 'Launches a local process and talks MCP over stdin/stdout.',
        sse: 'Connects to an SSE endpoint that serves MCP.',
        streamable_http: 'Connects to a streamable HTTP MCP endpoint.',
        http: 'Connects using the HTTP MCP client transport.',
      }
      return help[transport] || ''
    },

    usesRemoteTransport(server) {
      return ['sse', 'streamable_http', 'http'].includes(server.transport)
    },

    formatTimestamp(value) {
      if (!value) {
        return ''
      }
      const date = new Date(value)
      if (Number.isNaN(date.getTime())) {
        return ''
      }
      return date.toLocaleString()
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
