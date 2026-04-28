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
    userInput: '',
    isStreaming: false,
    abortController: null,
    newSessionAgentID: '',
    currentUserID: 0,
    agents: [],

    async init() {
      const agentsPromise = api('GET', '/api/agents').then(r => { this.agents = r || [] }).catch(() => {})
      const mePromise = api('GET', '/api/auth/me').then(r => { if (r && r.id) this.currentUserID = r.id }).catch(() => {})
      await Promise.all([this.loadSessions(), this.loadTools(), agentsPromise, mePromise])
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

    async createSession() {
      try {
        const sess = await api('POST', '/api/sessions', { agent_id: this.newSessionAgentID })
        this.sessions.unshift(sess)
        await this.openSession(sess.id)
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      }
    },

    async sendMessage() {
      if (!this.userInput.trim() || this.isStreaming) return
      const content = this.userInput.trim()
      this.userInput = ''
      this.sessionMessages.push({ role: 'user', content, timestamp: new Date().toISOString() })
      this.isStreaming = true
      this.abortController = new AbortController()
      try {
        const response = await fetch(
          '/api/sessions/' + encodeURIComponent(this.sessionDetail.id) + '/messages',
          {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ content }),
            signal: this.abortController.signal,
          }
        )
        if (!response.ok) {
          const text = await response.text()
          throw new Error(text || response.statusText)
        }
        const reader = response.body.getReader()
        const decoder = new TextDecoder()
        let buffer = ''
        let currentEvent = ''
        let currentData = ''
        // Reference to the current streaming assistant message's text block
        let streamingBlock = null

        const ensureStreamingMessage = () => {
          const last = this.sessionMessages[this.sessionMessages.length - 1]
          if (!last || last.role !== 'assistant' || !last._streaming) {
            streamingBlock = { type: 'text', text: '' }
            this.sessionMessages.push({
              role: 'assistant',
              blocks: [streamingBlock],
              timestamp: new Date().toISOString(),
              _streaming: true,
            })
          } else {
            // Use existing streaming block if last block is text
            const blocks = last.blocks
            const lastBlock = blocks[blocks.length - 1]
            if (lastBlock && lastBlock.type === 'text') {
              streamingBlock = lastBlock
            } else {
              // Create new text block
              streamingBlock = { type: 'text', text: '' }
              blocks.push(streamingBlock)
            }
          }
          return this.sessionMessages[this.sessionMessages.length - 1]
        }

        const dispatchEvent = (event, dataStr) => {
          if (!dataStr) return
          let data
          try {
            data = JSON.parse(dataStr)
          } catch {
            return
          }
          if (event === 'text') {
            const msg = ensureStreamingMessage()
            // Make sure streamingBlock points to a text block at end
            const blocks = msg.blocks
            const lastBlock = blocks[blocks.length - 1]
            if (!lastBlock || lastBlock.type !== 'text' || lastBlock !== streamingBlock) {
              streamingBlock = { type: 'text', text: '' }
              blocks.push(streamingBlock)
            }
            streamingBlock.text += (data.text || '')
            // Reassign to trigger Alpine reactivity on the array reference
            msg.blocks = [...msg.blocks.slice(0, -1), streamingBlock]
          } else if (event === 'tool_use') {
            if (data.type === 'tool_call') {
              const msg = ensureStreamingMessage()
              streamingBlock = null
              msg.blocks.push({
                type: 'tool_call',
                id: data.id,
                name: data.name,
                arguments: data.arguments,
                status: 'running',
              })
            } else if (data.type === 'tool_result') {
              // Find the assistant message with the matching tool_call block and set result
              for (let i = this.sessionMessages.length - 1; i >= 0; i--) {
                const msg = this.sessionMessages[i]
                if (msg.role !== 'assistant') continue
                for (const block of (msg.blocks || [])) {
                  if (block.type === 'tool_call' && block.id === data.tool_call_id) {
                    block.result = {
                      tool_call_id: data.tool_call_id,
                      content: data.content,
                      is_error: data.is_error,
                    }
                    block.status = 'done'
                    return
                  }
                }
              }
            }
          } else if (event === 'error') {
            this.$store.toast.show(data.error || 'Stream error', 'error')
          }
        }

        while (true) {
          const { done, value } = await reader.read()
          if (done) break
          buffer += decoder.decode(value, { stream: true })
          const lines = buffer.split('\n')
          buffer = lines.pop() // keep incomplete last line in buffer
          for (const line of lines) {
            if (line.startsWith('event: ')) {
              currentEvent = line.slice(7).trim()
            } else if (line.startsWith('data: ')) {
              currentData = line.slice(6).trim()
            } else if (line === '') {
              // blank line = dispatch
              if (currentEvent) {
                dispatchEvent(currentEvent, currentData)
              }
              currentEvent = ''
              currentData = ''
            }
          }
        }

        // Clear streaming flag from last assistant message
        const last = this.sessionMessages[this.sessionMessages.length - 1]
        if (last && last._streaming) {
          delete last._streaming
        }
      } catch (e) {
        if (e.name === 'AbortError') {
          // User aborted — clean up streaming flag
          const last = this.sessionMessages[this.sessionMessages.length - 1]
          if (last && last._streaming) delete last._streaming
        } else {
          this.$store.toast.show(e.message || 'Send failed', 'error')
        }
      } finally {
        this.isStreaming = false
        this.abortController = null
      }
    },

    abortMessage() {
      this.abortController?.abort()
    },
  }))
}
