function adminApp() {
  const providerDefaults = {
    'anthropic': { base_url: 'https://api.anthropic.com', name: 'Anthropic' },
    'openai': { base_url: 'https://api.openai.com/v1', name: 'OpenAI' },
    'openai-response': { base_url: 'https://api.openai.com/v1', name: 'OpenAI Response' },
  };

  return {
    tab: 'providers',
    tabs: [
      { id: 'providers', label: 'Providers' },
      { id: 'agents', label: 'Agents' },
      { id: 'channels', label: 'Channels' },
      { id: 'users', label: 'Users' },
      { id: 'sessions', label: 'Sessions' },
      { id: 'scheduler', label: 'Scheduler' },
      { id: 'settings', label: 'Settings' },
    ],
    status: '',
    toast: '',
    toastType: 'success',
    confirmMsg: '',
    confirmAction: () => {},
    providerDefaults,

    providers: [],
    providerModels: {},
    newProviderType: '',
    agents: [],
    users: [],
    userMemories: {},
    sessions: [],
    sessionDetail: null,
    sessionMessages: [],
    sessionMessagesLoading: false,
    sessionSystemPrompt: '',
    tools: [],
    toolsLoading: false,
    showTools: false,
    jobs: [],
    channelData: {
      telegram: { enabled: false, enable_notify: false, token: '', notify_chat: '', channel_id: '', group_mode: '', allowed_ids: [] },
      qq: { enabled: false, enable_notify: false, app_id: '', app_secret: '', group_mode: '', allowed_ids: [] },
      feishu: { enabled: false, enable_notify: false, app_id: '', app_secret: '', encrypt_key: '', verification_token: '', notify_chat: '', group_mode: '', allowed_ids: [] },
    },
    settingsKeys: ['runner', 'compaction', 'heartbeat', 'scheduler', 'plugins'],
    settingsEditors: {},

    showAgentForm: false,
    editingAgentId: null,
    agentForm: { id: '', name: '', model: '', model_strong: '', model_fast: '', system_prompt: '', workspace: '', enabled: true },
    editingJobId: null,
    jobForm: { name: '', cron: '', every: '', message: '', session_mode: 'reuse', enabled: true, agent_id: '', schedule_type: 'cron' },

    get addableProviders() {
      const existing = new Set(this.providers.map(p => p.id));
      return Object.keys(providerDefaults).filter(p => !existing.has(p));
    },

    allProviderModels() {
      const all = [];
      const seen = new Set();
      for (const p of this.providers) {
        for (const m of (this.providerModels[p.id] || [])) {
          const ref = p.id + '/' + m;
          if (!seen.has(ref)) {
            seen.add(ref);
            all.push(ref);
          }
        }
      }
      return all;
    },

    filteredProviderModels(search) {
      const all = this.allProviderModels();
      if (!search) return all;
      const q = search.toLowerCase();
      return all.filter(m => m.toLowerCase().includes(q));
    },

    getModelValue(field) {
      return this.agentForm[field];
    },

    setModelValue(field, value) {
      this.agentForm[field] = value;
    },

    async init() {
      await Promise.all([
        this.loadProviders(),
        this.loadAgents(),
        this.loadUsers(),
        this.loadSessions(),
        this.loadJobs(),
        this.loadChannels(),
        this.loadSettings(),
        this.loadTools(),
      ]);
      this.newProviderType = this.addableProviders[0] || '';
      this.status = 'Connected';
    },

    showToast(msg, type = 'success') {
      this.toast = msg;
      this.toastType = type;
      setTimeout(() => this.toast = '', 3000);
    },

    confirmDelete(msg, action) {
      this.confirmMsg = msg;
      this.confirmAction = action;
    },

    async api(method, path, body = null) {
      const opts = { method, headers: { 'Content-Type': 'application/json' } };
      if (body) opts.body = JSON.stringify(body);
      const res = await fetch(path, opts);
      const json = await res.json();
      if (json.error) throw new Error(json.error);
      return json.data;
    },

    // --- Providers ---

    async loadProviders() {
      try {
        const list = await this.api('GET', '/api/providers') || [];
        this.providers = list.map(p => ({ ...p, _fetching: false, _modelCount: 0, _showModels: false }));
      } catch (e) { console.error(e); }
    },

    async addProvider() {
      if (!this.newProviderType) return;
      const d = providerDefaults[this.newProviderType] || {};
      try {
        await this.api('POST', '/api/providers', {
          id: this.newProviderType, name: d.name || this.newProviderType, api_key: '', base_url: '',
        });
        await this.loadProviders();
        this.newProviderType = this.addableProviders[0] || '';
        this.showToast('Provider added');
      } catch (e) { this.showToast(e.message, 'error'); }
    },

    async saveProvider(p) {
      try {
        await this.api('PUT', '/api/providers/' + p.id, {
          id: p.id, name: p.name, api_key: p.api_key, base_url: p.base_url,
        });
        await this.loadProviders();
        this.showToast('Saved');
      } catch (e) { this.showToast(e.message, 'error'); }
    },

    async doDeleteProvider(id) {
      try {
        await this.api('DELETE', '/api/providers/' + id);
        await this.loadProviders();
        this.showToast('Deleted');
      } catch (e) { this.showToast(e.message, 'error'); }
    },

    async fetchModels(p) {
      p._fetching = true;
      try {
        const models = await this.api('POST', '/api/providers/' + p.id + '/models', {
          api_key: p.api_key, base_url: p.base_url,
        });
        this.providerModels[p.id] = models || [];
        p._modelCount = this.providerModels[p.id].length;
        this.showToast(p._modelCount + ' models fetched');
      } catch (e) { this.showToast(e.message, 'error'); }
      finally { p._fetching = false; }
    },

    // --- Agents ---

    async loadAgents() {
      try { this.agents = await this.api('GET', '/api/agents') || []; }
      catch (e) { console.error(e); }
    },

    resetAgentForm() {
      this.agentForm = { id: '', name: '', model: '', model_strong: '', model_fast: '', system_prompt: '', workspace: '', enabled: true };
      this.editingAgentId = null;
      this.showAgentForm = false;
    },

    editAgent(a) {
      this.agentForm = { ...a };
      this.editingAgentId = a.id;
      this.showAgentForm = true;
    },

    async saveAgent() {
      try {
        if (this.editingAgentId) {
          await this.api('PUT', '/api/agents/' + this.editingAgentId, this.agentForm);
        } else {
          await this.api('POST', '/api/agents', this.agentForm);
        }
        this.resetAgentForm();
        await this.loadAgents();
        this.showToast('Saved');
      } catch (e) { this.showToast(e.message, 'error'); }
    },

    async doDeleteAgent(id) {
      try {
        await this.api('DELETE', '/api/agents/' + id);
        await this.loadAgents();
        this.showToast('Deleted');
      } catch (e) { this.showToast(e.message, 'error'); }
    },

    // --- Channels ---

    async loadChannels() {
      try {
        const channels = await this.api('GET', '/api/channels') || [];
        for (const ch of channels) {
          let cfg = {};
          try { cfg = JSON.parse(ch.config || '{}'); } catch (_) {}
          if (ch.id === 'telegram') {
            this.channelData.telegram = {
              enabled: ch.enabled, enable_notify: cfg.enable_notify || false,
              token: cfg.token || '', notify_chat: cfg.notify_chat || '',
              channel_id: cfg.channel_id || '', group_mode: cfg.group_mode || '',
              allowed_ids: cfg.allowed_ids || [],
            };
          } else if (ch.id === 'qq') {
            this.channelData.qq = {
              enabled: ch.enabled, enable_notify: cfg.enable_notify || false,
              app_id: cfg.app_id || '', app_secret: cfg.app_secret || '',
              group_mode: cfg.group_mode || '', allowed_ids: cfg.allowed_ids || [],
            };
          } else if (ch.id === 'feishu') {
            this.channelData.feishu = {
              enabled: ch.enabled, enable_notify: cfg.enable_notify || false,
              app_id: cfg.app_id || '', app_secret: cfg.app_secret || '',
              encrypt_key: cfg.encrypt_key || '', verification_token: cfg.verification_token || '',
              notify_chat: cfg.notify_chat || '', group_mode: cfg.group_mode || '',
              allowed_ids: cfg.allowed_ids || [],
            };
          }
        }
      } catch (e) { console.error(e); }
    },

    async saveChannel(platform) {
      try {
        const data = this.channelData[platform];
        const { enabled, ...cfg } = data;
        await this.api('PUT', '/api/channels/' + platform, { enabled, config: JSON.stringify(cfg) });
        await this.loadChannels();
        this.showToast(platform + ' saved');
      } catch (e) { this.showToast(e.message, 'error'); }
    },

    // --- Users ---

    async loadUsers() {
      try {
        const list = await this.api('GET', '/api/users') || [];
        this.users = list.map(u => ({
          ...u, _defaultAgent: u.default_agent_id || '',
          _showMemory: false, _memoryCount: 0,
          _showAddMemory: false, _newMemoryAgent: '', _newMemoryContent: '',
        }));
      } catch (e) { console.error(e); }
    },

    async toggleUserMemory(u) {
      u._showMemory = !u._showMemory;
      if (u._showMemory) await this.loadUserMemories(u);
    },

    async loadUserMemories(u) {
      try {
        const mems = await this.api('GET', '/api/users/' + u.id + '/memories') || [];
        this.userMemories[u.id] = mems.map(m => ({ ...m, _content: m.content }));
        u._memoryCount = mems.length;
      } catch (e) { this.showToast(e.message, 'error'); }
    },

    async saveUserDefaultAgent(u) {
      try {
        await this.api('PUT', '/api/users/' + u.id, { default_agent_id: u._defaultAgent });
        u.default_agent_id = u._defaultAgent;
        this.showToast('Saved');
      } catch (e) { this.showToast(e.message, 'error'); }
    },

    async saveUserMemory(userId, agentId, content) {
      try {
        await this.api('PUT', '/api/users/' + userId + '/memories/' + agentId, { content });
        const u = this.users.find(u => u.id === userId);
        if (u) await this.loadUserMemories(u);
        this.showToast('Saved');
      } catch (e) { this.showToast(e.message, 'error'); }
    },

    async doDeleteUserMemory(userId, agentId) {
      try {
        await this.api('DELETE', '/api/users/' + userId + '/memories/' + agentId);
        const u = this.users.find(u => u.id === userId);
        if (u) await this.loadUserMemories(u);
        this.showToast('Deleted');
      } catch (e) { this.showToast(e.message, 'error'); }
    },

    async addUserMemory(u) {
      if (!u._newMemoryAgent || !u._newMemoryContent) return;
      try {
        await this.api('PUT', '/api/users/' + u.id + '/memories/' + u._newMemoryAgent, { content: u._newMemoryContent });
        u._showAddMemory = false;
        u._newMemoryAgent = '';
        u._newMemoryContent = '';
        await this.loadUserMemories(u);
        this.showToast('Added');
      } catch (e) { this.showToast(e.message, 'error'); }
    },

    // --- Sessions ---

    async loadSessions() {
      try { this.sessions = await this.api('GET', '/api/sessions') || []; }
      catch (e) { console.error(e); }
    },

    async openSession(sessionID) {
      this.sessionMessagesLoading = true;
      this.sessionMessages = [];
      this.sessionSystemPrompt = '';
      try {
        const enc = encodeURIComponent(sessionID);
        this.sessionDetail = await this.api('GET', '/api/sessions/' + enc);
        const [msgs, pr] = await Promise.all([
          this.api('GET', '/api/sessions/' + enc + '/messages'),
          this.api('GET', '/api/sessions/' + enc + '/system-prompt').catch(() => null),
        ]);
        this.sessionMessages = msgs || [];
        if (pr && pr.system_prompt) this.sessionSystemPrompt = pr.system_prompt;
      } catch (e) { this.showToast(e.message, 'error'); }
      finally { this.sessionMessagesLoading = false; }
    },

    formatTime(ts) {
      if (!ts) return '';
      try {
        let d = new Date(ts);
        if (isNaN(d.getTime()) && /^\d{4}-\d{2}-\d{2} \d{2}:\d{2}/.test(ts)) {
          d = new Date(ts.replace(' ', 'T') + 'Z');
        }
        if (isNaN(d.getTime())) return ts;
        const now = new Date();
        const ms = now - d;
        const min = Math.floor(ms / 60000);
        if (min < 1) return 'just now';
        if (min < 60) return min + 'm ago';
        const hr = Math.floor(min / 60);
        if (hr < 24) return hr + 'h ago';
        const day = Math.floor(hr / 24);
        if (day < 7) return day + 'd ago';
        return d.toLocaleDateString();
      } catch (_) { return ts; }
    },

    // --- Tools ---

    async loadTools() {
      this.toolsLoading = true;
      try { this.tools = await this.api('GET', '/api/tools') || []; }
      catch (e) { console.error(e); }
      finally { this.toolsLoading = false; }
    },

    // --- Scheduler ---

    async loadJobs() {
      try { this.jobs = await this.api('GET', '/api/scheduler/jobs') || []; }
      catch (e) { console.error(e); }
    },

    resetJobForm() {
      this.jobForm = { name: '', cron: '', every: '', message: '', session_mode: 'reuse', enabled: true, agent_id: '', schedule_type: 'cron' };
      this.editingJobId = null;
    },

    editJob(j) {
      this.editingJobId = j.id;
      this.jobForm = {
        name: j.name, message: j.message,
        schedule_type: j.cron ? 'cron' : 'every',
        cron: j.cron || '', every: j.every || '',
        session_mode: j.session_mode || 'reuse',
        enabled: j.enabled, agent_id: j.agent_id || '',
      };
    },

    async saveJob() {
      const p = {
        name: this.jobForm.name, message: this.jobForm.message,
        cron: this.jobForm.schedule_type === 'cron' ? this.jobForm.cron : '',
        every: this.jobForm.schedule_type === 'every' ? this.jobForm.every : '',
        session_mode: this.jobForm.session_mode,
        enabled: this.jobForm.enabled, agent_id: this.jobForm.agent_id,
      };
      try {
        if (this.editingJobId) {
          await this.api('PUT', '/api/scheduler/jobs/' + this.editingJobId, p);
        } else {
          await this.api('POST', '/api/scheduler/jobs', p);
        }
        this.resetJobForm();
        await this.loadJobs();
        this.showToast('Saved');
      } catch (e) { this.showToast(e.message, 'error'); }
    },

    async toggleJob(j) {
      try {
        await this.api('PUT', '/api/scheduler/jobs/' + j.id, {
          name: j.name, message: j.message,
          cron: j.cron || '', every: j.every || '',
          session_mode: j.session_mode,
          enabled: !j.enabled, agent_id: j.agent_id || '',
        });
        await this.loadJobs();
      } catch (e) { this.showToast(e.message, 'error'); }
    },

    async doDeleteJob(id) {
      try {
        await this.api('DELETE', '/api/scheduler/jobs/' + id);
        await this.loadJobs();
        this.showToast('Deleted');
      } catch (e) { this.showToast(e.message, 'error'); }
    },

    // --- Settings ---

    async loadSettings() {
      for (const key of this.settingsKeys) {
        try {
          const r = await this.api('GET', '/api/settings/' + key);
          this.settingsEditors[key] = typeof r.value === 'object' ? JSON.stringify(r.value, null, 2) : (r.value || '');
        } catch (e) { this.settingsEditors[key] = ''; }
      }
    },

    async saveSetting(key) {
      try {
        let val = this.settingsEditors[key];
        try { val = JSON.parse(val); } catch (_) {}
        await this.api('PUT', '/api/settings/' + key, { value: val });
        this.showToast(key + ' saved');
      } catch (e) { this.showToast(e.message, 'error'); }
    },
  };
}

function modelCombo(field) {
  return { open: false, search: '' };
}
