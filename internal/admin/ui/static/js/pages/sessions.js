import { api } from '/static/js/api.js'
import { formatTime } from '/static/js/utils.js'

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
    showTools: false,
    showSystemPrompt: false,

    async init() {
      await Promise.all([this.loadSessions(), this.loadTools()])
    },

    formatTime(ts) {
      return formatTime(ts)
    },

    async loadSessions() {
      try {
        this.sessions = await api('GET', '/api/sessions') || []
      } catch (e) {
        console.error(e)
      }
    },

    async openSession(sessionID) {
      this.sessionMessagesLoading = true
      this.sessionMessages = []
      this.sessionSystemPrompt = ''
      try {
        const enc = encodeURIComponent(sessionID)
        this.sessionDetail = await api('GET', '/api/sessions/' + enc)
        const [msgs, pr] = await Promise.all([
          api('GET', '/api/sessions/' + enc + '/messages'),
          api('GET', '/api/sessions/' + enc + '/system-prompt').catch(() => null),
        ])
        this.sessionMessages = msgs || []
        if (pr && pr.system_prompt) this.sessionSystemPrompt = pr.system_prompt
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

    backToList() {
      this.sessionDetail = null
      this.sessionMessages = []
      this.sessionSystemPrompt = ''
      this.showTools = false
      this.showSystemPrompt = false
    },
  }))
}
