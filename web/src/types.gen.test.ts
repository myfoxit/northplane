import { describe, it, expect, expectTypeOf } from 'vitest'
import type { GeneratedConformance } from './types.gen.conformance'
import type { components } from './types.gen'

// The real enforcement is compile-time: types.gen.conformance.ts is part of the
// app `tsc -b` build, so if a curated type in types.ts drifts from the Go API's
// generated DTO, `npm run build` / CI fails before this test ever runs (Vitest
// transpiles via esbuild and does NOT type-check). This suite is the runtime
// touchpoint: it keeps the guard wired into the test graph + coverage and
// documents the contract. `make types-check` is the separate CI gate that
// fails if web/src/types.gen.ts itself wasn't regenerated after a Go change.

describe('generated-type conformance', () => {
  it('asserts the curated domain types conform to the generated OpenAPI DTOs', () => {
    // GeneratedConformance is [true, true, true, true, true] only if every
    // Expect<ConformsDeep<…>> held at compile time (Alert, Incident,
    // CheckState, NPObject↦ObjectView, Overview↦overview).
    expectTypeOf<GeneratedConformance>().toEqualTypeOf<
      [true, true, true, true, true]
    >()
  })

  it('exposes the generated schemas under components.schemas', () => {
    // Sanity: the codegen surface the conformance guard depends on exists.
    expectTypeOf<components['schemas']['Alert']>().toBeObject()
    expectTypeOf<components['schemas']['ObjectView']>().toBeObject()
    expect(true).toBe(true)
  })
})
