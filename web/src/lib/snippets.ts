import type { Grant } from '@/api/client'

export interface SnippetCtx {
  name: string
  natsUrl: string
  publicUrl: string
  grants: Grant[]
}

const firstGrant = (gs: Grant[], action: Grant['action']) => gs.find((g) => g.action === action && g.subject)

/** A concrete subject for examples: wildcards replaced by sample tokens. */
export function exampleSubject(pattern: string): string {
  const out: string[] = []
  for (const t of pattern.split('.')) {
    if (t === '*') out.push('thing')
    else if (t === '>') out.push(...['thing', 'happened'].slice(0, Math.max(1, 4 - out.length)))
    else out.push(t)
  }
  const fill = ['evt', 'domain', 'entity', 'action']
  while (out.length < 4) out.push(fill[out.length])
  return out.join('.')
}

const lines = (...parts: (string | false)[]) => parts.filter((p) => p !== false).join('\n\n')

/**
 * Connection snippets for a client (PL-AUTH-011). Publish and subscribe examples appear only for what the client's
 * grants allow: its first publish subject, and its first subscribe grant or, failing that, its first durable grant.
 */
export function clientSnippets(c: SnippetCtx): Record<string, string> {
  const pubGrant = firstGrant(c.grants, 'publish')
  const pub = pubGrant ? exampleSubject(pubGrant.subject!) : undefined
  const subGrant = firstGrant(c.grants, 'subscribe')
  const durGrant = subGrant ? undefined : firstGrant(c.grants, 'durable')
  const sub = (subGrant ?? durGrant)?.subject
  const durable = durGrant?.name
  const creds = `${c.name}.creds`
  const none = !pub && !sub
  const out: Record<string, string> = {
    Go: lines(
      `import "github.com/aurimasniekis/go-pulse/pkg/pulse"

c, err := pulse.Connect("${c.natsUrl}", pulse.WithCredsFile("${creds}"), pulse.WithSource("${c.name}"))
if err != nil { log.Fatal(err) }
defer c.Close()`,
      none && '// This client has no publish or subscribe grants yet.',
      !!pub && `_, err = c.Publish(ctx, "${pub}", "example.v1", map[string]any{"hello": "world"})`,
      !!sub &&
        `sub, err := c.Subscribe(ctx, "${sub}", func(ctx context.Context, e pulse.Event) error {
\tlog.Printf("%s %s", e.Subject, e.Data)
\treturn nil
}, ${durable ? `pulse.Durable("${durable}")` : 'pulse.Since(time.Now().Add(-24*time.Hour))'})`,
    ),
    Python: lines(
      `# pip install "git+https://github.com/aurimasniekis/go-pulse#subdirectory=sdk/python"
import asyncio
from pulse import connect

async def main():
    c = await connect("${c.natsUrl}", creds="${creds}", source="${c.name}")`,
      none && '    # This client has no publish or subscribe grants yet.',
      !!pub && `    await c.publish("${pub}", "example.v1", {"hello": "world"})`,
      !!sub &&
        (durable
          ? `    async for d in c.subscribe("${sub}", durable="${durable}"):
        print(d.event.subject, d.event.data)
        await d.ack()`
          : `    async for e in c.subscribe("${sub}", since="24h"):
        print(e.subject, e.data)`),
      'asyncio.run(main())',
    ),
    TypeScript: lines(
      `// pnpm add "github:aurimasniekis/go-pulse#path:/sdk/ts"
import { connect } from '@pulse/sdk'

const c = await connect({ servers: '${c.natsUrl}', creds: await readFile('${creds}', 'utf8'), source: '${c.name}' })`,
      none && '// This client has no publish or subscribe grants yet.',
      !!pub && `await c.publish('${pub}', 'example.v1', { hello: 'world' })`,
      !!sub &&
        (durable
          ? `for await (const d of c.subscribe('${sub}', { durable: '${durable}' })) {
  console.log(d.subject, d.json())
  d.ack()
}`
          : `for await (const e of c.subscribe('${sub}', { since: '24h' })) console.log(e.subject, e.json())`),
    ),
    'pulse CLI': [
      `pulse context add ${c.name} --url ${c.natsUrl} --creds ./${creds}`,
      pub && `pulse pub ${pub} '{"hello":"world"}' -t example.v1`,
      sub && (durable ? `pulse sub '${sub}' --durable ${durable}` : `pulse sub '${sub}' --since 24h`),
    ]
      .filter(Boolean)
      .join('\n'),
  }
  if (pub) {
    out['nats CLI'] = `nats --server ${c.natsUrl} --creds ./${creds} --inbox-prefix _INBOX_<client-id> \\
  pub ${pub} '{"hello":"world"}' -H ce-type:example.v1`
  }
  return out
}

/** Snippets for an HTTP token: publishing with a publish grant, replay and the live tail with a subscribe grant. */
export function tokenSnippets(publicUrl: string, grants: Grant[]): Record<string, string> {
  const base = (publicUrl || 'https://events.example.com') + '/api/v1'
  const pubGrant = firstGrant(grants, 'publish')
  const sub = firstGrant(grants, 'subscribe')?.subject
  const out: Record<string, string> = {}
  if (pubGrant) {
    out.Publish = `curl -X POST ${base}/publish/${exampleSubject(pubGrant.subject!)} \\
  -H "Authorization: Bearer $PULSE_TOKEN" \\
  -H "Ce-Type: example.v1" -H "Content-Type: application/json" \\
  -H "Idempotency-Key: $(uuidgen)" \\
  -d '{"hello":"world"}'`
  }
  if (sub) {
    out.Replay = `curl -G ${base}/events -H "Authorization: Bearer $PULSE_TOKEN" \\
  --data-urlencode 'subject=${sub}' --data-urlencode 'since=2026-01-01T00:00:00Z'`
    out.Tail = `curl -N -G ${base}/tail -H "Authorization: Bearer $PULSE_TOKEN" --data-urlencode 'subject=${sub}'`
  }
  return out
}
