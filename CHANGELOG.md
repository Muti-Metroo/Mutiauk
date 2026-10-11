# Changelog

## [0.5.2](https://github.com/postalsys/Mutiauk/compare/mutiauk-v0.5.1...mutiauk-v0.5.2) (2026-10-11)


### Bug Fixes

* **api:** close active client connections in Stop ([5fd5034](https://github.com/postalsys/Mutiauk/commit/5fd5034481e0ade88729326562a1e887c0a12cd9))
* **api:** exit accept loop when the listener is closed ([42c4af2](https://github.com/postalsys/Mutiauk/commit/42c4af22832a9914f3cf47cd2d08d3c50329e301))
* **ci:** keep VERSION in sync with release-please releases ([c1c8c05](https://github.com/postalsys/Mutiauk/commit/c1c8c050d8f5612434ba716ee12df20589dbf634))
* **daemon:** stop mutating the live stored config in route persist paths ([ef80b30](https://github.com/postalsys/Mutiauk/commit/ef80b3000b018c2e774bb3dd50de02807eaa827d))
* **dns:** shut down listeners on cancelled or failed Listen startup ([79171ad](https://github.com/postalsys/Mutiauk/commit/79171adec59d00c66cd774b7738ba03ad834bca4))
* **expose:** only escalate reconnect backoff after failed sessions ([fba484b](https://github.com/postalsys/Mutiauk/commit/fba484b4064a8c815cff280ee10c5b9d7eacbea1))
* **expose:** snapshot config in the reconnect loop; reset backoff after clean sessions ([d326bdf](https://github.com/postalsys/Mutiauk/commit/d326bdfb298257aae8123bc03aa9d0b8bbd84475))

## [0.5.1](https://github.com/postalsys/Mutiauk/compare/mutiauk-v0.5.0...mutiauk-v0.5.1) (2026-10-10)


### Bug Fixes

* **deps:** patch golang.org/x/net vulnerability ([0869623](https://github.com/postalsys/Mutiauk/commit/0869623f7cda1ac4d251cffe1a1600841e2bd31d))

## [0.5.0](https://github.com/postalsys/Mutiauk/compare/mutiauk-v0.5.0...mutiauk-v0.5.0) (2026-10-10)


### Miscellaneous Chores

* re-release 0.5.0 after repository history migration ([8c32122](https://github.com/postalsys/Mutiauk/commit/8c321228980a59a9efcf3e6cd47d124d3f1cf7eb))

## [0.2.16](https://github.com/postalsys/Mutiauk/compare/mutiauk-v0.2.15...mutiauk-v0.2.16) (2026-02-26)


### Bug Fixes

* remove stale IsReply byte from ICMP wire format and simplify ([13852e7](https://github.com/postalsys/Mutiauk/commit/13852e7c9f20e0437823237d3b36ada69812f2e9))

## [0.2.15](https://github.com/postalsys/Mutiauk/compare/mutiauk-v0.2.14...mutiauk-v0.2.15) (2026-02-07)


### Bug Fixes

* resolve memory leaks, race conditions, and concurrency issues ([6211e42](https://github.com/postalsys/Mutiauk/commit/6211e4206367c1c77dd2b4a86da54d746d1f8414))

## [0.2.14](https://github.com/postalsys/Mutiauk/compare/mutiauk-v0.2.13...mutiauk-v0.2.14) (2026-01-16)


### Features

* add HTTP Basic Auth support for WebSocket transport ([2a25955](https://github.com/postalsys/Mutiauk/commit/2a25955bf334538123d2b998b1faad0b9c5557a4))

## [0.2.13](https://github.com/postalsys/Mutiauk/compare/mutiauk-v0.2.12...mutiauk-v0.2.13) (2026-01-16)


### Features

* add WebSocket transport for SOCKS5 connections ([9ebad6c](https://github.com/postalsys/Mutiauk/commit/9ebad6c8454ca5e0031fd7a07913a05047a823ab))

## [0.2.12](https://github.com/postalsys/Mutiauk/compare/mutiauk-v0.2.11...mutiauk-v0.2.12) (2026-01-13)


### Bug Fixes

* align ICMP relay wire format with Muti Metroo ([502a88f](https://github.com/postalsys/Mutiauk/commit/502a88f7fdbb2af50a707c54430c7728153dc718))

## [0.2.11](https://github.com/postalsys/Mutiauk/compare/mutiauk-v0.2.10...mutiauk-v0.2.11) (2026-01-13)


### Features

* add IPv6 ICMP echo support ([19203af](https://github.com/postalsys/Mutiauk/commit/19203afda3544ad08576c018cbdc8e6118b8c8be))
* add IPv6 UDP response support ([b0857b8](https://github.com/postalsys/Mutiauk/commit/b0857b86b4208091a763fa61abc1fd4c24e4d626))

## [0.2.10](https://github.com/postalsys/Mutiauk/compare/mutiauk-v0.2.9...mutiauk-v0.2.10) (2026-01-13)


### Features

* add ICMP echo forwarding through SOCKS5 proxy ([7fcca83](https://github.com/postalsys/Mutiauk/commit/7fcca837f2fc4aa23c4941ea64d73bf208d26482))

## [0.2.9](https://github.com/postalsys/Mutiauk/compare/mutiauk-v0.2.8...mutiauk-v0.2.9) (2026-01-13)


### Bug Fixes

* use IPv4 UDP socket for IPv4 relay addresses ([b0fb687](https://github.com/postalsys/Mutiauk/commit/b0fb687403b192eafb0149f24506595a4275bc02))

## [0.2.8](https://github.com/postalsys/Mutiauk/compare/mutiauk-v0.2.7...mutiauk-v0.2.8) (2026-01-10)


### Bug Fixes

* trigger release ([4b1fe8c](https://github.com/postalsys/Mutiauk/commit/4b1fe8c888eb492794646de4808ed30e6ee4906a))

## [0.2.7](https://github.com/postalsys/Mutiauk/compare/mutiauk-v0.2.6...mutiauk-v0.2.7) (2026-01-10)


### Features

* **wizard:** add autoroutes configuration option ([dbbf443](https://github.com/postalsys/Mutiauk/commit/dbbf443a114f2e1309cbb4082b5c142583d5f500))


### Bug Fixes

* docker-test environment configuration ([06f0c27](https://github.com/postalsys/Mutiauk/commit/06f0c2716da06170bd263c1e943302366e2d80b1))

## [0.2.6](https://github.com/postalsys/Mutiauk/compare/mutiauk-v0.2.5...mutiauk-v0.2.6) (2026-01-09)


### Features

* add Unix socket API for CLI-daemon communication ([c72ee67](https://github.com/postalsys/Mutiauk/commit/c72ee67b06627c2cd979977fdb2d779eb4edbd32))

## [0.2.5](https://github.com/postalsys/Mutiauk/compare/mutiauk-v0.2.4...mutiauk-v0.2.5) (2026-01-09)


### Features

* add route trace command for routing analysis ([1a5531a](https://github.com/postalsys/Mutiauk/commit/1a5531ae7bb4dfc3f71362efbdc3bc095eb34a09))

## [0.2.4](https://github.com/postalsys/Mutiauk/compare/mutiauk-v0.2.3...mutiauk-v0.2.4) (2026-01-08)


### Features

* add autoroutes feature for automatic route fetching from Muti Metroo API ([ee8a8cd](https://github.com/postalsys/Mutiauk/commit/ee8a8cd848394aaaa0c4d5e5bfba5bce682bfadf))

## [0.2.3](https://github.com/postalsys/Mutiauk/compare/mutiauk-v0.2.2...mutiauk-v0.2.3) (2026-01-08)


### Bug Fixes

* correct import path from coinstash to postalsys ([99fee24](https://github.com/postalsys/Mutiauk/commit/99fee245cffa1bc58cee25e387005b30b4f1439b))

## [0.2.2](https://github.com/postalsys/Mutiauk/compare/mutiauk-v0.2.1...mutiauk-v0.2.2) (2026-01-08)


### Bug Fixes

* trigger initial release build ([39776a7](https://github.com/postalsys/Mutiauk/commit/39776a7a3d003d54a613718773145bf2d10c6733))

## Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).
