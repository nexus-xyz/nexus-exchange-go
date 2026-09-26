# Changelog

## 0.1.0 (2026-09-26)


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
