import { api } from '/static/js/api.js'

let providerDefaults = {}

function createCustomModelForm() {
  return {
    original_id: '',
    id: '',
    name: '',
    enabled: true,
    reasoning: false,
    input: 'text',
    output: 'text',
    context_window: '',
    max_tokens: '',
    cost_input: '',
    cost_output: '',
    cost_cache_read: '',
    cost_cache_write: '',
  }
}

function normalizeModalities(value) {
  if (!value) return []
  return String(value)
    .split(',')
    .map(item => item.trim())
    .filter(Boolean)
}

function formFromModelConfig(modelID, config) {
  const form = createCustomModelForm()
  form.original_id = modelID
  form.id = modelID
  form.name = config?.name || ''
  form.enabled = config?.enabled !== false
  form.reasoning = Boolean(config?.reasoning)
  form.input = (config?.input || []).join(', ')
  form.output = (config?.output || []).join(', ')
  form.context_window = config?.contextWindow ?? ''
  form.max_tokens = config?.maxTokens ?? ''
  form.cost_input = config?.cost?.input ?? ''
  form.cost_output = config?.cost?.output ?? ''
  form.cost_cache_read = config?.cost?.cacheRead ?? ''
  form.cost_cache_write = config?.cost?.cacheWrite ?? ''
  return form
}

function numberOrZero(value) {
  const parsed = Number(value)
  return Number.isFinite(parsed) ? parsed : 0
}

function modelConfigFromForm(form) {
  const model = {
    id: form.id,
    name: form.name || form.id,
    enabled: form.enabled !== false,
    reasoning: Boolean(form.reasoning),
    input: normalizeModalities(form.input),
    output: normalizeModalities(form.output),
  }

  const contextWindow = numberOrZero(form.context_window)
  const maxTokens = numberOrZero(form.max_tokens)
  if (contextWindow > 0) {
    model.contextWindow = contextWindow
  }
  if (maxTokens > 0) {
    model.maxTokens = maxTokens
  }

  const cost = {
    input: numberOrZero(form.cost_input),
    output: numberOrZero(form.cost_output),
    cacheRead: numberOrZero(form.cost_cache_read),
    cacheWrite: numberOrZero(form.cost_cache_write),
  }
  if (cost.input !== 0 || cost.output !== 0 || cost.cacheRead !== 0 || cost.cacheWrite !== 0) {
    model.cost = cost
  }

  return model
}

function providerJSONValue(provider) {
  return {
    type: provider.type,
    name: provider.name,
    enabled: provider.enabled,
    api_key: provider.api_key,
    base_url: provider.base_url,
    models: provider.models || {},
  }
}

function normalizeProvider(provider) {
  const models = provider.models || {}
  const normalized = {
    ...provider,
    type: provider.type || provider.id,
    enabled: provider.enabled !== false,
    models,
    _fetching: false,
    _modelCount: 0,
    _showModels: false,
    _collapsed: false,
    _showAdvancedJSON: false,
    _providerJSON: '',
    _customModelForm: createCustomModelForm(),
    _showCustomModelForm: false,
  }
  normalized._providerJSON = JSON.stringify(providerJSONValue(normalized), null, 2)
  return normalized
}

/**
 * Registers the providersPage Alpine.data component.
 *
 * @param {import('alpinejs').Alpine} Alpine
 */
function groupProvidersByType(providers) {
  const groups = []
  const byType = new Map()

  for (const provider of providers) {
    if (!byType.has(provider.type)) {
      const group = { type: provider.type, providers: [] }
      byType.set(provider.type, group)
      groups.push(group)
    }
    byType.get(provider.type).providers.push(provider)
  }

  for (const group of groups) {
    group.providers.sort((a, b) => (a.name || a.id).localeCompare(b.name || b.id) || a.id.localeCompare(b.id))
  }

  groups.sort((a, b) => a.type.localeCompare(b.type))
  return groups
}

export function register(Alpine) {
  Alpine.data('providersPage', () => ({
    providers: [],
    providerModels: {},
    providerTypes: [],
    newProviderType: '',
    newProviderID: '',
    newProviderName: '',

    confirmMsg: '',
    confirmAction: () => {},

    get providerDefaults() {
      return providerDefaults
    },

    get addableProviders() {
      return this.providerTypes.map(type => type.id)
    },

    get groupedProviders() {
      return groupProvidersByType(this.providers)
    },

    async init() {
      await this.loadProviderTypes()
      await this.loadProviders()
      this.newProviderType = this.providerTypes[0]?.id || ''
    },

    async loadProviderTypes() {
      try {
        const types = await api('GET', '/api/provider-types') || []
        providerDefaults = {}
        for (const t of types) {
          providerDefaults[t.id] = { base_url: t.default_url, name: t.name }
        }
        this.providerTypes = [...types]
          .sort((a, b) => (a.name || a.id).localeCompare(b.name || b.id))
          .map(type => ({
            id: type.id,
            label: type.name || type.id,
          }))
      } catch (e) {
        console.error('Failed to load provider types:', e)
      }
    },

    async loadProviders() {
      try {
        const list = await api('GET', '/api/providers') || []
        this.providers = list.map(normalizeProvider)
        await Promise.all(this.providers.map(p => this.loadProviderModels(p.id)))
      } catch (e) {
        console.error(e)
      }
    },

    providerByID(providerID) {
      return this.providers.find(p => p.id === providerID)
    },

    syncProviderJSON(p) {
      p._providerJSON = JSON.stringify(providerJSONValue(p), null, 2)
    },

    setMergedProviderModels(providerID, models) {
      const nextModels = models || []
      this.providerModels[providerID] = nextModels
      const provider = this.providerByID(providerID)
      if (provider) {
        provider._modelCount = nextModels.length
      }
    },

    async loadProviderModels(providerID) {
      if (!this.providerByID(providerID)) return
      try {
        const models = await api('GET', '/api/providers/' + providerID + '/models') || []
        this.setMergedProviderModels(providerID, models)
      } catch (e) {
        console.error('Failed to load provider models:', providerID, e)
        this.setMergedProviderModels(providerID, [])
      }
    },

    async addProvider() {
      if (!this.newProviderType) return
      const d = providerDefaults[this.newProviderType] || {}
      const providerID = (this.newProviderID || '').trim()
      if (!providerID) {
        this.$store.toast.show('Provider ID is required', 'error')
        return
      }
      if (this.providerByID(providerID)) {
        this.$store.toast.show('Provider ID already exists', 'error')
        return
      }
      try {
        await api('POST', '/api/providers', {
          id: providerID,
          type: this.newProviderType,
          name: (this.newProviderName || '').trim() || d.name || providerID,
          enabled: true,
          api_key: '',
          base_url: '',
          models: {},
        })
        await this.loadProviders()
        this.newProviderType = this.providerTypes[0]?.id || ''
        this.newProviderID = ''
        this.newProviderName = ''
        this.$store.toast.show('Provider added')
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      }
    },

    parseCustomModels(p) {
      const raw = (p._customModelsJSON || '').trim()
      if (!raw) {
        return {}
      }
      let parsed
      try {
        parsed = JSON.parse(raw)
      } catch (e) {
        throw new Error('Custom models JSON is invalid: ' + e.message)
      }
      if (!parsed || Array.isArray(parsed) || typeof parsed !== 'object') {
        throw new Error('Custom models JSON must be an object keyed by model id')
      }
      return parsed
    },

    openCustomModelForm(p) {
      p._customModelForm = createCustomModelForm()
      p._showCustomModelForm = true
      p._showModels = true
    },

    editCustomModel(p, model) {
      const config = (p.models || {})[model.id] || {}
      p._customModelForm = formFromModelConfig(model.id, config)
      p._showCustomModelForm = true
      p._showModels = true
    },

    cancelCustomModelForm(p) {
      p._customModelForm = createCustomModelForm()
      p._showCustomModelForm = false
    },

    submitCustomModel(p) {
      try {
        const form = p._customModelForm || createCustomModelForm()
        const modelID = String(form.id || '').trim()
        if (!modelID) {
          throw new Error('Model ID is required')
        }

        const nextModels = { ...(p.models || {}) }
        if (form.original_id && form.original_id !== modelID) {
          delete nextModels[form.original_id]
        }
        nextModels[modelID] = modelConfigFromForm({ ...form, id: modelID })
        p.models = nextModels
        this.syncProviderJSON(p)
        p._customModelForm = formFromModelConfig(modelID, nextModels[modelID])
        p._showCustomModelForm = false
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      }
    },

    applyAdvancedJSON(p) {
      try {
        const parsed = this.parseProviderJSON(p)
        p.type = parsed.type
        p.name = parsed.name
        p.enabled = parsed.enabled
        p.api_key = parsed.api_key
        p.base_url = parsed.base_url
        p.models = parsed.models
        p._customModelForm = createCustomModelForm()
        p._showCustomModelForm = false
        this.syncProviderJSON(p)
        this.$store.toast.show('Provider JSON applied')
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      }
    },

    toggleModelEnabled(p, model) {
      const nextModels = { ...(p.models || {}) }
      const current = nextModels[model.id] || { id: model.id, name: model.name || model.id }
      current.enabled = !model.enabled
      nextModels[model.id] = current
      p.models = nextModels
      model.enabled = current.enabled
      this.syncProviderJSON(p)
    },

    removeCustomModel(p, modelID) {
      const next = { ...(p.models || {}) }
      delete next[modelID]
      p.models = next
      this.syncProviderJSON(p)
      const current = this.providerModels[p.id] || []
      this.setMergedProviderModels(p.id, current.filter(model => !(model.id === modelID && model.source === 'custom')))
      if (p._customModelForm?.original_id === modelID || p._customModelForm?.id === modelID) {
        this.cancelCustomModelForm(p)
      }
    },

    providerTypeLabel(type) {
      return providerDefaults[type]?.name || type
    },

    parseProviderJSON(p) {
      const raw = (p._providerJSON || '').trim()
      if (!raw) {
        throw new Error('Provider JSON is required')
      }
      let parsed
      try {
        parsed = JSON.parse(raw)
      } catch (e) {
        throw new Error('Provider JSON is invalid: ' + e.message)
      }
      if (!parsed || Array.isArray(parsed) || typeof parsed !== 'object') {
        throw new Error('Provider JSON must be an object')
      }
      return {
        type: String(parsed.type || p.type || '').trim(),
        name: String(parsed.name || p.name || p.id).trim() || p.id,
        enabled: parsed.enabled !== false,
        api_key: String(parsed.api_key || ''),
        base_url: String(parsed.base_url || ''),
        models: parsed.models && !Array.isArray(parsed.models) && typeof parsed.models === 'object' ? parsed.models : {},
      }
    },

    async saveProvider(p) {
      try {
        const parsed = this.parseProviderJSON(p)
        p.type = parsed.type
        p.name = parsed.name
        p.enabled = parsed.enabled
        p.api_key = parsed.api_key
        p.base_url = parsed.base_url
        p.models = parsed.models
        await api('PUT', '/api/providers/' + p.id, {
          id: p.id,
          type: p.type,
          name: p.name,
          enabled: p.enabled,
          api_key: p.api_key,
          base_url: p.base_url,
          models: p.models,
        })
        await this.loadProviders()
        this.$store.toast.show('Saved')
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      }
    },

    async doDeleteProvider(id) {
      try {
        await api('DELETE', '/api/providers/' + id)
        await this.loadProviders()
        this.newProviderType = this.addableProviders[0] || ''
        this.$store.toast.show('Deleted')
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      }
    },

    async fetchModels(p) {
      p._fetching = true
      try {
        const models = await api('POST', '/api/providers/' + p.id + '/models', {
          api_key: p.api_key,
          base_url: p.base_url,
        })
        this.setMergedProviderModels(p.id, models)
        this.$store.toast.show(p._modelCount + ' models available')
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      } finally {
        p._fetching = false
      }
    },

    confirmDelete(msg, action) {
      this.confirmMsg = msg
      this.confirmAction = action
    },
  }))
}
