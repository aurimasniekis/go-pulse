# @pulse/sdk (TypeScript)

Pulse SDK for node ≥ 22 and browsers (PL-SDK-003), on `@nats-io/*` v3.

```sh
pnpm add "github:aurimasniekis/go-pulse#path:/sdk/ts"   # builds dist/ on install (prepare)
```

```ts
import { readFile } from 'node:fs/promises'
import { connect } from '@pulse/sdk'

const c = await connect({ servers: 'tls://r.events.example.com:4222', creds: await readFile('door-bot.creds', 'utf8'), source: 'door-bot' })
await c.publish('evt.home.door.opened', 'home.door.opened.v1', { door: 'front' })
for await (const e of c.subscribe('evt.home.>', { since: '24h' })) console.log(e.sequence, e.subject, e.json())
```

- `servers`: `tls://` / `nats://` use the node TCP transport; `ws://` / `wss://` use WebSocket. In browsers, connect to
  `wss://events.example.com/ws`, pulsed's NATS WebSocket proxy. The browser bundle `dist/pulse.browser.js`
  (global `Pulse`) is exported as `@pulse/sdk/browser`.
- The inbox prefix `_INBOX_<client-id>` comes from the creds, so scoped clients stay within their grants.
- `publish(subject, type, data, { id, time, contentType, headers, blob })`: `Uint8Array` → octet-stream, `string` →
  text, anything else → JSON.
- `subscribe(filter, { since, seq, new, durable })`: an ordered consumer, or the durable `<client-id>_<name>` yielding
  `Delivery` events with `ack()` / `nak()` / `term()`.
- Times are `bigint` unix nanoseconds (`e.timeNs`, `e.streamTimeNs`; `e.time` is RFC 3339), because `ce-time`
  carries nanoseconds.
- `request(subject, data)`, `putBlob(bytes, ext)`, `getBlob(ref)`.

Tests: `make sdk-test` (conformance vectors) and `make test-sdk-interop` (Go ↔ Python ↔ TS over tls:// and ws://).
