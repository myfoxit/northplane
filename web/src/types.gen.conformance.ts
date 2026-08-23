// Compile-time conformance guard: the curated, app-facing domain types in
// types.ts must stay structurally compatible with the types generated from the
// Go API's OpenAPI spec (types.gen.ts, produced by `make types`).
//
// Why a guard instead of aliasing types.ts straight to the generated types:
// the hand-written types are deliberately *narrower and leaner* than the raw
// wire DTOs. They use semantic unions (`Severity`, `AlertStatus`, `Kind`,
// `StateType`) and numeric state enums where openapi-typescript can only emit
// `string`/`number`, and they intentionally omit transport-only fields the UI
// never reads (`payload`, `eventIds`, `tenantId`, …). Aliasing would discard
// those unions and pull wire noise into ~40 consumers — real regressions. So
// types.ts stays curated and we *assert* it conforms here instead.
//
// What "conforms" means (and what drift it catches) — applied recursively,
// see ConformsDeep below:
//   1. Every field the frontend declares must exist on the generated DTO —
//      so if the backend renames/removes a field the UI reads, this fails.
//   2. Each shared field's frontend type must refine the generated type — so a
//      real type change (e.g. backend `version` string→number) fails, while an
//      intentional narrowing (`Severity` ⊂ `string`) is fine.
// The frontend may legally OMIT wire-only fields the UI never reads; that is
// not drift. Null/undefined are treated interchangeably (the frontend models
// some optionals as `T | null`; the generated DTO uses `T | undefined`).
//
// This module is part of the app `tsc -b` build (it is NOT a *.test.ts file,
// which the app build excludes and which Vitest does not type-check), so any
// drift surfaces as a `npm run build` / CI failure. types.gen.test.ts gives it
// a runtime touchpoint for the Vitest suite and coverage.

import type { components } from './types.gen'
import type {
  Alert,
  CheckState,
  Incident,
  NPObject,
  Overview,
  TTSProfile,
} from './types'

type Schemas = components['schemas']

// Drop null/undefined from a member so optional-vs-required and the
// frontend's `T | null` vs the wire's `T | undefined` never trip the check —
// presence/absence is not the drift we're guarding against; the value type is.
type Defined<T> = Exclude<T, null | undefined>
type Plain =
  | string | number | boolean | bigint | symbol
  | ((...a: never[]) => unknown)

// An index-signature record (`Record<string, V>`) — `string` is one of its
// keys. The frontend intentionally keeps a few fields opaque this way (e.g.
// `NPObject.spec: Record<string, unknown>`, narrowed locally in specUtil.ts)
// rather than mirror the fully-typed Go `ObjectSpec`. Such a field is a
// deliberate loosening, not drift, so we only require the wire side to be an
// object too — we don't force the opaque record to match every typed key.
type IsIndexRecord<T> = string extends keyof T ? true : false

// ConformsDeep<H, G> is `true` iff the hand-written H is a structurally
// compatible *refinement* of the generated G:
//   • objects → every key of H must exist on G, recursing into each value
//     (so H may legally OMIT wire-only fields of G — `Incident` has no
//     `tenantId` — but may not INVENT a field G lacks, nor change a shared
//     field's type);
//   • arrays  → recurse on the element type;
//   • leaves  → H assignable to G, so intentional narrowings pass
//     (`Severity` ⊂ `string`, the `state` number-enum ⊂ `number`).
// A real backend change (renamed/removed field the UI reads, or a flipped
// scalar type) makes the relevant member `false` → Expect<> fails to compile
// → `npm run build` / CI goes red.
type ConformsDeep<H, G> =
  [Defined<H>] extends [Plain]
    ? [Defined<H>] extends [Defined<G>] ? true : false
    : Defined<H> extends readonly (infer EH)[]
      ? Defined<G> extends readonly (infer EG)[] ? ConformsDeep<EH, EG> : false
      : IsIndexRecord<Defined<H>> extends true
        ? Defined<G> extends object ? true : false
        : Defined<G> extends object
          ? {
              [K in keyof Defined<H>]-?: K extends keyof Defined<G>
                ? ConformsDeep<Defined<H>[K], Defined<G>[K]>
                : false
            }[keyof Defined<H>] extends true
            ? true
            : false
          : false

type Expect<T extends true> = T

// Each member is `true` only if the curated type still conforms to the
// generated DTO. Exported so noUnusedLocals keeps it and tests can reach it.
//
// Drift surfaced + handled during integration:
//  - Alert/Incident/Object DTOs carry wire-only fields the UI omits
//    (payload, eventIds, tenantId, createdAt/updatedAt) — fine: conformance
//    only requires the frontend's fields to exist on the wire and match.
//  - NPObject is the decorated list/detail view, so it is asserted against the
//    generated `ObjectView` (which adds hostName + nested state), NOT the bare
//    `Object` schema (the POST create response).
//  - Overview comes from an anonymous Go struct → generated as schema
//    `overview` (lower-case). Its nested `openIncidents` is checked element-
//    wise against the generated `Incident`, and `summary` against
//    `StateSummary`, all under the same omit-wire-fields rule.
export type GeneratedConformance = [
  Expect<ConformsDeep<Alert, Schemas['Alert']>>,
  Expect<ConformsDeep<Incident, Schemas['Incident']>>,
  Expect<ConformsDeep<CheckState, Schemas['CheckState']>>,
  Expect<ConformsDeep<NPObject, Schemas['ObjectView']>>,
  Expect<ConformsDeep<Overview, Schemas['overview']>>,
  Expect<ConformsDeep<TTSProfile, Schemas['TTSProfile']>>,
]
