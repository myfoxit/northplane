import { describe, it, expect } from 'vitest'
import { redactSecrets, isSecretName, REDACTED } from './redact'

describe('isSecretName', () => {
  it('matches secret flags/keys in any casing or separator', () => {
    for (const n of ['--token', 'token', 'API_KEY', '--api-key', 'apiKey', 'password', 'clientSecret', 'db_passwd']) {
      expect(isSecretName(n)).toBe(true)
    }
  })
  it('does not match ops names that merely contain "key"', () => {
    for (const n of ['_HOSTKEY', 'serviceKey', 'hostname', '--port', 'interval']) {
      expect(isSecretName(n)).toBe(false)
    }
  })
})

describe('redactSecrets', () => {
  it('masks the value after a bare --token flag', () => {
    expect(redactSecrets({ args: ['--token', 'nlagent-64252e6cd4c9', '--port', '5432'] }))
      .toEqual({ args: ['--token', REDACTED, '--port', '5432'] })
  })
  it('masks the value in --token=value form, keeping the flag', () => {
    expect(redactSecrets({ args: ['--token=nlagent-abc', '--host=db01'] }))
      .toEqual({ args: [`--token=${REDACTED}`, '--host=db01'] })
  })
  it('masks object values under secret keys', () => {
    expect(redactSecrets({ password: 'hunter2', vars: { apiKey: 'x', region: 'eu' } }))
      .toEqual({ password: REDACTED, vars: { apiKey: REDACTED, region: 'eu' } })
  })
  it('leaves non-secret data structurally unchanged', () => {
    const spec = { address: '10.0.0.1', args: ['--port', '5432'], interval: '60s' }
    expect(redactSecrets(spec)).toEqual(spec)
  })
})
