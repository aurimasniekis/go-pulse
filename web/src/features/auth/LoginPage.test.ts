import type { Me } from '@/api/client'
import { resumeStep } from './LoginPage'

const me = (over: Partial<Me> & { mfa_enrolled?: boolean } = {}): Me =>
  ({
    auth_method: 'password', mfa_pending: false, must_enroll: false, permissions: [], session_id: 's', listener: 'loopback',
    auth_at: '', expires_at: '', reauth_window_seconds: 300, recovery_codes_left: 0,
    user: { id: 'u', display_name: 'U', enabled: true, has_password: true, mfa_enrolled: over.mfa_enrolled ?? false, mfa_required: false,
      provisioned_via: 'admin', role_assignments: [], roles: [], version: 1, created_at: '' },
    ...over,
  }) as Me

test('a limited session resumes at the right step', () => {
  expect(resumeStep(null, undefined)).toBeNull()
  expect(resumeStep(me({ must_enroll: true }), undefined)).toBe('enroll')
  expect(resumeStep(me({ mfa_pending: true }), undefined)).toBe('mfa')
  expect(resumeStep(me(), undefined)).toBe('done')
  expect(resumeStep(me(), 'forbidden')).toBeNull()
  expect(resumeStep(me({ auth_method: 'oidc', mfa_enrolled: true }), 'mfa_required')).toBe('stepup-totp')
  expect(resumeStep(me({ auth_method: 'oidc' }), 'mfa_required')).toBeNull()
})
