/** node's tls `ca` replaces its default trust store, so the extra CA travels with the root certificates (one PEM bundle). */
export function withRoots(ca: string, roots: readonly string[]): string {
  return [...roots, ca.trim()].join('\n') + '\n'
}
