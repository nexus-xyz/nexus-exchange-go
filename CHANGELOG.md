# Changelog

## [0.3.0](https://github.com/nexus-xyz/nexus-exchange-go/compare/v0.2.0...v0.3.0) (2026-10-09)


### ⚠ BREAKING CHANGES

* **account:** AccountFees.MakerFeeBps and AccountFees.TakerFeeBps are now json.Number, not int, and can be fractional ("2.8"). Migration: read the served digits with .String(), use nexus.ParseDecimal(f.TakerFeeBps.String()) for exact arithmetic, or .Float64() for display only. .Int64() fails on a fractional rate.

### Bug Fixes

* **account:** decode fractional fee rates (ENG-21111) ([912520c](https://github.com/nexus-xyz/nexus-exchange-go/commit/912520cd1ffd11fc78ffe4f021f31d457e0d3329))

## [0.2.0](https://github.com/nexus-xyz/nexus-exchange-go/compare/v0.1.1...v0.2.0) (2026-10-08)


### ⚠ BREAKING CHANGES

* **agents:** Client.RevokeAgent(ctx, address) is now Client.RevokeAgent(ctx, wallet, address). The server accepts only the owner wallet's signature on DELETE /agents/{address}; the API key or session call the old method made is refused with 401 WALLET_SIGNATURE_REQUIRED.

### Features

* **agents:** revoke agents with the wallet signature (ENG-20579) ([#27](https://github.com/nexus-xyz/nexus-exchange-go/issues/27)) ([ef347e2](https://github.com/nexus-xyz/nexus-exchange-go/commit/ef347e2eb28cfe3e453ddf9f5a0e2791568d3599))
* **market:** RoundPrice and RoundSize tick/lot helpers, matching the Rust SDK (ENG-20360) ([#23](https://github.com/nexus-xyz/nexus-exchange-go/issues/23)) ([edcacd0](https://github.com/nexus-xyz/nexus-exchange-go/commit/edcacd03d0e36f0d2a767f3d1e777e4179ccd5d5))
* **ws:** detect a silent Subscription connection with pings (ENG-20363) ([#24](https://github.com/nexus-xyz/nexus-exchange-go/issues/24)) ([1c8005f](https://github.com/nexus-xyz/nexus-exchange-go/commit/1c8005f94d8da5b35c48ac497bc8080dc6c44b98))

## [0.1.1](https://github.com/nexus-xyz/nexus-exchange-go/compare/v0.1.0...v0.1.1) (2026-10-05)


### Bug Fixes

* **models:** decode served market rows (id/base/quote) (ENG-19679) ([#20](https://github.com/nexus-xyz/nexus-exchange-go/issues/20)) ([4452590](https://github.com/nexus-xyz/nexus-exchange-go/commit/4452590f3867dee96105a3f0b11b22bbaf39c24c))

## 0.1.0 (2026-09-29)


### ⚠ BREAKING CHANGES

* rename client methods to the R2.25 canonical names (ENG-17795) ([#15](https://github.com/nexus-xyz/nexus-exchange-go/issues/15))

### Features

* add Decimal money type and the epoch-ms time convention (ENG-16557) ([#3](https://github.com/nexus-xyz/nexus-exchange-go/issues/3)) ([16f9d0c](https://github.com/nexus-xyz/nexus-exchange-go/commit/16f9d0c4c1b7b5b32f9a46efbae9133626f7e39b))
* authenticated REST for orders, account and positions (ENG-16560) ([#9](https://github.com/nexus-xyz/nexus-exchange-go/issues/9)) ([ef4c7e7](https://github.com/nexus-xyz/nexus-exchange-go/commit/ef4c7e772b1deb867229353836af2b8773a3096f))
* generate the v1 wire models with oapi-codegen (ENG-16558) ([#6](https://github.com/nexus-xyz/nexus-exchange-go/issues/6)) ([99296b3](https://github.com/nexus-xyz/nexus-exchange-go/commit/99296b3872ac84bbc3bb18469572a146fcb644f0))
* HMAC request signing with a hex-only, unprintable APISecret (ENG-16555) ([#5](https://github.com/nexus-xyz/nexus-exchange-go/issues/5)) ([0cf9306](https://github.com/nexus-xyz/nexus-exchange-go/commit/0cf9306e4a43bc39f7032a9d4c1d8eb1b0b72226))
* pace on spec weights, retry GET 429 after retry-after (ENG-16562) ([#11](https://github.com/nexus-xyz/nexus-exchange-go/issues/11)) ([0e7223d](https://github.com/nexus-xyz/nexus-exchange-go/commit/0e7223d46297ff1521a2c8bc628d8646c3d1bd97))
* parity with the fleet, plus endpoints.txt and a spec-drift gate (ENG-17796) ([#18](https://github.com/nexus-xyz/nexus-exchange-go/issues/18)) ([ed409e2](https://github.com/nexus-xyz/nexus-exchange-go/commit/ed409e2fc83a72b26e0f94655961fbd81caa2e4a))
* public market-data REST methods and one pagination iterator (ENG-16559) ([#10](https://github.com/nexus-xyz/nexus-exchange-go/issues/10)) ([4ffedbc](https://github.com/nexus-xyz/nexus-exchange-go/commit/4ffedbc681e75d8bf11e1d7b7ad6a55c54f47c27))
* rename client methods to the R2.25 canonical names (ENG-17795) ([#15](https://github.com/nexus-xyz/nexus-exchange-go/issues/15)) ([71cd41a](https://github.com/nexus-xyz/nexus-exchange-go/commit/71cd41a1602f8ea75fd340782252252db07e3827))
* transport core, network axis, one mount and one error envelope (ENG-16554) ([#4](https://github.com/nexus-xyz/nexus-exchange-go/issues/4)) ([f56cb17](https://github.com/nexus-xyz/nexus-exchange-go/commit/f56cb1734fec41902ae48f13e50ee5b497040960))
* wallet sign-in, sessions and agent keys (ENG-16556) ([#13](https://github.com/nexus-xyz/nexus-exchange-go/issues/13)) ([b3a1b89](https://github.com/nexus-xyz/nexus-exchange-go/commit/b3a1b898a6048458b59aacbce69f4ffce6ec9d73))
* WebSocket clients for /stream and /ws with per-channel resume (ENG-16561) ([#12](https://github.com/nexus-xyz/nexus-exchange-go/issues/12)) ([479a0dd](https://github.com/nexus-xyz/nexus-exchange-go/commit/479a0ddb4724e233b3071e6e3b40531da396008c))


### Bug Fixes

* **release:** first release is v0.1.0, not v1.0.0 (ENG-16553) ([#8](https://github.com/nexus-xyz/nexus-exchange-go/issues/8)) ([8140452](https://github.com/nexus-xyz/nexus-exchange-go/commit/8140452c1a803694b9fd7376f0d27716878f539e))
* **test:** move the conformance lane to the R2.25 method names (ENG-16564) ([#17](https://github.com/nexus-xyz/nexus-exchange-go/issues/17)) ([ca83f51](https://github.com/nexus-xyz/nexus-exchange-go/commit/ca83f51df098b278fb8d687584615a64737bacf8))
