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
  - title: Small and honest
    details: >-
      One VPS with 2 GB of RAM, one distroless image plus the official NATS image, metrics, alerts and an audit log.
      Single node, single owner. The security page says what is tested and what is not.
---

<div class="pl-home">

<div class="pl-status">

**Pre-release.** No version is tagged yet. Read the [security model](/security/) and its
[release status](/security/#release-status) before you run Pulse anywhere that matters.

</div>

## How it works

<Flow />

1. `pulsed init` sets NATS up in JWT operator mode, with its own operator, accounts and signing keys sealed by a master
   key.
2. You create a **client** per tool in the dashboard and give it **grants**. Its `.creds` file is shown once.
3. Tools publish events such as `evt.home.door.opened` with a small CloudEvents-style envelope, over NATS, HTTP or a
   signed webhook.
4. Any client with a subscribe grant replays from a time or sequence, then follows live events.

## See it

<Shot name="overview" themed alt="Pulse overview with event counts, throughput over time, top domains and types and the disk forecast" caption="Overview: throughput, top domains and types, disk forecast and retention horizon" />

<div class="pl-gallery">
<Shot name="explorer" themed alt="Event explorer with filters and a list of events" caption="Event explorer" />
<Shot name="clients" themed alt="Clients with their grants and live connections" caption="Clients and their grants" />
<Shot name="security" themed alt="Security page with bans and authentication failures" caption="Bans and authentication failures" />
<Shot name="system" themed alt="System page with NATS, JetStream and certificate status" caption="NATS, JetStream and certificates" />
</div>

## Where to go next

- New here? Read [What is Pulse?](/guide/what-is-pulse) and the [concepts](/guide/concepts).
- Want to see it run? [Try it locally](/guide/try-locally) with Docker in a few minutes.
- Connecting a tool? [Clients & SDKs](/clients/) for Go, Python, TypeScript, the CLI, HTTP and webhooks.
- Running it for real? [Deploy on a VPS](/guide/deploy) and the [operator guides](/operators/).

</div>
