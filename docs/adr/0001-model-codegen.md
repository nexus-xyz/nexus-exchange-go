# ADR 0001: Generate the wire models with oapi-codegen

- Status: accepted
- Date: 2026-09-24
- Ticket: ENG-16558 (epic ENG-16552)

## Context

The Interfaces roadmap's C2 bar (no hand-maintained copy of anything the
contract can generate) means the model structs are generated, into
`internal/models`, with hand-written client methods over them. What was open
was which generator, and whether it can get these right:

1. **Per-field casing (R2.8).** Requests are snake_case. CCXT-parity responses
   mix CCXT camelCase and Nexus snake_case on one object. JSON tags must come
   from the spec's property names, field by field, never from a naming strategy.
2. **Money is a decimal string (R2.9).** Every `$ref: Decimal` becomes our
   `Decimal`, and no `float64` enters a wire type (the `TestNoFloatsInWireTypes`
   test enforces that over this package).
3. **Open enums (R3.3).** An enum value the pinned spec does not know decodes
   without error and can be read back.
4. **Nullable is not absent.** `["number","null"]` and `oneOf [T, null]` keep
   an explicit `null` apart from a missing key.

### Prior art: ENG-3451

ENG-3451 prototyped a generated Rust SDK with progenitor (nexus#1796). The
prototype was later removed from the monorepo in favour of the public
`nexus-exchange-rs` (nexus#2351), whose types today are hand-written. What
carries over is its type-mapping rule: use an idiomatic type only when it
round-trips the wire value losslessly. So `Decimal` is never a float, enums
are open, and unknown fields are never rejected. We keep that rule. We do not
follow its choice of `f64` for CCXT `number` fields: we map those to
`json.Number`, which is lossless and keeps float64 out of the wire types.

## Options

Tried against the pinned spec (v0.8.1, OpenAPI 3.1) on 2026-09-24.

| Requirement | oapi-codegen v2.8.0 | ogen (latest) | In-repo generator |
| --- | --- | --- | --- |
| Tags from property names | yes | yes (jx codecs keyed by name) | would write it |
| `Decimal`, no floats | `exclude-schemas` + `type-mapping` (config only) | `number` is `float64`, no general type mapping | would write it |
| Open enums | named `string` types, plus `Valid()` | decoder accepts, but response validation rejects unknown values | would write it |
| Null vs absent | `nullable.Nullable[T]` for 3.1 type arrays and `oneOf` with null | `OptNil*` types | would write it |
| Filter to v1 operations | `include-operation-ids`, unreachable schemas pruned | path regex | would write it |
| Output for the v1 subset | about 1,300 lines, models only | about 9,000 lines (client and codecs come with it) | smaller |
| Runtime deps | `oapi-codegen/nullable`, `oapi-codegen/runtime` | `jx`, `go-faster/errors`, ogen runtime, `uuid` | none |

## Decision

Use **oapi-codegen**, models only, configured in
`internal/models/oapi-codegen.yaml`:

- `include-operation-ids` lists the v1 operations. Schemas they do not reach
  are pruned.
- `exclude-schemas: [Decimal]`, so references resolve to the hand-written
  `Decimal` in `internal/models/decimal.go`. The root package aliases it
  (`nexus.Decimal`), because declaring it at the root would make
  root -> models -> root an import cycle.
- `type-mapping`: `number` is `json.Number`; `uuid` and `date-time` stay
  strings, as served.
- `nullable-type: true`, and `always-prefix-enum-values` so a new enum
  elsewhere cannot rename an existing constant.

It meets all four requirements with configuration and no spec edits or
post-processing. A generator of our own would avoid the two runtime
dependencies, but it would be a few hundred lines we must maintain for every
new schema construct the spec adopts. That is more moving parts than two
small, pinned modules.

Regeneration is `go generate ./...`. It fetches `openapi.json` from the
`nexus-exchange-api` release named in `.api-version` and runs the generator,
which is pinned by a `tool` directive in `tools.mod`. That is a separate
modfile, so the generator's dependencies stay out of the SDK's `go.mod`. CI's
`generate` job regenerates and fails on any diff.

## Consequences

- Generated Go is not idiomatic: optional fields are pointers, and unions are
  opaque with `As*` and `ValueByDiscriminator` accessors. That stays inside
  `internal/`. What the root package exposes is a deliberate choice for the
  client modules.
- `nullable.Nullable` is a third-party type. If a public type needs one, wrap
  it rather than aliasing it, for the same reason `Decimal` is ours.
- **The pinned spec lags R2.8.** At v0.8.1, `POST /orders` returns the
  snake_case `{order, fills}` shape, not the mixed-case CCXT order. The
  mixed-case case is covered by `Trade` (`takerOrMaker` next to
  `is_liquidation`). Run against the monorepo spec (0.9.83), the same config
  generates the mixed-case `Order` (`clientOrderId` next to
  `fill_totals_error`) correctly, so bumping the pin needs no config change.
- **Not in v0.8.1:** `setLeverage`, `/metadata` and the four `/stats/*`
  statistics operations. They get models when the pin moves past them.
- **WebSocket frames have no schemas.** The `op` envelope is described in
  prose, even at 0.9.83. `LiquidationEvent` is a schema, but no operation
  references it, so pruning drops it. The fix belongs in the spec: frame
  schemas referenced from `/ws`. Until then, module 9 must hand-write the
  envelope.
