import { safeNextPath } from '@/lib/redirect'

/**
 * Browser navigation into the OIDC flow (GET /auth/oidc/{slug}/start, outside /api): a full page load, because the
 * provider round trip sets and checks a binding cookie. Errors come back as /login?sso_error=<code>.
 */
export type OidcPurpose = 'login' | 'stepup' | 'test' | 'link'

export function oidcStartUrl(slug: string, purpose: OidcPurpose, returnTo?: string, linkToken?: string): string {
  const params = new URLSearchParams({ purpose })
  if (returnTo !== undefined) params.set('return_to', safeNextPath(returnTo))
  if (linkToken) params.set('link_token', linkToken)
  return `/auth/oidc/${encodeURIComponent(slug)}/start?${params.toString()}`
}

/** Messages for sso_error codes (stable codes from the server, never internals). */
export function ssoErrorMessage(code: string): string {
  switch (code) {
    case 'mfa_required':
      return 'Your identity provider could not prove a fresh sign-in. Confirm with your Pulse authenticator code.'
    case 'reauth_required':
      return 'Linking an identity needs a recent sign-in. Confirm it is you in Pulse, then start linking again.'
    case 'forbidden':
      return 'Single sign-on was refused for this account. Ask an administrator to check your account or the provider mappings.'
    case 'not_found':
      return 'That sign-in provider is not available.'
    case 'precondition':
      return 'Single sign-on could not continue (for example, step-up needs an SSO session). Start again.'
    case 'rate_limited':
      return 'Too many sign-in attempts. Wait a minute and try again.'
    case 'unavailable':
      return 'The identity provider is unavailable. Try again later, or sign in another way.'
    case 'conflict':
      return 'This identity is already linked to another account, or its email belongs to another user. Ask an administrator.'
    case 'unauthorized':
    case 'invalid':
      return 'The sign-in attempt expired or was already used. Start again.'
    default:
      return 'Single sign-on failed. Try again, or contact an administrator.'
  }
}

const STEPUP_RETURN_KEY = 'pulse.stepup.return'

/** Remembers where an SSO step-up started, for the TOTP fallback after /login?sso_error=mfa_required. */
export function rememberStepUpReturn(path: string): void {
  try {
    window.sessionStorage.setItem(STEPUP_RETURN_KEY, safeNextPath(path))
  } catch {
    // not persisted: the fallback returns to the dashboard
  }
}

export function takeStepUpReturn(): string {
  try {
    const v = window.sessionStorage.getItem(STEPUP_RETURN_KEY)
    window.sessionStorage.removeItem(STEPUP_RETURN_KEY)
    return safeNextPath(v)
  } catch {
    return '/'
  }
}
