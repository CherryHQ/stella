/**
 * Registers the loginPage Alpine.data component.
 *
 * @param {import('alpinejs').Alpine} Alpine
 */
export function register(Alpine) {
  Alpine.data('loginPage', () => ({
    isRegister: false,
    username: '',
    password: '',
    confirmPassword: '',
    error: '',
    loading: false,
    // Feishu login state
    feishuAvailable: false,
    feishuLoading: false,
    feishuUnavailableReason: '',

    init() {
      this.checkFeishuAvailability()
    },

    toggleMode() {
      this.isRegister = !this.isRegister
      this.error = ''
      this.password = ''
      this.confirmPassword = ''
    },

    async checkFeishuAvailability() {
      try {
        const res = await fetch('/api/auth/login/feishu/availability')
        const data = await res.json()
        this.feishuAvailable = data.available
        if (!data.available && data.reason) {
          const reasonMap = {
            'no_login_instance': 'Feishu login is not configured.',
            'multiple_login_instances': 'Multiple Feishu instances configured. Please configure exactly one.',
            'missing_credentials': 'Feishu login credentials are incomplete.',
            'store_error': 'Unable to check Feishu login status.',
          }
          this.feishuUnavailableReason = reasonMap[data.reason] || 'Feishu login is currently unavailable.'
        }
      } catch (e) {
        // Silently fail - Feishu login is optional
        this.feishuAvailable = false
      }
    },

    async loginWithFeishu() {
      this.error = ''
      this.feishuLoading = true
      try {
        const redirectUrl = new URLSearchParams(window.location.search).get('redirect') || '/'
        const res = await fetch('/api/auth/login/feishu/start', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ redirect_url: redirectUrl }),
        })
        const data = await res.json()
        if (data.error) {
          this.error = data.error
          return
        }
        if (data.auth_url) {
          window.location.href = data.auth_url
        } else {
          this.error = 'Failed to start Feishu login'
        }
      } catch (e) {
        this.error = e.message || 'Failed to start Feishu login'
      } finally {
        this.feishuLoading = false
      }
    },

    async login() {
      this.error = ''
      this.loading = true
      try {
        const res = await fetch('/api/auth/login', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({
            username: this.username,
            password: this.password,
          }),
        })
        const json = await res.json()
        if (json.error) {
          this.error = json.error
          return
        }
        window.location.href = '/'
      } catch (e) {
        this.error = e.message || 'Login failed'
      } finally {
        this.loading = false
      }
    },

    async register() {
      this.error = ''
      if (this.password !== this.confirmPassword) {
        this.error = 'Passwords do not match'
        return
      }
      if (this.password.length < 8) {
        this.error = 'Password must be at least 8 characters'
        return
      }
      this.loading = true
      try {
        const res = await fetch('/api/auth/register', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({
            username: this.username,
            password: this.password,
          }),
        })
        const json = await res.json()
        if (json.error) {
          this.error = json.error
          return
        }
        window.location.href = '/'
      } catch (e) {
        this.error = e.message || 'Registration failed'
      } finally {
        this.loading = false
      }
    },
  }))
}
