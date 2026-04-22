import { credentialsSettingsMixin } from '/static/js/pages/account_settings.js'

/**
 * Registers the credentialsPage Alpine.data component.
 *
 * @param {import('alpinejs').Alpine} Alpine
 */
export function register(Alpine) {
  Alpine.data('credentialsPage', () => ({
    ...credentialsSettingsMixin(),
  }))
}
