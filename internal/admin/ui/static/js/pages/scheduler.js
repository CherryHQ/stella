import { api } from '/static/js/api.js'
import { formatTime } from '/static/js/utils.js'

/**
 * Registers the schedulerPage Alpine.data component.
 *
 * @param {import('alpinejs').Alpine} Alpine
 */
export function register(Alpine) {
  Alpine.data('schedulerPage', () => ({
    jobs: [],
    agents: [],
    isAdmin: false,
    editingJobId: null,
    expandedJobId: null,
    triggeringJobId: null,
    runHistories: {},
    jobForm: {
      name: '',
      cron: '',
      every: '',
      message: '',
      session_mode: 'reuse',
      enabled: true,
      agent_id: '',
      schedule_type: 'cron',
      system_job: false,
    },
    confirmMsg: '',
    confirmAction: () => {},

    async init() {
      await Promise.all([
        this.loadJobs(),
        this.loadAgents(),
        this.loadMe(),
      ])
    },

    async loadMe() {
      try {
        const me = await api('GET', '/api/auth/me')
        this.isAdmin = me.is_admin || false
      } catch (_) {
        this.isAdmin = false
      }
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

    jobScheduleText(j) {
      if (j.cron) return j.cron
      if (j.every) return 'every ' + j.every
      if (j.at) return 'at ' + j.at
      return 'unscheduled'
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
        system_job: false,
      }
      this.editingJobId = null
    },

    editJob(j) {
      if (j.owner_kind === 'plugin') return
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
        system_job: !j.user_id,
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
      if (this.isAdmin && this.jobForm.system_job) {
        payload.user_id = 0
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
      if (j.owner_kind === 'plugin') return
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
      const job = this.jobs.find((item) => item.id === id)
      if (job?.owner_kind === 'plugin') return
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

    async triggerJob(j) {
      this.triggeringJobId = j.id
      try {
        await api('POST', '/api/scheduler/jobs/' + j.id + '/run')
        this.$store.toast.show('Job triggered')
        if (this.expandedJobId === j.id) {
          await this.loadRuns(j.id)
        }
        await this.loadJobs()
      } catch (e) {
        this.$store.toast.show(e.message, 'error')
      } finally {
        this.triggeringJobId = null
      }
    },

    async loadRuns(jobId) {
      try {
        this.runHistories[jobId] = await api('GET', '/api/scheduler/jobs/' + jobId + '/runs') || []
      } catch (e) {
        console.error(e)
      }
    },

    async toggleRuns(jobId) {
      if (this.expandedJobId === jobId) {
        this.expandedJobId = null
        return
      }
      this.expandedJobId = jobId
      await this.loadRuns(jobId)
    },

    formatTime,

    statusBadgeClass(status) {
      if (status === 'success') return 'badge-success'
      if (status === 'error') return 'badge-error'
      if (status === 'running') return 'badge-warning'
      return 'badge-ghost'
    },
  }))
}
