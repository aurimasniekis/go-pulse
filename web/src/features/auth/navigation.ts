import { safeNextPath } from '@/lib/redirect'

/** Leaves a sign-in page for the app with a full page load, so every query starts from the new session. */
export function enterApp(target?: string) {
  window.location.assign(safeNextPath(target ?? '/'))
}

/** Drops one-time tokens from the address bar (history, screenshots, shoulder surfing). */
export function stripTokenFromURL() {
  const url = new URL(window.location.href)
  if (url.searchParams.has('token')) {
    url.searchParams.delete('token')
    window.history.replaceState(window.history.state, '', `${url.pathname}${url.search}${url.hash}`)
  }
}
