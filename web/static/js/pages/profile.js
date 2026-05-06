import { profileSettingsMixin } from '/static/js/pages/account_settings.js'

/**
 * Registers the profilePage Alpine.data component.
 *
 * @param {import('alpinejs').Alpine} Alpine
 */
export function register(Alpine) {
  Alpine.data('profilePage', () => ({
    ...profileSettingsMixin(),
  }))
}
