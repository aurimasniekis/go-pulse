// The ca option adds to node's root certificates instead of replacing them.
import { execFileSync } from 'node:child_process'
import { mkdtempSync, readFileSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { connect as tlsConnect, createServer, rootCertificates, type TLSSocket } from 'node:tls'
import { describe, expect, it } from 'vitest'
import { withRoots } from '../src/tls.js'

function hasOpenSSL(): boolean {
  try {
    execFileSync('openssl', ['version'])
    return true
  } catch {
    return false
  }
}

describe('withRoots', () => {
  it('keeps every root certificate and appends the CA', () => {
    const bundle = withRoots('-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n', rootCertificates)
    for (const r of rootCertificates) expect(bundle).toContain(r)
    expect(bundle.trimEnd().endsWith('AAAA\n-----END CERTIFICATE-----')).toBe(true)
  })

  it.skipIf(!hasOpenSSL())('trusts a server signed by the extra CA', async () => {
    const dir = mkdtempSync(join(tmpdir(), 'pulse-tls-'))
    const run = (...a: string[]) => execFileSync('openssl', a, { cwd: dir, stdio: 'pipe' })
    run('req', '-x509', '-newkey', 'ec', '-pkeyopt', 'ec_paramgen_curve:P-256', '-nodes', '-days', '1', '-subj', '/CN=pulse test CA',
      '-keyout', 'ca.key', '-out', 'ca.pem')
    run('req', '-newkey', 'ec', '-pkeyopt', 'ec_paramgen_curve:P-256', '-nodes', '-subj', '/CN=localhost', '-keyout', 'srv.key', '-out', 'srv.csr')
    writeFileSync(join(dir, 'san.ext'), 'subjectAltName=DNS:localhost\n')
    run('x509', '-req', '-in', 'srv.csr', '-CA', 'ca.pem', '-CAkey', 'ca.key', '-CAcreateserial', '-days', '1', '-out', 'srv.pem',
      '-extfile', 'san.ext')
    const read = (f: string) => readFileSync(join(dir, f), 'utf8')
    const server = createServer({ key: read('srv.key'), cert: read('srv.pem') }, (s) => s.end())
    await new Promise<void>((r) => server.listen(0, '127.0.0.1', r))
    const port = (server.address() as { port: number }).port
    try {
      const sock = await new Promise<TLSSocket>((resolve, reject) => {
        const s = tlsConnect({ host: '127.0.0.1', port, servername: 'localhost', ca: withRoots(read('ca.pem'), rootCertificates) }, () => resolve(s))
        s.on('error', reject)
      })
      expect(sock.authorized).toBe(true)
      sock.destroy()
    } finally {
      server.close()
    }
  })
})
