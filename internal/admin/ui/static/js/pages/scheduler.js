import { api } from '/static/js/api.js'

/**
 * Registers the schedulerPage Alpine.data component.
 *
 * @param {import('alpinejs').Alpine} Alpine
 */
export function register(Alpine) {
  Alpine.data('schedulerPage', () => ({
    jobs: [],
    agents: [],
    editingJobId: null,
    jobForm: {
      name: '',
      cron: '',
      every: '',
      message: '',
      session_mode: 'reuse',
      enabled: true,
      agent_id: '',
      schedule_type: 'cron',
    },
    confirmMsg: '',
    confirmAction: () => {},

    async init() {
      await Promise.all([
        this.loadJobs(),
        this.loadAgents(),
      ])
    },

    async loadJobs() {
      try {
        this.jobs = await api('GET', '/api/scheduler/jobs') || []
      } catch (e) {
        console.error(e)
      }
    },

    async loadAgents() {
      try {
        this.agents = await api('GET', '/api/agents') || []
      } catch (e) {
        console.error(e)
      }
    },

    resetJobForm() {
      this.jobForm = {
        name: '',
        cron: '',
        every: '',
        message: '',
        session_mode: 'reuse',
        enabled: true,
        agent_id: '',
        schedule_type: 'cron',
      }
      this.editingJobId = null
    },

    editJob(j) {
      this.editingJobId = j.id
      this.jobForm = {
        name: j.name,
        message: j.message,
        schedule_type: j.cron ? 'cron' : 'every',
        cron: j.cron || '',
        every: j.every || '',
        session_mode: j.session_mode || 'reuse',
        enabled: j.enabled,
        agent_id: j.agent_id || '',
      }
    },

    async saveJob() {
      const payload = {
        name: this.jobForm.name,
        message: this.jobForm.message,
        cron: this.jobForm.schedule_type === 'cron' ? this.jobForm.cron : '',
        every: this.jobForm.schedule_type === 'every' ? this.jobForm.every : '',
        session_mode: this.jobForm.session_mode,
        enabled: this.jobForm.enabled,
        agent_id: this.jobForm.agent_id,
      }
      try {
        if (this.editingJobId) {
          await api('PUT', '/api/scheduler/jobs/' + this.editingJobId, payload)
        } else {
          await api('POST', '/api/scheduler/jobs', payload)
        }
        this.resetJobForm()
        await this.loadJobs()
        this.$store.toast.show('Saved')
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      }
    },

    async toggleJob(j) {
      try {
        await api('PUT', '/api/scheduler/jobs/' + j.id, {
          name: j.name,
          message: j.message,
          cron: j.cron || '',
          every: j.every || '',
          session_mode: j.session_mode,
          enabled: !j.enabled,
          agent_id: j.agent_id || '',
        })
        await this.loadJobs()
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      }
    },

    async doDeleteJob(id) {
      try {
        await api('DELETE', '/api/scheduler/jobs/' + id)
        await this.loadJobs()
        this.$store.toast.show('Deleted')
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      }
    },

    confirmDelete(msg, action) {
      this.confirmMsg = msg
      this.confirmAction = action
    },
  }))
}
