# Dashboard SSO contract (`PULSE_AUTH_MODE=oidc`)

> PL-ADM-005. **Not implemented.** This is the contract the future `oidc` Authenticator must meet before the admin
> listener may be exposed (`PULSE_ADMIN_PUBLIC=true`). Until then pulsed refuses `oidc` at start (DECISIONS.md R-02).

## Flow

1. Unauthenticated browser requests to the admin UI get a 302 to the IdP authorization endpoint (authorization code flow
   with PKCE S256, `state` and `nonce`). API requests get 401 with no redirect.
2. The callback (`/auth/callback`) validates `state`, exchanges the code, verifies the ID token (issuer, audience, expiry,
   nonce, signature via JWKS with rotation) and maps the subject/groups to the single admin role via an allowlist.
3. pulsed creates a server-side session (random 256-bit ID, stored hashed in SQLite) and sets the cookie
   `__Host-pulse_session`: `Secure`, `HttpOnly`, `SameSite=Lax`, `Path=/`, no `Domain`. Idle timeout 12 h, absolute 7 d.
4. Logout deletes the session and redirects to the IdP end-session endpoint when available.

## Requirements carried over from mode `none`

- `X-Pulse-CSRF` on every state-changing request (kept even with SameSite cookies).
- Host allowlist: the public hostname must be listed in `PULSE_ADMIN_HOSTS`.
- Strict CSP, `X-Frame-Options: DENY`, no CORS.
- Every admin mutation is audited with the session's subject as actor.
- In the prod profile, `PULSE_ADMIN_PUBLIC=true` is allowed only with `oidc`, and the admin group is mounted on the
  public listener behind AOP mTLS like every other public route.
