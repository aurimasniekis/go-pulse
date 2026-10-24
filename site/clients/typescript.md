# TypeScript SDK

`@pulse/sdk`, in [`sdk/ts`](https://github.com/aurimasniekis/go-pulse/tree/main/sdk/ts), on the `@nats-io/*` v3
libraries. It runs in node 22 or later (TCP or WebSocket) and in browsers (WebSocket through `pulsed`'s `/ws` proxy).

```sh
pnpm add "github:aurimasniekis/go-pulse#path:/sdk/ts"   # builds dist/ on install (prepare script)
```

## Node

```ts
import { readFile } from 'node:fs/promises'
import { connect } from '@pulse/sdk'

const c = await connect({
  servers: 'tls://r.events.example.com:4222',
  creds: await readFile('door-bot.creds', 'utf8'),
  source: 'door-bot',
})

const ack = await c.publish('evt.home.door.opened', 'home.door.opened.v1', { door: 'front' })
console.log(ack.sequence, ack.id, ack.duplicate)

for await (const e of c.subscribe('evt.home.>', { since: '24h' })) {
  console.log(e.sequence, e.subject, e.type, e.json())
}
```

## Browser

Browsers cannot open TCP connections, so they use NATS over WebSocket at `wss://events.example.com/ws`. `pulsed`
proxies it to NATS (through Cloudflare), and the client authenticates with `.creds` exactly as over TCP.

With a bundler, `import { connect } from '@pulse/sdk'` works as is: the node TCP transport is only loaded for
`tls://` / `nats://` URLs. Without one, use the prebuilt bundle `dist/pulse.browser.js` (exported as
`@pulse/sdk/browser`), which defines a global `Pulse`:

```html
<script src="pulse.browser.js"></script>
<script type="module">
  const c = await Pulse.connect({ servers: 'wss://events.example.com/ws', creds: CREDS, source: 'dashboard-widget' })
  for await (const e of c.subscribe('evt.home.>', { since: '1h' })) render(e.subject, e.json())
</script>
```

::: warning Credentials in a browser
Anything a page can read, its users can read. Give each browser app its **own client** with the narrowest grants (often
`subscribe` only), keep the page private, and rotate the client if the page leaks.
:::

## Connect

`connect(options)`:

| Option | Effect |
|---|---|
| `servers` | One URL or a list. `tls://` / `nats://` use the node TCP transport; when every URL is `ws://` / `wss://`, the WebSocket transport is used (browsers, or node ≥ 22). |
| `creds` | The `.creds` **content** (string or bytes); read the file yourself. The client ID inside sets the inbox prefix `_INBOX_<client-id>`. |
| `source` | `ce-source` for published events; also the connection name unless `name` is set. |
| `name` | NATS connection name. |
| `stream` | Default stream for subscriptions (`EVENTS`). |
| `ca` | A CA as PEM for `tls://` in node, trusted **instead of** the system roots. Use it only while 4222 serves the internal bootstrap certificate, and remove it once the Let's Encrypt certificate is served. |
| `nats` | Extra `ConnectionOptions` for `@nats-io`. |

The connection reconnects forever. `await c.close()` drains it. `c.nc` and `c.js` are the underlying connection and
JetStream client; `c.clientId` is the client ID.

## Publish

```ts
await c.publish(subject, type, data, { id, time, contentType, source, headers, blob })
```

`data`: a `Uint8Array` is `application/octet-stream`, a `string` is `text/plain; charset=utf-8`, `undefined` is empty,
anything else is JSON. `time` takes a `Date`, an RFC 3339 string or `bigint` nanoseconds. Returns
`{ id, stream, sequence, duplicate }`. A bad subject throws `SubjectError`.

## Subscribe

```ts
for await (const e of c.subscribe(filter, { since, seq, new: true, durable, stream, ackWaitMs })) { … }
```

- `since`: a duration (`'24h'`, `'7d'`, `'1h30m'`), an RFC 3339 string, a `Date` or `bigint` nanoseconds; replays by
  stream timestamp. `seq` replays from a sequence, `new: true` delivers only new events.
- **Without `durable`**: an ordered consumer that yields `Event` objects and resumes after reconnects.
- **With `durable: 'name'`**: the consumer `<client-id>_name` (needs a `durable` grant) yields `Delivery` objects with
  `ack()`, `nak(delayMs?)` and `term()`. A delivery left unsettled when the loop moves on is acknowledged; leaving the
  loop redelivers unsettled and prefetched events after the ack wait. Start options only apply when the durable is
  first created.

Breaking out of the `for await` loop stops the consumer.

### The Event

`subject`, `id`, `type`, `source`, `timeNs` (`bigint`; `e.time` is the RFC 3339 string with nanoseconds),
`contentType`, `data` (`Uint8Array`), `blob`, `extra` (other headers), and the read side `stream`, `sequence`,
`streamTimeNs`. Helpers: `e.json<T>()`, `e.text()`, `e.untyped`.

Times are `bigint` nanoseconds because `ce-time` carries nanoseconds that `Date` would round.

## Request/reply and blobs

```ts
const reply = await c.request('svc.weather.now', '{"city":"Vilnius"}', 5000)   // needs a request grant

const ref = await c.putBlob(bytes, '.jpg')                                    // needs a blobs grant
await c.publish('evt.home.camera.snapshot', 'home.camera.snapshot.v1', { camera: 'porch' }, { blob: ref })
const data = await c.getBlob(e.blob!)                                         // checks the SHA-256
```

## Tests

`make sdk-test` runs the unit tests against the shared conformance vectors. `make test-sdk-interop` runs Go, Python and
TypeScript (node over `tls://` and `ws://`) against a real server, and the Playwright suite replays events in a real
browser over `/ws`.
