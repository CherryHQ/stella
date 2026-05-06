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

    toggleMode() {
      this.isRegister = !this.isRegister
      this.error = ''
      this.password = ''
      this.confirmPassword = ''
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
