import { api } from '/static/js/api.js'

let providerDefaults = {}

function createCustomModelForm() {
  return {
    original_id: '',
    id: '',
    name: '',
    input_modalities: 'text',
    output_modalities: 'text',
    context_limit: '',
    output_limit: '',
    thinking_enabled: false,
    thinking_budget_tokens: '',
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
  form.input_modalities = (config?.modalities?.input || []).join(', ')
  form.output_modalities = (config?.modalities?.output || []).join(', ')
  form.context_limit = config?.limit?.context ?? ''
  form.output_limit = config?.limit?.output ?? ''
  form.thinking_enabled = config?.options?.thinking?.type === 'enabled'
  form.thinking_budget_tokens = config?.options?.thinking?.budgetTokens ?? ''
  return form
}

function modelConfigFromForm(form) {
  const model = {
    name: form.name || form.id,
  }

  const inputModalities = normalizeModalities(form.input_modalities)
  const outputModalities = normalizeModalities(form.output_modalities)
  if (inputModalities.length || outputModalities.length) {
    model.modalities = {}
    if (inputModalities.length) {
      model.modalities.input = inputModalities
    }
    if (outputModalities.length) {
      model.modalities.output = outputModalities
    }
  }

  const contextLimit = Number(form.context_limit)
  const outputLimit = Number(form.output_limit)
  if (contextLimit > 0 || outputLimit > 0) {
    model.limit = {}
    if (contextLimit > 0) {
      model.limit.context = contextLimit
    }
    if (outputLimit > 0) {
      model.limit.output = outputLimit
    }
  }

  if (form.thinking_enabled) {
    model.options = {
      thinking: {
        type: 'enabled',
      },
    }
    const budgetTokens = Number(form.thinking_budget_tokens)
    if (budgetTokens > 0) {
      model.options.thinking.budgetTokens = budgetTokens
    }
  }

  return model
}

function normalizeProvider(provider) {
  const models = provider.models || {}
  return {
    ...provider,
    type: provider.type || provider.id,
    enabled: provider.enabled !== false,
    models,
    disabled_models: Array.isArray(provider.disabled_models) ? [...provider.disabled_models] : [],
    _fetching: false,
    _modelCount: 0,
    _showModels: false,
    _showAdvancedJSON: false,
    _customModelsJSON: JSON.stringify(models, null, 2),
    _customModelForm: createCustomModelForm(),
    _showCustomModelForm: false,
  }
}

/**
 * Registers the providersPage Alpine.data component.
 *
 * @param {import('alpinejs').Alpine} Alpine
 */
export function register(Alpine) {
  Alpine.data('providersPage', () => ({
    providers: [],
    providerModels: {},
    newProviderType: '',
    newProviderID: '',
    newProviderName: '',

    confirmMsg: '',
    confirmAction: () => {},

    get providerDefaults() {
      return providerDefaults
    },

    get addableProviders() {
      return Object.keys(providerDefaults)
    },

    async init() {
      await this.loadProviderTypes()
      await this.loadProviders()
      this.newProviderType = this.addableProviders[0] || ''
    },

    async loadProviderTypes() {
      try {
        const types = await api('GET', '/api/provider-types') || []
        providerDefaults = {}
        for (const t of types) {
          providerDefaults[t.id] = { base_url: t.default_url, name: t.name }
        }
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

    syncProviderModelsJSON(p) {
      p._customModelsJSON = JSON.stringify(p.models || {}, null, 2)
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
      try {
        await api('POST', '/api/providers', {
          id: providerID,
          type: this.newProviderType,
          name: (this.newProviderName || '').trim() || d.name || providerID,
          enabled: true,
          api_key: '',
          base_url: '',
          models: {},
          disabled_models: [],
        })
        await this.loadProviders()
        this.newProviderType = this.addableProviders[0] || ''
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
        this.syncProviderModelsJSON(p)
        p._customModelForm = formFromModelConfig(modelID, nextModels[modelID])
        p._showCustomModelForm = false
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      }
    },

    applyAdvancedJSON(p) {
      try {
        p.models = this.parseCustomModels(p)
        p._customModelForm = createCustomModelForm()
        p._showCustomModelForm = false
        this.syncProviderModelsJSON(p)
        this.$store.toast.show('Custom models JSON applied')
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      }
    },

    toggleModelEnabled(p, model) {
      const disabled = new Set(p.disabled_models || [])
      if (model.enabled) {
        disabled.add(model.id)
        model.enabled = false
      } else {
        disabled.delete(model.id)
        model.enabled = true
      }
      p.disabled_models = [...disabled].sort()
    },

    removeCustomModel(p, modelID) {
      const next = { ...(p.models || {}) }
      delete next[modelID]
      p.models = next
      this.syncProviderModelsJSON(p)
      const current = this.providerModels[p.id] || []
      this.setMergedProviderModels(p.id, current.filter(model => !(model.id === modelID && model.source === 'custom')))
      if (p._customModelForm?.original_id === modelID || p._customModelForm?.id === modelID) {
        this.cancelCustomModelForm(p)
      }
    },

    async saveProvider(p) {
      try {
        p.models = this.parseCustomModels(p)
        await api('PUT', '/api/providers/' + p.id, {
          id: p.id,
          type: p.type,
          name: p.name,
          enabled: p.enabled,
          api_key: p.api_key,
          base_url: p.base_url,
          models: p.models,
          disabled_models: p.disabled_models,
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
