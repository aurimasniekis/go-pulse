/** Server password rule: NFKC-normalized, 12–1024 UTF-8 bytes, never truncated. */
export const PASSWORD_MIN_BYTES = 12
export const PASSWORD_MAX_BYTES = 1024

export function passwordByteLength(password: string): number {
  return new TextEncoder().encode(password.normalize('NFKC')).length
}

/** An error message, or null when the password satisfies the length rule. */
export function validatePasswordLength(password: string): string | null {
  const n = passwordByteLength(password)
  if (n < PASSWORD_MIN_BYTES) return `Use at least ${PASSWORD_MIN_BYTES} characters.`
  if (n > PASSWORD_MAX_BYTES) return `Use at most ${PASSWORD_MAX_BYTES} bytes; longer passwords are rejected, not truncated.`
  return null
}

/** Validates a new password and its confirmation. */
export function newPasswordError(password: string, confirm: string): string | null {
  return validatePasswordLength(password) ?? (password !== confirm ? 'The passwords do not match.' : null)
}
