import { describe, it, expect } from 'vitest'
import { permImplies, hasPermission } from './permissions'

// Mirrors the backend authority matrix (internal/auth/principal_test.go
// TestPrincipalAllowMatrix) so the UI gate can never drift from the server.
describe('permImplies', () => {
  const cases: Array<[string, string, boolean]> = [
    ['*:*', 'anything:goes', true], // super-admin grants everything
    ['*', 'admin:tenants', true], // bare star grants everything
    ['admin:tenants', 'admin:tenants', true], // exact
    ['admin:*', 'admin:tenants', true], // resource wildcard
    ['admin:*', 'admin:users', true],
    ['admin:*', 'objects:read', false], // wrong resource
    ['*:read', 'objects:read', true], // action wildcard
    ['*:read', 'objects:write', false], // wrong action
    ['objects:read', 'admin:tenants', false], // unrelated
    ['admin:tenants', 'admin:users', false], // same resource, diff action
    ['admin', 'admin:tenants', false], // unqualified perm implies nothing
  ]
  for (const [have, want, expected] of cases) {
    it(`${have} implies ${want} = ${expected}`, () => {
      expect(permImplies(have, want)).toBe(expected)
    })
  }
})

describe('hasPermission', () => {
  it('grants the super-admin ("*:*") the customer switcher', () => {
    // The exact regression this fixes: admin role holds "*:*".
    expect(hasPermission(['*:*'], 'admin:tenants')).toBe(true)
  })
  it('grants a literal admin:tenants holder', () => {
    expect(hasPermission(['objects:read', 'admin:tenants'], 'admin:tenants')).toBe(true)
  })
  it('denies a plain operator', () => {
    expect(hasPermission(['objects:read', 'objects:write'], 'admin:tenants')).toBe(false)
  })
  it('handles null/undefined permission lists', () => {
    expect(hasPermission(null, 'admin:tenants')).toBe(false)
    expect(hasPermission(undefined, 'admin:tenants')).toBe(false)
  })
})
