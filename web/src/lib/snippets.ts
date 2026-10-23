import type { Grant } from '@/api/client'

export interface SnippetCtx {
  name: string
  natsUrl: string
  publicUrl: string
  grants: Grant[]
}

const firstSubject = (gs: Grant[], action: Grant['action'], fallback: string) => {
  const s = gs.find((g) => g.action === action)?.subject
  return s ?? fallback
}

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

/** Connection snippets for a client (PL-AUTH-011). */
export function clientSnippets(c: SnippetCtx): Record<string, string> {
  const pub = exampleSubject(firstSubject(c.grants, 'publish', 'evt.example.thing.created'))
  const sub = firstSubject(c.grants, 'subscribe', firstSubject(c.grants, 'durable', 'evt.example.>'))
  const creds = `${c.name}.creds`
  return {
    Go: `import "github.com/aurimasniekis/go-pulse/pkg/pulse"

c, err := pulse.Connect("${c.natsUrl}", pulse.WithCredsFile("${creds}"), pulse.WithSource("${c.name}"))
if err != nil { log.Fatal(err) }
defer c.Close()

_, err = c.Publish(ctx, "${pub}", "example.v1", map[string]any{"hello": "world"})

sub, err := c.Subscribe(ctx, "${sub}", func(ctx context.Context, e pulse.Event) error {
\tlog.Printf("%s %s", e.Subject, e.Data)
\treturn nil
}, pulse.Since(time.Now().Add(-24*time.Hour)))`,
    Python: `# pip install "git+https://github.com/aurimasniekis/go-pulse#subdirectory=sdk/python"
import asyncio
from pulse import connect

async def main():
    c = await connect("${c.natsUrl}", creds="${creds}", source="${c.name}")
    await c.publish("${pub}", "example.v1", {"hello": "world"})
    async for e in c.subscribe("${sub}", since="24h"):
        print(e.subject, e.data)

asyncio.run(main())`,
    TypeScript: `// pnpm add "github:aurimasniekis/go-pulse#path:/sdk/ts"
import { connect } from '@pulse/sdk'

const c = await connect({ servers: '${c.natsUrl}', creds: await readFile('${creds}', 'utf8'), source: '${c.name}' })
await c.publish('${pub}', 'example.v1', { hello: 'world' })
for await (const e of c.subscribe('${sub}', { since: '24h' })) console.log(e.subject, e.json())`,
    'pulse CLI': `pulse context add ${c.name} --url ${c.natsUrl} --creds ./${creds}
pulse pub ${pub} '{"hello":"world"}' -t example.v1
pulse sub '${sub}' --since 24h`,
    'nats CLI': `nats --server ${c.natsUrl} --creds ./${creds} --inbox-prefix _INBOX_<client-id> \\
  pub ${pub} '{"hello":"world"}' -H ce-type:example.v1`,
  }
}

/** Snippets for an HTTP token. */
export function tokenSnippets(publicUrl: string, grants: Grant[]): Record<string, string> {
  const base = (publicUrl || 'https://events.example.com') + '/api/v1'
  const pub = exampleSubject(firstSubject(grants, 'publish', 'evt.example.thing.created'))
  const sub = firstSubject(grants, 'subscribe', 'evt.example.>')
  return {
    Publish: `curl -X POST ${base}/publish/${pub} \\
  -H "Authorization: Bearer $PULSE_TOKEN" \\
  -H "Ce-Type: example.v1" -H "Content-Type: application/json" \\
  -H "Idempotency-Key: $(uuidgen)" \\
  -d '{"hello":"world"}'`,
    Replay: `curl -G ${base}/events -H "Authorization: Bearer $PULSE_TOKEN" \\
  --data-urlencode 'subject=${sub}' --data-urlencode 'since=2026-01-01T00:00:00Z'`,
    Tail: `curl -N -G ${base}/tail -H "Authorization: Bearer $PULSE_TOKEN" --data-urlencode 'subject=${sub}'`,
  }
}
