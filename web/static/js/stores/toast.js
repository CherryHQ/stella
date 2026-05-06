/**
 * Registers the global toast Alpine store.
 *
 * Usage in Alpine templates:
 *   $store.toast.show('Saved')
 *   $store.toast.show('Something went wrong', 'error')
 *
 * @param {import('alpinejs').Alpine} Alpine
 */
export function registerToast(Alpine) {
  Alpine.store('toast', {
    message: '',
    type: 'success',
    visible: false,
    _timeout: null,

    show(msg, type = 'success') {
      this.message = msg
      this.type = type
      this.visible = true
      if (this._timeout) clearTimeout(this._timeout)
      this._timeout = setTimeout(() => {
        this.visible = false
      }, 3000)
    },
  })
}
