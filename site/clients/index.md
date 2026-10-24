# Clients & SDKs

There are four ways for a tool to talk to Pulse. They all end up as the same events in the same `EVENTS` stream.

| Way | Credential | Reach | Good for |
|---|---|---|---|
| **NATS** with an SDK or the CLI | the client's `.creds` | `tls://r.events.example.com:4222`, direct | long-running tools, bots and agents; replay and durable consumers; request/reply; large payloads as blobs |
| **WebSocket** `/ws` | the client's `.creds` | `wss://events.example.com/ws`, via Cloudflare | browser apps (the TypeScript SDK's browser bundle) |
| **HTTP API** `/api/v1` | an HTTP token | `https://events.example.com/api/v1`, via Cloudflare | shell scripts, cron jobs, serverless functions, anything that can only make HTTP requests |
| **Inbound webhooks** `/hooks/<name>` | the sender's signature | `https://events.example.com/hooks/<name>` | GitHub, Stripe and other services that send webhooks |

And one way out: **outbound webhooks** push matching events to your own HTTP endpoints.

## Which one to pick

- **Prefer NATS.** It is the native protocol: publishes are acknowledged by JetStream, subscriptions replay from a
  time or sequence and then keep following, durable consumers survive restarts, and connections reconnect on their
  own. The SDKs handle the envelope and stay inside the client's grants.
- Use the **HTTP API** when a NATS client is impractical. It publishes (one event or a batch of up to 100), bridges
  requests, replays pages of events and offers an SSE live tail. There are no durable consumers over HTTP.
- Use **inbound webhooks** for third-party senders, so they need no Pulse credential at all.
- Use **outbound webhooks** for services that should receive events without running a NATS client.

## Libraries

| | Install | Page |
|---|---|---|
| Go | `go get github.com/aurimasniekis/go-pulse/pkg/pulse` | [Go SDK](/clients/go) |
| Python ≥ 3.10 (asyncio) | `pip install "git+https://github.com/aurimasniekis/go-pulse#subdirectory=sdk/python"` | [Python SDK](/clients/python) |
| TypeScript (node ≥ 22, browsers) | `pnpm add "github:aurimasniekis/go-pulse#path:/sdk/ts"` | [TypeScript SDK](/clients/typescript) |
| CLI | `go install github.com/aurimasniekis/go-pulse/cmd/pulse@latest` | [pulse CLI](/clients/cli) |

The SDKs are not published to package registries; install them from the repository. All three are tested against the
same [conformance vectors](https://github.com/aurimasniekis/go-pulse/blob/main/sdk/conformance/vectors.json) for the
envelope, and against each other over a real server (Go publishes; Python, TypeScript over TCP and WebSocket, and a
real browser replay the same events).

Every SDK has the same shape:

- **connect** with a `.creds` file; the client ID in the credentials sets the inbox prefix `_INBOX_<client-id>`;
- **publish**(subject, type, data) and wait for the JetStream acknowledgement;
- **subscribe**(filter, since / seq / new / durable): ordered replay then live, or a durable consumer with ack, nak
  and term;
- **request** for request/reply services;
- **put blob / get blob** for payloads above 1 MiB.

## Plain NATS clients

Any NATS client works with a Pulse `.creds` file, with two rules:

1. **Use the client's inbox prefix.** Replies and JetStream acknowledgements are only allowed on
   `_INBOX_<client-id>.>`. With the `nats` CLI: `--inbox-prefix _INBOX_<client-id>`. The ID is on the client's page.
2. **Create filtered consumers.** Clients may only create consumers through the filtered create subject
   (`$JS.API.CONSUMER.CREATE.<stream>.<name>.<filter>`) with a filter inside their grants. Library helpers that use
   other API subjects fail with permission violations.

```sh
nats --server tls://r.events.example.com:4222 --creds ./door-bot.creds --inbox-prefix _INBOX_<client-id> \
  pub evt.home.door.opened '{"door":"front"}' -H ce-type:home.door.opened.v1
```

Events published without the envelope headers are still stored, and shown as untyped. The envelope is described in
[Subjects and the envelope](/clients/envelope).

## Permission errors

NATS reports permission violations asynchronously. A publish or subscribe outside the grants usually shows up as a
**timeout** (5 seconds by default) in the SDK, plus a `Permissions Violation` error on the connection. If something
times out, check the client's grants first.
