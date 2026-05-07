import { api } from '/static/js/api.js'
import { formatTime } from '/static/js/utils.js'
import { marked } from 'https://esm.sh/marked@14'

let _treesModule = null
async function getTreesModule() {
  if (!_treesModule) {
    const [trees] = await Promise.all([
      import('https://esm.sh/@pierre/trees@1.0.0-beta.3'),
      import('https://esm.sh/@pierre/trees@1.0.0-beta.3/web-components'),
    ])
    _treesModule = trees
  }
  return _treesModule
}

marked.setOptions({ breaks: true, gfm: true })

/**
 * Registers the sessionsPage Alpine.data component.
 *
 * @param {import('alpinejs').Alpine} Alpine
 */
export function register(Alpine) {
  Alpine.data('sessionsPage', () => ({
    // Session list state
    sessions: [],
    sessionsOffset: 0,
    sessionsHasMore: false,
    sessionsLoading: false,

    // Session detail state
    sessionDetail: null,
    sessionMessages: [],
    sessionMessagesSkip: 0,
    sessionMessagesHasMore: false,
    sessionMessagesLoading: false,
    sessionSystemPrompt: '',
    sessionWorkspace: null,
    workspaceLoading: false,
    _fileTree: null,

    tools: [],
    toolsLoading: false,
    activePanel: null,
    searchQuery: '',
    showArchived: false,
    showScheduler: false,
    filterChannel: '',
    filterAgent: '',
    filterUser: '',
    userInput: '',
    isStreaming: false,
    abortController: null,
    newSessionAgentID: '',
    currentUserID: 0,
    agents: [],
    _mdCache: {},

    async init() {
      const agentsPromise = api('GET', '/api/agents').then(r => { this.agents = r || [] }).catch(() => {})
      const mePromise = api('GET', '/api/auth/me').then(r => { if (r && r.id) this.currentUserID = r.id }).catch(() => {})
      await Promise.all([this.loadSessions(), agentsPromise, mePromise])
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
        if (!this.showScheduler && s.id && (s.id.startsWith('scheduler:') || s.id.includes(':scheduler:'))) return false
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

    get sessionTotalTokens() {
      return this.sessionMessages.reduce((sum, m) => sum + (m.token_count || 0), 0)
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

    toolColor(name) {
      const n = (name || '').toLowerCase()
      if (n === 'bash') return 'text-warning'
      if (n === 'skill' || n === 'skills') return 'text-info'
      if (n === 'memory') return 'text-accent'
      if (n === 'agent') return 'text-primary'
      if (n === 'read' || n === 'write' || n === 'edit') return 'text-secondary'
      return 'text-primary'
    },

    toolPreview(block) {
      const args = block.arguments || {}
      const n = (block.name || '').toLowerCase()
      const trunc = (s, len = 55) => s.length > len ? s.slice(0, len) + '…' : s
      const shortPath = p => { const pts = (p || '').split('/'); return pts.length > 2 ? '…/' + pts.slice(-2).join('/') : p }
      if (n === 'bash') return trunc('$ ' + (args.command || args.input || ''))
      if (n === 'skill' || n === 'skills') {
        const parts = [args.action, args.skill || args.name, args.args || args.input].filter(Boolean)
        return trunc(parts.join(' › '))
      }
      if (n === 'read') return shortPath(args.path || args.file_path || args.input)
      if (n === 'write') {
        const lines = args.content ? args.content.split('\n').length : 0
        return shortPath(args.path || args.file_path || args.input) + (lines ? ' (' + lines + ' lines)' : '')
      }
      if (n === 'edit') return shortPath(args.path || args.file_path || args.input)
      if (n === 'memory') {
        const action = args.action || args.input || ''
        const detail = args.pattern || args.constraint_text || args.history_scope || ''
        return detail ? action + ': ' + trunc(detail, 40) : action
      }
      if (n === 'agent') {
        const tasks = args.tasks || []
        if (!tasks.length) return args.input || ''
        const prefix = tasks.length > 1 ? '[' + tasks.length + '] ' : ''
        return trunc(prefix + (tasks[0].task || ''))
      }
      return ''
    },

    renderMd(text) {
      if (!text) return ''
      if (!this._mdCache[text]) {
        this._mdCache[text] = marked.parse(text)
      }
      return this._mdCache[text]
    },

    formatTime(ts) {
      return formatTime(ts)
    },

    _sessionIDFromURL() {
      const parts = window.location.pathname.split('/')
      return parts.length >= 3 && parts[1] === 'sessions' ? decodeURIComponent(parts[2]) : ''
    },

    // --- Session list loading ---

    async loadSessions() {
      if (this.sessionsLoading) return
      this.sessionsLoading = true
      try {
        const batch = await api('GET', `/api/sessions?limit=10&offset=${this.sessionsOffset}`) || []
        this.sessions = this.sessionsOffset === 0 ? batch : [...this.sessions, ...batch]
        this.sessionsOffset += batch.length
        this.sessionsHasMore = batch.length === 10
      } catch (e) {
        console.error(e)
      } finally {
        this.sessionsLoading = false
      }
      // If the list container isn't scrollable yet (content doesn't overflow),
      // the scroll event can never fire — keep loading until it can scroll.
      if (this.sessionsHasMore) {
        this.$nextTick(() => {
          const el = this.$refs.sessionsList
          if (el && el.scrollHeight <= el.clientHeight + 20) {
            this.loadSessions()
          }
        })
      }
    },

    handleSessionsScroll(el) {
      if (this.sessionsHasMore && !this.sessionsLoading &&
          el.scrollHeight - el.scrollTop - el.clientHeight < 120) {
        this.loadSessions()
      }
    },

    // --- Session message loading ---

    async openSession(sessionID, pushState = true) {
      const prevPanel = this.activePanel
      this.sessionMessagesLoading = true
      this.sessionMessages = []
      this.sessionMessagesSkip = 0
      this.sessionMessagesHasMore = false
      this.sessionSystemPrompt = ''
      this.sessionWorkspace = null
      this._destroyFileTree()
      this.activePanel = null
      try {
        const enc = encodeURIComponent(sessionID)
        this.sessionDetail = await api('GET', '/api/sessions/' + enc)
        const [msgs, pr] = await Promise.all([
          api('GET', '/api/sessions/' + enc + '/messages?limit=20'),
          api('GET', '/api/sessions/' + enc + '/system-prompt').catch(() => null),
        ])
        this.sessionMessages = msgs || []
        this.sessionMessagesSkip = this.sessionMessages.length
        this.sessionMessagesHasMore = this.sessionMessages.length === 20
        if (pr && pr.system_prompt) this.sessionSystemPrompt = pr.system_prompt
        if (pushState) {
          history.pushState({ sessionID }, '', '/sessions/' + enc)
        }
        // Restore the previously open panel; reload workspace if it was visible.
        if (prevPanel) {
          this.activePanel = prevPanel
          if (prevPanel === 'workspace') this.loadWorkspace()
        }
        // Scroll transcript to bottom after render.
        this.$nextTick(() => {
          const el = this.$refs.transcript
          if (el) el.scrollTop = el.scrollHeight
        })
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      } finally {
        this.sessionMessagesLoading = false
      }
      // If the transcript isn't scrollable yet (messages fit the viewport),
      // the scroll event can never fire — keep loading until it overflows.
      if (this.sessionMessagesHasMore) {
        this.$nextTick(() => {
          const el = this.$refs.transcript
          if (el && el.scrollHeight <= el.clientHeight + 20) {
            this.handleTranscriptScroll(el)
          }
        })
      }
    },

    async handleTranscriptScroll(el) {
      if (el.scrollTop > 60 || !this.sessionMessagesHasMore || this.sessionMessagesLoading) return
      const enc = encodeURIComponent(this.sessionDetail.id)
      const prevHeight = el.scrollHeight
      this.sessionMessagesLoading = true
      try {
        const older = await api('GET', `/api/sessions/${enc}/messages?limit=20&skip=${this.sessionMessagesSkip}`)
        if (!older || older.length === 0) {
          this.sessionMessagesHasMore = false
          return
        }
        this.sessionMessages = [...older, ...this.sessionMessages]
        this.sessionMessagesSkip += older.length
        this.sessionMessagesHasMore = older.length === 20
        // Restore scroll position so the view doesn't jump.
        this.$nextTick(() => {
          el.scrollTop = el.scrollHeight - prevHeight
        })
      } catch (e) {
        console.error(e)
      } finally {
        this.sessionMessagesLoading = false
      }
      // Keep filling if the transcript still doesn't overflow after this batch.
      if (this.sessionMessagesHasMore) {
        this.$nextTick(() => {
          if (el.scrollHeight <= el.clientHeight + 20) {
            this.handleTranscriptScroll(el)
          }
        })
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
      if (name === 'tools' && this.activePanel === 'tools' && this.tools.length === 0) {
        this.loadTools()
      }
      if (name === 'workspace' && this.activePanel === 'workspace' && !this.sessionWorkspace) {
        this.loadWorkspace()
      }
    },

    _destroyFileTree() {
      if (this._fileTree) {
        this._fileTree.cleanUp()
        this._fileTree = null
      }
    },

    async loadWorkspace() {
      if (!this.sessionDetail) return
      this.workspaceLoading = true
      try {
        const enc = encodeURIComponent(this.sessionDetail.id)
        const data = await api('GET', `/api/sessions/${enc}/workspace`)
        this.sessionWorkspace = data
        if (data && data.paths && data.paths.length > 0) {
          this.$nextTick(async () => {
            const container = this.$refs.fileTreeContainer
            if (!container) return
            container.innerHTML = ''
            const host = document.createElement('file-tree-container')
            host.style.cssText = 'display: block; width: 100%;'
            container.appendChild(host)
            const { FileTree } = await getTreesModule()
            this._destroyFileTree()
            // Resolve actual computed colour values from daisyUI tokens so the
            // @pierre/trees shadow-DOM theme receives real hex/oklch strings.
            const style = getComputedStyle(document.documentElement)
            const resolve = v => style.getPropertyValue(v).trim() || undefined
            const isDark = document.documentElement.dataset.theme === 'dark' ||
              window.matchMedia('(prefers-color-scheme: dark)').matches
            const theme = {
              type: isDark ? 'dark' : 'light',
              bg: resolve('--color-base-100'),
              fg: resolve('--color-base-content'),
              colors: {
                'input.background': resolve('--color-base-200'),
                'input.border': resolve('--color-base-300'),
                'sideBar.background': resolve('--color-base-100'),
                'sideBar.foreground': resolve('--color-base-content'),
                'sideBar.border': resolve('--color-base-300'),
                'list.hoverBackground': resolve('--color-base-200'),
                'list.activeSelectionBackground': resolve('--color-base-200'),
                'list.activeSelectionForeground': resolve('--color-primary'),
              },
            }
            const tree = new FileTree({
              paths: data.paths,
              initialExpansion: 'first',
              search: false,
            })
            tree.render({ fileTreeContainer: host, theme })
            this._fileTree = tree
          })
        }
      } catch (e) {
        console.error('loadWorkspace:', e)
      } finally {
        this.workspaceLoading = false
      }
    },

    backToList() {
      this.sessionDetail = null
      this.sessionMessages = []
      this.sessionMessagesSkip = 0
      this.sessionMessagesHasMore = false
      this.sessionSystemPrompt = ''
      this.sessionWorkspace = null
      this._destroyFileTree()
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
        this.sessionsOffset++
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
      // Scroll to bottom when user sends.
      this.$nextTick(() => {
        const el = this.$refs.transcript
        if (el) el.scrollTop = el.scrollHeight
      })
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

        const scrollToBottom = () => {
          const el = this.$refs.transcript
          if (el && el.scrollHeight - el.scrollTop - el.clientHeight < 200) {
            el.scrollTop = el.scrollHeight
          }
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
            scrollToBottom()
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
              scrollToBottom()
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
