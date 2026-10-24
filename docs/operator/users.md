# Dashboard users and single sign-on

Every dashboard page and admin API call needs a signed-in user. Users sign in with an email and password (plus TOTP), or
through any OpenID Connect provider (Keycloak, Authentik, Dex, Entra ID, Google, Okta, …).

## First administrator

There are no default credentials. On the server, while no administrator exists:

```sh
docker compose exec pulsed pulsed admin bootstrap
# One-time setup link (single use, expires …):
#   https://admin.events.example.com/setup?token=…
```

Open the link on the dashboard (`PULSE_ADMIN_URL`: the public dashboard host, or `http://127.0.0.1:8080` through
`ssh -L 8080:127.0.0.1:8080 <server>`), choose an email and a password (at least 12 bytes), sign in and enroll TOTP.
Keep the ten recovery codes offline. `make dev-admin` does the same for the dev stack.

## Roles

| Role | Can |
|---|---|
| administrator | Everything, including users, SSO settings and backup restores. |
| operator | Run the bus: clients and HTTP tokens, webhooks, streams, KV and objects, bans, alert rules, running backups, publishing events, the audit log. Not users, SSO or restores. |
| auditor | Everything a viewer reads, plus the audit log and the user list (not SSO settings). |
| viewer | Read the dashboards, events and payloads, clients, webhooks, bans, alerts, backups and settings; not the audit log or users. |

A user without roles can sign in but sees nothing. Role changes and disabling a user sign them out everywhere. The last
enabled administrator cannot be demoted, disabled or deleted. Password accounts with the administrator or operator role
must enroll TOTP (`PULSE_MFA_REQUIRED_ROLES`).

Sensitive actions ask you to confirm it is you if you have not signed in for 5 minutes (`PULSE_REAUTH_WINDOW`), some of
them with a second factor as well:

| Needs recent sign-in | Also needs a recent second factor (password accounts) |
|---|---|
| Creating clients, rotating them, changing their grants; creating HTTP tokens and webhooks, updating webhooks (secrets are shown once); deleting streams, KV and object buckets | Creating, changing and deleting users; password links, invitations and link invitations; linking and unlinking identities; SSO provider, mapping and sign-in settings changes; backup restore dry runs and restores; creating personal access tokens |

## Adding people

- **Invitation** (Users → Invitations): email and roles; the link (72 h, single use) lets them set a password.
- **User + password link** (Users → New user): then "Password link" (1 h, single use). Also used for resets:
  administrators never set or see other users' passwords.
- **SSO**: link the user's identity at a provider (below), or let the provider create users just in time.

Signing in: one message for every failure; failures are throttled per account and per address (up to 15 minutes, never a
permanent lockout) and written to the audit log. On the public dashboard, failed sign-ins also count toward
[bans](bans.md).

## Single sign-on

Settings → Single sign-on → Add provider:

| Field | Notes |
|---|---|
| Slug, display name | Shown on the sign-in page. |
| Issuer | `https://` and exactly the `issuer` of the provider's discovery document. |
| Client ID, client secret | The secret is optional (PKCE works without one), sealed at rest and never shown again. |
| Scopes | Default `openid profile email`; `openid` is required. |
| Groups claim | The claim with group names (array or string), e.g. `groups`. |
| Provisioning | `preprovisioned` (default): unknown identities are refused. `jit`: unknown identities become users when every restriction matches (verified email domains, `tid` tenants, `hd` hosted domains, groups). JIT users get roles only from mappings. |
| CA bundle, private network, cross-origin endpoints | For providers on a private CA or network. Off by default; TLS verification is never disabled. |
| Step-up fallback, ACR values | See step-up below. |

Register exactly **`<PULSE_ADMIN_URL>/auth/oidc/callback`** as the redirect URI (the provider page shows it). SSO runs only
on that origin; the other listener's sign-in page links to it.

- **Identity** is the provider's issuer + subject. Email addresses are never used to match or merge accounts.
- **Linking** an existing user: an administrator enters the subject, issues a single-use link invitation (72 h), or the
  user signs in with a password and uses Account → Link at the provider.
- **Group → role mappings** replace the user's SSO roles at every SSO login. Manually assigned roles are kept separately.
  Removing a group at the provider removes the role at the next login. Mapped roles apply only to SSO sessions.
- **Test login** (on the provider page) runs the whole flow and shows the claims, the mapping result and the roles the
  user would get, without creating a session.
- **SSO-only**: password sign-in can be disabled after a successful test login, while at least one enabled administrator
  can sign in with SSO. Break-glass recovery still works.
- **Step-up**: SSO users confirm sensitive actions with a fresh login at the provider (`prompt=login`, checked `auth_time`
  and `acr`). Providers that do not send `auth_time` can use the user's Pulse TOTP instead (`local_totp`).
- **Logout** ends the Pulse session; providers with RP-initiated logout enabled also offer signing out there.

## Personal access tokens (CLI)

Account → Personal access tokens: name, one of your roles, lifetime (default 90 days, at most 366). The token is shown
once. It works only on the loopback admin listener (through the SSH tunnel), never on the public dashboard, and it never
has more than the role you still hold.

```sh
pulse context add home --url tls://r.events.example.com:4222 --creds ~/.config/pulse/home.creds \
  --admin-url http://127.0.0.1:8080 --admin-token-file ~/.config/pulse/admin.token
pulse clients ls
```

`pulsed backup run` takes one too (`--token` or `PULSE_ADMIN_TOKEN`). For scripts and deployment checks, mint one on the
host instead: `docker compose exec pulsed pulsed admin token --email automation@example.com --create --role operator --days 1`
(a passwordless user that can only use tokens; same trust as break-glass).

## Break-glass

Lost the only password or second factor, or the identity provider is down while SSO-only is on:

```sh
docker compose exec pulsed pulsed admin recover --email you@example.com
docker compose exec pulsed pulsed admin recover --email new-admin@example.com --new-admin
```

The single-use link (30 minutes) signs the user in, signs out their other sessions, removes their TOTP factor and recovery
codes, and offers a password reset; administrators then enroll a new factor. It needs a shell on the host with the
master key, and every step is in the audit log.

## Public dashboard

`PULSE_ADMIN_PUBLIC=true` serves the dashboard on the public listener, behind Cloudflare and Authenticated Origin Pulls
like every public route:

- **Own hostname** (`PULSE_ADMIN_PUBLIC_HOST=admin.events.example.com`): that host serves only the dashboard; the API
  hostname keeps serving only the API, webhooks and `/ws`. Add a proxied DNS record and include the name on the origin
  certificate.
- **Shared hostname** (`PULSE_ADMIN_PUBLIC_HOST` unset): `events.example.com` serves the dashboard on every path that is
  not `/api/v1`, `/hooks`, `/ws` or `/healthz`.

Sessions are bound to the listener they were created on. The loopback listener and the SSH tunnel keep working.
