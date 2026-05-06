import { api } from '/static/js/api.js'

/**
 * Registers the pluginsPage Alpine.data component.
 *
 * @param {import('alpinejs').Alpine} Alpine
 */
export function register(Alpine) {
  Alpine.data('pluginsPage', () => ({
    tab: 'tools',
    plugins: [],
    pluginSchemas: {},
    pluginConfigOpen: {},
    pluginConfigLoading: {},
    pluginConfigSaving: {},
    pluginConfigLoaded: {},
    pluginConfigRaw: {},
    pluginConfigDrafts: {},
    // Manifest plugins state
    manifestPlugins: [],
    manifestSyncing: false,
    manifestSyncResult: null,
    manifestInstallOpen: {},
    manifestInstallDrafts: {},
    manifestRowID: 0,
    showAddManifestTool: false,
    newManifestTool: {
      id: 'tool/',
      name: '',
      display_name: '',
      description: '',
      binary_name: '',
      repo: '',
      version: '',
      bin_path: '',
      exe: '',
    },

    mcpServers: [],
    mcpStatuses: [],
    mcpSaving: false,
    mcpSavedSignature: '{"servers":[]}',
    mcpLastSavedAt: '',
    mcpRowID: 0,

    get toolPlugins() {
      return this.semanticPlugins('tool').filter(p => p.id !== 'tool/mcp')
    },

    get channelPlugins() {
      return this.semanticPlugins('channel')
    },

    get hookPlugins() {
      return this.semanticPlugins('hook')
    },

    get memoryPlugins() {
      return this.plugins.filter(p => p.kind === 'memory')
    },

    get sandboxPlugins() {
      const validBackends = new Set(['sandbox/docker', 'sandbox/local', 'sandbox/none'])
      return this.plugins.filter(p => p.kind === 'sandbox' && validBackends.has(p.id))
    },

    get otherPlugins() {
      const known = new Set(['tool', 'channel', 'hook', 'memory', 'provider', 'sandbox'])
      const byID = new Map()
      for (const plugin of this.plugins) {
        if (!known.has(plugin.kind)) {
          byID.set(plugin.id, this.withManifestMeta(plugin))
        }
      }
      for (const manifest of this.manifestPlugins) {
        if (!known.has(manifest.kind) && !byID.has(manifest.id)) {
          byID.set(manifest.id, this.manifestOnlyPlugin(manifest))
        }
      }
      return this.sortPlugins(Array.from(byID.values()))
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
      await this.loadManifestPlugins()
      await this.syncManifest({ silent: true })
    },

    async loadPlugins() {
      try {
        this.plugins = (await api('GET', '/api/plugins') || []).map(plugin => ({
          ...plugin,
          capabilities: Array.isArray(plugin.capabilities) ? plugin.capabilities : [],
        }))
        await this.loadPluginSchemas()
        const mcpPlugin = this.plugins.find(p => p.id === 'tool/mcp')
        this.mcpServers = this.normalizeMcpServers(mcpPlugin?.config?.servers || [])
        this.mcpSavedSignature = this.snapshotMcpSignature()
        await this.loadMcpStatus()
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      }
    },

    async loadPluginSchemas() {
      const results = await Promise.all(
        this.plugins
          .filter(plugin => plugin.has_config)
          .map(async plugin => {
            try {
              return [plugin.id, await api('GET', this.pluginSchemaPath(plugin)) || {}]
            } catch {
              return [plugin.id, null]
            }
          }),
      )
      this.pluginSchemas = Object.fromEntries(results.filter(([, schema]) => !!schema))
    },

    cloneValue(value) {
      if (value === undefined) {
        return undefined
      }
      return JSON.parse(JSON.stringify(value))
    },

    manifestByID(id) {
      return this.manifestPlugins.find(plugin => plugin.id === id) || null
    },

    withManifestMeta(plugin) {
      const manifest = this.manifestByID(plugin.id)
      return {
        ...plugin,
        _manifest: !!manifest,
        _manifestPlugin: manifest,
      }
    },

    manifestOnlyPlugin(manifest) {
      return {
        id: manifest.id,
        kind: manifest.kind,
        name: manifest.name,
        display_name: manifest.display_name,
        description: manifest.description,
        enabled: manifest.enabled,
        config: {},
        capabilities: [],
        has_config: false,
        has_status: false,
        _manifest: true,
        _manifestPlugin: manifest,
      }
    },

    sortPlugins(plugins) {
      return plugins.sort((a, b) =>
        (this.pluginLabel(a) || '').localeCompare(this.pluginLabel(b) || '') ||
        String(a.id || '').localeCompare(String(b.id || '')),
      )
    },

    semanticPlugins(kind) {
      const byID = new Map()
      for (const plugin of this.plugins) {
        if (plugin.kind === kind) {
          byID.set(plugin.id, this.withManifestMeta(plugin))
        }
      }
      for (const manifest of this.manifestPlugins) {
        if (manifest.kind === kind && !byID.has(manifest.id)) {
          byID.set(manifest.id, this.manifestOnlyPlugin(manifest))
        }
      }
      return this.sortPlugins(Array.from(byID.values()))
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

    async toggleSandboxPlugin(id, enabled) {
      try {
        if (enabled) {
          // Disable all other sandbox backends first (mutual exclusion).
          const others = this.sandboxPlugins.filter(p => p.id !== id && p.enabled)
          for (const other of others) {
            await api('PATCH', '/api/plugins/' + encodeURIComponent(other.id), { enabled: false })
          }
        }
        await api('PATCH', '/api/plugins/' + encodeURIComponent(id), { enabled })
        await this.loadPlugins()
        this.$store.toast.show(enabled ? id + ' set as active sandbox' : id + ' disabled')
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

    pluginConfigPath(plugin) {
      return '/api/plugin-config/' + encodeURIComponent(plugin.kind) + '/' + encodeURIComponent(plugin.name)
    },

    hasGenericConfigEditor(plugin) {
      return !!plugin && plugin.id !== 'tool/mcp' && plugin.has_config && this.pluginSchemaFields(plugin).length > 0
    },

    isPluginConfigOpen(plugin) {
      return !!this.pluginConfigOpen[plugin.id]
    },

    pluginConfigIsLoading(plugin) {
      return !!this.pluginConfigLoading[plugin.id]
    },

    pluginConfigIsSaving(plugin) {
      return !!this.pluginConfigSaving[plugin.id]
    },

    async togglePluginConfigEditor(plugin) {
      const next = !this.isPluginConfigOpen(plugin)
      this.pluginConfigOpen[plugin.id] = next
      if (next) {
        await this.loadPluginConfig(plugin)
      }
    },

    async loadPluginConfig(plugin, force = false) {
      if (!force && this.pluginConfigLoaded[plugin.id]) {
        return
      }
      this.pluginConfigLoading[plugin.id] = true
      try {
        const config = this.cloneValue(await api('GET', this.pluginConfigPath(plugin)) || {})
        this.pluginConfigRaw[plugin.id] = config
        this.pluginConfigDrafts[plugin.id] = this.buildPluginConfigDraft(plugin, config)
        this.pluginConfigLoaded[plugin.id] = true
      } catch (e) {
        this.pluginConfigOpen[plugin.id] = false
        this.$store.toast.show(e.message, 'error')
      } finally {
        this.pluginConfigLoading[plugin.id] = false
      }
    },

    resetPluginConfigDraft(plugin) {
      this.pluginConfigDrafts[plugin.id] = this.buildPluginConfigDraft(plugin, this.pluginConfigRaw[plugin.id] || {})
    },

    async savePluginConfig(plugin) {
      try {
        this.pluginConfigSaving[plugin.id] = true
        const config = this.buildPluginConfigPayload(plugin)
        await api('PUT', this.pluginConfigPath(plugin), { config })
        this.pluginConfigRaw[plugin.id] = this.cloneValue(config)
        this.pluginConfigDrafts[plugin.id] = this.buildPluginConfigDraft(plugin, config)
        this.pluginConfigLoaded[plugin.id] = true
        await this.loadPlugins()
        this.$store.toast.show(plugin.id + ' config saved')
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      } finally {
        this.pluginConfigSaving[plugin.id] = false
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

    pluginSchemaPath(plugin) {
      return '/api/plugin-config-schema/' + encodeURIComponent(plugin.kind) + '/' + encodeURIComponent(plugin.name)
    },

    pluginSchemaFields(plugin) {
      const properties = this.pluginSchemas[plugin.id]?.properties || {}
      return Object.entries(properties).map(([name, schema]) => ({
        name,
        schema: schema || {},
      }))
    },

    pluginFieldID(plugin, field) {
      return plugin.id.replaceAll('/', '-') + '-' + field.name
    },

    pluginFieldType(schema) {
      if (Array.isArray(schema?.type)) {
        return schema.type.find(type => type !== 'null') || schema.type[0] || 'string'
      }
      return schema?.type || 'string'
    },

    pluginFieldHasEnum(field) {
      return Array.isArray(field.schema?.enum) && field.schema.enum.length > 0
    },

    pluginFieldIsComplex(field) {
      const type = this.pluginFieldType(field.schema)
      return type === 'object' || type === 'array'
    },

    pluginFieldIsSecret(field) {
      return /(token|secret|password|api[_-]?key|encrypt[_-]?key)$/i.test(field.name)
    },

    pluginFieldInputType(field) {
      const type = this.pluginFieldType(field.schema)
      if (type === 'integer' || type === 'number') {
        return 'number'
      }
      if (this.pluginFieldIsSecret(field)) {
        return 'password'
      }
      return 'text'
    },

    pluginFieldDescription(field) {
      return field.schema?.description || ''
    },

    pluginFieldPlaceholder(field) {
      if (field.schema?.default === undefined || field.schema?.default === null) {
        return ''
      }
      if (typeof field.schema.default === 'object') {
        return ''
      }
      return String(field.schema.default)
    },

    pluginFieldRows(field) {
      return this.pluginFieldType(field.schema) === 'object' ? 8 : 6
    },

    pluginFieldOptionLabel(option) {
      if (option === '') {
        return '(empty)'
      }
      return String(option)
    },

    buildPluginConfigDraft(plugin, config) {
      const draft = {}
      for (const field of this.pluginSchemaFields(plugin)) {
        const type = this.pluginFieldType(field.schema)
        let value = config?.[field.name]
        if (value === undefined && field.schema?.default !== undefined) {
          value = this.cloneValue(field.schema.default)
        }
        if (type === 'object' || type === 'array') {
          draft[field.name] = value === undefined ? '' : JSON.stringify(value, null, 2)
          continue
        }
        if (type === 'boolean') {
          draft[field.name] = value === undefined ? false : !!value
          continue
        }
        draft[field.name] = value === undefined ? '' : value
      }
      return draft
    },

    buildPluginConfigPayload(plugin) {
      const next = this.cloneValue(this.pluginConfigRaw[plugin.id] || {})
      const draft = this.pluginConfigDrafts[plugin.id] || {}
      for (const field of this.pluginSchemaFields(plugin)) {
        const type = this.pluginFieldType(field.schema)
        const value = draft[field.name]
        if (type === 'object' || type === 'array') {
          const text = String(value || '').trim()
          if (!text) {
            delete next[field.name]
            continue
          }
          try {
            next[field.name] = JSON.parse(text)
          } catch {
            throw new Error(field.name + ' must be valid JSON')
          }
          continue
        }
        if (type === 'boolean') {
          next[field.name] = !!value
          continue
        }
        if (type === 'integer' || type === 'number') {
          if (value === '' || value === null || value === undefined) {
            delete next[field.name]
            continue
          }
          const parsed = Number(value)
          if (Number.isNaN(parsed)) {
            throw new Error(field.name + ' must be a number')
          }
          next[field.name] = type === 'integer' ? Math.trunc(parsed) : parsed
          continue
        }
        const text = value === undefined || value === null ? '' : String(value)
        if (text === '' && !field.schema?.enum?.includes('')) {
          delete next[field.name]
          continue
        }
        next[field.name] = text
      }
      return next
    },

    pluginLabel(plugin) {
      return plugin.display_name || plugin.name || plugin.id
    },

    pluginDescription(plugin) {
      return plugin.description || ''
    },

    manifestInstallSummary(plugin) {
      const manifest = plugin._manifestPlugin
      if (!manifest) {
        return ''
      }
      const binaries = (manifest.binaries || []).map(binary => binary.name).filter(Boolean)
      if (binaries.length === 0) {
        return 'No binaries declared'
      }
      return 'Binaries: ' + binaries.join(', ')
    },

    sandboxMeta(plugin) {
      const meta = {
        'sandbox/docker': {
          recommended: true,
          isDefault: false,
          features: [
            'Full container-level process, filesystem, and network isolation',
            'Works on Linux, macOS, and Windows',
            'Per-agent network policy enforcement',
            'Dedicated container process namespace for MCP servers',
          ],
          limitations: [
            'Requires a running Docker daemon',
          ],
        },
        'sandbox/local': {
          recommended: false,
          isDefault: true,
          features: [
            'No Docker daemon required',
            'Linux: process group kill, rlimits, bwrap filesystem/network isolation',
            'Suitable for CI without Docker or embedded deployments',
          ],
          limitations: [
            'No container-level isolation',
            'macOS: no filesystem or network policy enforcement',
            'Windows: not supported',
            'Linux: bwrap is required; sessions fail closed if unavailable',
          ],
        },
        'sandbox/none': {
          recommended: false,
          isDefault: false,
          features: [
            'No external dependencies — works everywhere',
            'Agent inherits full host environment and permissions',
            'Suitable for trusted workloads or single-user local deployments',
          ],
          limitations: [
            'No isolation of any kind — agent runs as the current user',
            'No filesystem, network, or process restrictions enforced',
            'Not safe for untrusted agents or multi-user environments',
          ],
        },
      }
      return meta[plugin.id] || { recommended: false, isDefault: false, features: [], limitations: [] }
    },

    async loadManifestPlugins() {
      try {
        this.manifestPlugins = (await api('GET', '/api/manifest-plugins') || [])
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      }
    },

    registeredPluginFor(id) {
      return this.plugins.find(plugin => plugin.id === id) || null
    },

    async saveManifestPlugins() {
      await api('PUT', '/api/manifest-plugins', { plugins: this.manifestPlugins })
      await this.loadManifestPlugins()
      await this.loadPlugins()
      await this.syncManifest({ silent: true })
    },

    async toggleSemanticPlugin(plugin, enabled) {
      if (plugin._manifest) {
        await this.toggleManifestPlugin(plugin.id, enabled)
        return
      }
      await this.togglePlugin(plugin.id, enabled)
    },

    async toggleManifestPlugin(id, enabled) {
      try {
        const plugin = this.manifestPlugins.find(p => p.id === id)
        if (!plugin) return
        plugin.enabled = enabled
        await this.saveManifestPlugins()
        this.$store.toast.show(id + (enabled ? ' enabled' : ' disabled'))
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      }
    },

    isManifestInstallOpen(plugin) {
      return !!this.manifestInstallOpen[plugin.id]
    },

    toggleManifestInstallEditor(plugin) {
      const next = !this.isManifestInstallOpen(plugin)
      this.manifestInstallOpen[plugin.id] = next
      if (next && !this.manifestInstallDrafts[plugin.id]) {
        this.resetManifestInstallDraft(plugin)
      }
    },

    nextManifestRowID() {
      this.manifestRowID += 1
      return this.manifestRowID
    },

    normalizeManifestBinaries(binaries) {
      return (binaries || []).map(binary => ({
        id: this.nextManifestRowID(),
        name: binary.name || '',
        repo: binary.repo || '',
        version: binary.version || '',
        bin_path: binary.bin_path || '',
        exe: binary.exe || '',
      }))
    },

    normalizeManifestSessionEnv(sessionEnv) {
      return (sessionEnv || []).map(env => ({
        id: this.nextManifestRowID(),
        env_var: env.env_var || '',
        source: env.source || 'static',
        value: env.value || '',
        required: !!env.required,
      }))
    },

    resetManifestInstallDraft(plugin) {
      const manifest = plugin._manifestPlugin || this.manifestByID(plugin.id) || {}
      this.manifestInstallDrafts[plugin.id] = {
        id: manifest.id || plugin.id || '',
        kind: manifest.kind || plugin.kind || 'tool',
        name: manifest.name || plugin.name || '',
        display_name: manifest.display_name || plugin.display_name || '',
        description: manifest.description || plugin.description || '',
        enabled: manifest.enabled !== false,
        binaries: this.normalizeManifestBinaries(manifest.binaries || []),
        session_env: this.normalizeManifestSessionEnv(manifest.session_env || []),
        oauth_provider: manifest.oauth_provider || '',
        oauth_provider_config_field: manifest.oauth_provider_config_field || '',
        oauth_provider_choices: (manifest.oauth_provider_choices || []).join(', '),
      }
    },

    addManifestBinary(plugin) {
      this.manifestInstallDrafts[plugin.id].binaries.push({
        id: this.nextManifestRowID(),
        name: '',
        repo: '',
        version: '',
        bin_path: '',
        exe: '',
      })
    },

    removeManifestBinary(plugin, index) {
      this.manifestInstallDrafts[plugin.id].binaries.splice(index, 1)
    },

    addManifestSessionEnv(plugin) {
      this.manifestInstallDrafts[plugin.id].session_env.push({
        id: this.nextManifestRowID(),
        env_var: '',
        source: 'static',
        value: '',
        required: false,
      })
    },

    removeManifestSessionEnv(plugin, index) {
      this.manifestInstallDrafts[plugin.id].session_env.splice(index, 1)
    },

    buildManifestBinaries(rows) {
      return (rows || [])
        .map(row => {
          const binary = {
            name: String(row.name || '').trim(),
            repo: String(row.repo || '').trim(),
          }
          if (row.version) binary.version = String(row.version).trim()
          if (row.bin_path) binary.bin_path = String(row.bin_path).trim()
          if (row.exe) binary.exe = String(row.exe).trim()
          return binary
        })
        .filter(binary => binary.name || binary.repo)
    },

    buildManifestSessionEnv(rows) {
      return (rows || [])
        .map(row => {
          const env = {
            env_var: String(row.env_var || '').trim(),
            source: String(row.source || '').trim(),
          }
          if (row.value) env.value = String(row.value)
          if (row.required) env.required = true
          return env
        })
        .filter(env => env.env_var || env.source)
    },

    buildManifestPluginFromDraft(plugin) {
      const draft = this.manifestInstallDrafts[plugin.id]
      if (!draft) {
        throw new Error('manifest draft missing')
      }
      const binaries = this.buildManifestBinaries(draft.binaries)
      const sessionEnv = this.buildManifestSessionEnv(draft.session_env)
      const next = {
        id: (draft.id || '').trim(),
        kind: (draft.kind || '').trim(),
        name: (draft.name || '').trim(),
        display_name: (draft.display_name || '').trim(),
        description: (draft.description || '').trim(),
        enabled: !!draft.enabled,
        binaries,
        session_env: sessionEnv,
      }
      if (draft.oauth_provider) {
        next.oauth_provider = draft.oauth_provider.trim()
      }
      if (draft.oauth_provider_config_field) {
        next.oauth_provider_config_field = draft.oauth_provider_config_field.trim()
      }
      const choices = String(draft.oauth_provider_choices || '')
        .split(',')
        .map(value => value.trim())
        .filter(Boolean)
      if (choices.length > 0) {
        next.oauth_provider_choices = choices
      }
      return next
    },

    async saveManifestInstall(plugin) {
      try {
        const next = this.buildManifestPluginFromDraft(plugin)
        const index = this.manifestPlugins.findIndex(p => p.id === plugin.id)
        if (index >= 0) {
          this.manifestPlugins.splice(index, 1, next)
        } else {
          this.manifestPlugins.push(next)
        }
        if (next.id !== plugin.id) {
          delete this.manifestInstallOpen[plugin.id]
          delete this.manifestInstallDrafts[plugin.id]
        }
        await this.saveManifestPlugins()
        this.$store.toast.show(next.id + ' install saved')
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      }
    },

    resetNewManifestTool() {
      this.newManifestTool = {
        id: 'tool/',
        name: '',
        display_name: '',
        description: '',
        binary_name: '',
        repo: '',
        version: '',
        bin_path: '',
        exe: '',
      }
    },

    fillNewManifestToolDefaults() {
      const binary = (this.newManifestTool.binary_name || '').trim()
      if (!binary) return
      if (!this.newManifestTool.name) {
        this.newManifestTool.name = binary
      }
      if (this.newManifestTool.id === 'tool/' || !this.newManifestTool.id) {
        this.newManifestTool.id = 'tool/' + binary
      }
      if (!this.newManifestTool.display_name) {
        this.newManifestTool.display_name = binary
      }
    },

    async createManifestTool() {
      try {
        this.fillNewManifestToolDefaults()
        const draft = this.newManifestTool
        const id = (draft.id || '').trim()
        const name = (draft.name || '').trim()
        const binaryName = (draft.binary_name || '').trim()
        const repo = (draft.repo || '').trim()
        if (!id || !id.startsWith('tool/')) {
          throw new Error('Plugin ID must start with tool/')
        }
        if (!name) {
          throw new Error('Name is required')
        }
        if (!binaryName) {
          throw new Error('Binary name is required')
        }
        if (!repo) {
          throw new Error('GitHub repo is required')
        }
        if (this.manifestPlugins.some(plugin => plugin.id === id)) {
          throw new Error(id + ' already exists')
        }
        const binary = { name: binaryName, repo }
        if (draft.version) binary.version = draft.version.trim()
        if (draft.bin_path) binary.bin_path = draft.bin_path.trim()
        if (draft.exe) binary.exe = draft.exe.trim()
        this.manifestPlugins.push({
          id,
          kind: 'tool',
          name,
          display_name: (draft.display_name || '').trim() || name,
          description: (draft.description || '').trim(),
          enabled: true,
          binaries: [binary],
        })
        await this.saveManifestPlugins()
        this.showAddManifestTool = false
        this.resetNewManifestTool()
        this.$store.toast.show(id + ' added')
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      }
    },

    async syncManifest(options = {}) {
      try {
        this.manifestSyncing = true
        this.manifestSyncResult = await api('POST', '/api/manifest-plugins/sync')
        if (!options.silent) {
          this.$store.toast.show('Manifest sync complete')
        }
      } catch (e) {
        if (!options.silent) {
          this.$store.toast.show(e.message, 'error')
        }
      } finally {
        this.manifestSyncing = false
      }
    },

    pluginMetaBadges(plugin) {
      const badges = []
      if (plugin._manifest) {
        badges.push({ key: 'manifest', label: 'manifest', className: 'badge-primary' })
      }
      if (plugin.managed) {
        badges.push({ key: 'managed', label: 'managed', className: 'badge-primary' })
      }
      if (plugin.has_config) {
        badges.push({ key: 'config', label: 'config', className: 'badge-neutral' })
      }
      if (plugin.has_status) {
        badges.push({ key: 'status', label: 'status', className: 'badge-neutral' })
      }
      if (plugin.supports_notifications) {
        badges.push({ key: 'notifications', label: 'notifications', className: 'badge-info' })
      }
      const hiddenCapabilities = new Set([plugin.kind, 'config', 'status'])
      for (const capability of plugin.capabilities || []) {
        if (hiddenCapabilities.has(capability)) {
          continue
        }
        badges.push({ key: 'capability:' + capability, label: capability, className: 'badge-ghost' })
      }
      return badges
    },

    pluginSchemaSummary(plugin) {
      const schema = this.pluginSchemas[plugin.id]
      const properties = Object.keys(schema?.properties || {})
      if (properties.length === 0) {
        return ''
      }
      const preview = properties.slice(0, 3).join(', ')
      if (properties.length <= 3) {
        return 'Config fields: ' + preview
      }
      return 'Config fields: ' + preview + ', +' + (properties.length - 3) + ' more'
    },
  }))
}
