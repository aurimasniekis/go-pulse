/** A readable message for any thrown value. */
export function errorMessage(e: unknown): string {
  if (e instanceof Error) return e.message
  return typeof e === 'string' ? e : 'Something went wrong'
}
