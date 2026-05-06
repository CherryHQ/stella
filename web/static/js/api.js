/**
 * ESM fetch wrapper for the admin JSON API.
 *
 * @param {string} method - HTTP method (GET, POST, PUT, DELETE).
 * @param {string} path - API path (e.g. "/api/providers").
 * @param {any} [body] - Optional request body (will be JSON-encoded).
 * @returns {Promise<any>} The `data` field from the JSON response.
 * @throws {Error} If the response contains an `error` field.
 */
export async function api(method, path, body = null) {
  const opts = { method }
  if (body instanceof FormData) {
    opts.body = body
  } else {
    opts.headers = { 'Content-Type': 'application/json' }
    if (body) opts.body = JSON.stringify(body)
  }
  const res = await fetch(path, opts)
  if (res.status === 204 || res.headers.get('content-length') === '0') {
    return null
  }
  const json = await res.json()
  if (json.error) {
    throw new Error(json.error)
  }
  return json.data
}
