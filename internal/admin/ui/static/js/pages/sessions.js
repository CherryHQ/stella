import { api } from '/static/js/api.js'
import { formatTime } from '/static/js/utils.js'
import { marked } from 'https://esm.sh/marked@14'

marked.setOptions({ breaks: true, gfm: true })

/**
 * Registers the sessionsPage Alpine.data component.
 *
 * @param {import('alpinejs').Alpine} Alpine
 */
export function register(Alpine) {
  Alpine.data('sessionsPage', () => ({
    sessions: [],
    sessionDetail: null,
    sessionMessages: [],
    sessionMessagesLoading: false,
    sessionSystemPrompt: '',
    tools: [],
    toolsLoading: false,
    activePanel: null,
    searchQuery: '',
    showArchived: false,
    filterChannel: '',
    filterAgent: '',
    filterUser: '',

    async init() {
      await Promise.all([this.loadSessions(), this.loadTools()])
      const sessionID = this._sessionIDFromURL()
      if (sessionID) {
        await this.openSession(sessionID, false)
      }
    },

    // Merge tool results into their corresponding tool_call blocks so each
    // call+result pair collapses as a single unit. Also annotates each message
    // with showTimestamp (only on role-change boundaries) and sameRoleAsPrev.
    get processedMessages() {
      const resultsByID = {}
      for (const msg of this.sessionMessages) {
        if (msg.role === 'tool' && msg.tool_call_id) {
          resultsByID[msg.tool_call_id] = msg
        }
      }
      const msgs = this.sessionMessages
        .filter(msg => msg.role !== 'tool')
        .map(msg => {
          if (msg.role !== 'assistant') return msg
          return {
            ...msg,
            blocks: (msg.blocks || []).map(block => {
              if (block.type === 'tool_call' && block.id && resultsByID[block.id]) {
                return { ...block, result: resultsByID[block.id] }
              }
              return block
            }),
          }
        })
      return msgs.map((msg, i) => ({
        ...msg,
        showTimestamp: i === msgs.length - 1 || msgs[i + 1].role !== msg.role,
        sameRoleAsPrev: i > 0 && msgs[i - 1].role === msg.role,
      }))
    },

    get filteredSessions() {
      return this.sessions.filter(s => {
        if (!this.showArchived && s.archived) return false
        if (this.filterChannel && s.channel !== this.filterChannel) return false
        if (this.filterAgent && s.agent_id !== this.filterAgent) return false
        if (this.filterUser && String(s.user_id) !== this.filterUser) return false
        if (this.searchQuery.trim()) {
          const q = this.searchQuery.toLowerCase()
          return (s.title || '').toLowerCase().includes(q) ||
            (s.channel || '').toLowerCase().includes(q) ||
            (s.agent_id || '').toLowerCase().includes(q)
        }
        return true
      })
    },

    get uniqueChannels() {
      return [...new Set(this.sessions.map(s => s.channel).filter(Boolean))].sort()
    },

    // Extract human-readable channel name from full channel ID.
    // e.g. "anna:user:1:channel:coder-tg:private" → "coder-tg"
    //      "cli" → "cli"
    channelLabel(ch) {
      if (!ch) return ch
      const m = ch.match(/:channel:([^:]+)/)
      return m ? m[1] : ch
    },

    get uniqueAgents() {
      return [...new Set(this.sessions.map(s => s.agent_id).filter(Boolean))].sort()
    },

    get uniqueUsers() {
      return [...new Set(this.sessions.map(s => s.user_id).filter(v => v != null && v !== 0))].sort()
    },

    // Extract plain text from a user message's content field.
    // Content may be a plain string or a JSON-encoded array of content blocks.
    // Blocks use either "kind" or "type" as the discriminator field.
    userText(msg) {
      const raw = msg.content || ''
      try {
        const parsed = JSON.parse(raw)
        if (Array.isArray(parsed)) {
          return parsed
            .filter(b => b.kind === 'text' || b.type === 'text')
            .map(b => b.text || '')
            .join('\n')
        }
      } catch { /* plain string */ }
      return raw
    },

    // Initial letter for the user avatar, derived from the resolved user name.
    userInitial() {
      const name = this.sessionDetail?.user_name || ''
      return name ? name[0].toUpperCase() : 'U'
    },

    renderMd(text) {
      return marked.parse(text || '')
    },

    formatTime(ts) {
      return formatTime(ts)
    },

    _sessionIDFromURL() {
      const parts = window.location.pathname.split('/')
      return parts.length >= 3 && parts[1] === 'sessions' ? decodeURIComponent(parts[2]) : ''
    },

    async loadSessions() {
      try {
        this.sessions = await api('GET', '/api/sessions') || []
      } catch (e) {
        console.error(e)
      }
    },

    async openSession(sessionID, pushState = true) {
      this.sessionMessagesLoading = true
      this.sessionMessages = []
      this.sessionSystemPrompt = ''
      this.activePanel = null
      try {
        const enc = encodeURIComponent(sessionID)
        this.sessionDetail = await api('GET', '/api/sessions/' + enc)
        const [msgs, pr] = await Promise.all([
          api('GET', '/api/sessions/' + enc + '/messages'),
          api('GET', '/api/sessions/' + enc + '/system-prompt').catch(() => null),
        ])
        this.sessionMessages = msgs || []
        if (pr && pr.system_prompt) this.sessionSystemPrompt = pr.system_prompt
        if (pushState) {
          history.pushState({ sessionID }, '', '/sessions/' + enc)
        }
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      } finally {
        this.sessionMessagesLoading = false
      }
    },

    async loadTools() {
      this.toolsLoading = true
      try {
        this.tools = await api('GET', '/api/tools') || []
      } catch (e) {
        console.error(e)
      } finally {
        this.toolsLoading = false
      }
    },

    togglePanel(name) {
      this.activePanel = this.activePanel === name ? null : name
    },

    backToList() {
      this.sessionDetail = null
      this.sessionMessages = []
      this.sessionSystemPrompt = ''
      this.activePanel = null
      history.pushState(null, '', '/sessions')
    },

    async copyID() {
      if (!this.sessionDetail?.id) return
      try {
        await navigator.clipboard.writeText(this.sessionDetail.id)
        this.$store.toast.show('Session ID copied', 'success')
      } catch {
        this.$store.toast.show('Copy failed', 'error')
      }
    },
  }))
}
