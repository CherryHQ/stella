import { profileSettingsMixin } from '/static/js/pages/account_settings.js'

/**
 * Registers the accountPage Alpine.data component.
 *
 * @param {import('alpinejs').Alpine} Alpine
 */
export function register(Alpine) {
  Alpine.data('accountPage', () => ({
    ...profileSettingsMixin(),
  }))
}
