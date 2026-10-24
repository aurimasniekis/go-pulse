import { newPasswordError, validatePasswordLength } from './password'

test('password length counts NFKC bytes', () => {
  expect(validatePasswordLength('short')).not.toBeNull()
  expect(validatePasswordLength('correct horse')).toBeNull()
  expect(validatePasswordLength('ü'.repeat(6))).toBeNull() // 12 bytes
  expect(newPasswordError('correct horse battery', 'correct horse')).toBe('The passwords do not match.')
})
