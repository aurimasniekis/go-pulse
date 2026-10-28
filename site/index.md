---
layout: home

hero:
  name: Pulse
  text: One durable event bus for all your tools
  tagline: >-
    Bots, agents, scripts and webhooks publish to one NATS JetStream stream. Each tool gets its own least-privilege
    credentials, and any new tool can replay years of history from a timestamp.
  image:
    src: /logo.svg
    alt: Pulse
  actions:
    - theme: brand
      text: Get started
      link: /guide/what-is-pulse
    - theme: alt
      text: Try it locally
      link: /guide/try-locally
    - theme: alt
      text: Clients & SDKs
      link: /clients/

features:
  - title: History you can replay
    details: >-
      Every event lands in the EVENTS stream, kept until the stream reaches its size limit. Subscribe with a start
      time or sequence and get the past in order, then live events. Durable consumers keep their place.
  - title: Least privilege per tool
    details: >-
      Each client is its own NATS user with permissions built from grants such as publish:evt.home.>. Credentials are
      shown once. Rotation and revocation disconnect live sessions within seconds.
  - title: NATS, HTTP or webhooks
    details: >-
      Go, Python and TypeScript SDKs and a CLI over NATS TLS. HTTP tokens for publish, replay and an SSE tail. Signed
      inbound webhooks from GitHub, Stripe or any HMAC sender, and ordered, retried outbound webhooks.
  - title: Secure at the edge
    details: >-
      pulsed terminates TLS behind Cloudflare with Authenticated Origin Pulls, gets Let's Encrypt for raw NATS, and
      bans addresses that keep failing authentication, in nftables, over HTTP and at Cloudflare.
  - title: Backups that keep their timestamps
    details: >-
      Nightly, age-encrypted, deduplicated backups to S3-compatible storage on a 7/4/12 schedule. Restores keep
      sequence numbers and stored times, so replay by time works the same afterwards.
  - title: Users, roles and single sign-on
    details: >-
      Dashboard users sign in with a password and TOTP or through any OpenID Connect provider. Four roles, from
      administrator to viewer, and every sign-in and change is in the audit log. Reach it by SSH tunnel or behind
      Cloudflare.
---

<div class="pl-home">

## How it works

<Flow />

Pulse is small on purpose: one VPS with 2 GB of RAM, one distroless image plus the official NATS image, a single node.

1. `pulsed init` sets NATS up in JWT operator mode, with its own operator, accounts and signing keys sealed by a master
   key.
2. `pulsed admin bootstrap` prints a one-time link for the first administrator. You sign in to the dashboard with a
   password and TOTP, or later with single sign-on, and invite the rest of your people with a role each.
3. You create a **client** per tool in the dashboard and give it **grants**. Its `.creds` file is shown once.
4. Tools publish events such as `evt.home.door.opened` with a small CloudEvents-style envelope, over NATS, HTTP or a
   signed webhook.
5. Any client with a subscribe grant replays from a time or sequence, then follows live events.

## See it

<Shot name="overview" themed alt="Pulse overview with event counts, throughput over time and the top domains, types and subjects" caption="Overview: event counts, throughput and the top domains, types and subjects" />

<div class="pl-gallery">
<Shot name="explorer" themed alt="Event explorer with filters and a list of events" caption="Event explorer" />
<Shot name="clients" themed alt="Clients with their grants and live connections" caption="Clients and their grants" />
<Shot name="security" themed alt="Security page with automatic and manual bans and authentication failures" caption="Automatic bans and authentication failures" />
<Shot name="webhook-deliveries" alt="An outbound webhook's delivery log with delivered and retried attempts" caption="Outbound webhooks with a delivery log" />
<Shot name="alerts" alt="Alerts with a firing rule, Telegram and Discord notifiers and the rule list" caption="Alerts to Telegram and Discord" />
<Shot name="system" themed alt="System page with NATS, JetStream and certificate status" caption="NATS, JetStream and certificates" />
<Shot name="login" themed alt="Sign-in page with email and password and a single sign-on button" caption="Sign-in: password and TOTP, or single sign-on" />
<Shot name="users" alt="Users page listing people with their roles, sign-in methods and status" caption="Users and roles" />
</div>

## Where to go next

- New here? Read [What is Pulse?](/guide/what-is-pulse) and the [concepts](/guide/concepts).
- Want to see it run? [Try it locally](/guide/try-locally) with Docker in a few minutes.
- Connecting a tool? [Clients & SDKs](/clients/) for Go, Python, TypeScript, the CLI, HTTP and webhooks.
- Running it for real? [Deploy on a VPS](/guide/deploy) and the [operator guides](/operators/).
- Adding people or an identity provider? [Users and single sign-on](/operators/users).

</div>
