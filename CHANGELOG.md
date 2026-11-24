# Changelog

## [1.6.0](https://github.com/galeone/session-vault-plugin/compare/v1.5.0...v1.6.0) (2026-11-24)


### Features

* rotate-key command and key-loss documentation ([#19](https://github.com/galeone/session-vault-plugin/issues/19)) ([6c5b1bb](https://github.com/galeone/session-vault-plugin/commit/6c5b1bb548d946742d0d8eb4265d728bcf56e3c0))
* warn that restored sessions can contain secrets ([#17](https://github.com/galeone/session-vault-plugin/issues/17)) ([ef77250](https://github.com/galeone/session-vault-plugin/commit/ef77250c123cd4df979839ec4e680b59d111d33c))

## [1.5.0](https://github.com/galeone/session-vault-plugin/compare/v1.4.1...v1.5.0) (2026-11-22)


### Features

* get ready for Anthropic's plugin directory ([#15](https://github.com/galeone/session-vault-plugin/issues/15)) ([7251238](https://github.com/galeone/session-vault-plugin/commit/725123859921258f596638950435ca0b1e665f78))


### Bug Fixes

* record where the session started when the track hook never ran ([#13](https://github.com/galeone/session-vault-plugin/issues/13)) ([1f8a97a](https://github.com/galeone/session-vault-plugin/commit/1f8a97a943ebe635f78f16f2a74ab59598add6de))

## [1.4.1](https://github.com/galeone/session-vault-plugin/compare/v1.4.0...v1.4.1) (2026-11-20)


### Bug Fixes

* review findings — untrusted server metadata, session resolution, stale restores ([#11](https://github.com/galeone/session-vault-plugin/issues/11)) ([0a2e714](https://github.com/galeone/session-vault-plugin/commit/0a2e714d7fcdcca9071cd4ec311b903b64f8c02f))

## [1.4.0](https://github.com/galeone/session-vault-plugin/compare/v1.3.0...v1.4.0) (2026-11-18)


### Features

* migrate plugin to the current Claude Code format ([#9](https://github.com/galeone/session-vault-plugin/issues/9)) ([2e989f6](https://github.com/galeone/session-vault-plugin/commit/2e989f6e2e1058e720c444f502a1ae89749b63a3))

## [1.3.0](https://github.com/galeone/session-vault-plugin/compare/v1.2.0...v1.3.0) (2026-11-17)


### Features

* add /session-vault:list and /session-vault:clean commands ([e3aad6c](https://github.com/galeone/session-vault-plugin/commit/e3aad6c2c9ccae1da08a252e08ce0f5c8f03d658))
* add /session-vault:list and /session-vault:clean commands ([a71bd76](https://github.com/galeone/session-vault-plugin/commit/a71bd7617679277cda10f735e67e7d21f30054e7))

## [1.2.0](https://github.com/galeone/session-vault-plugin/compare/v1.1.1...v1.2.0) (2026-11-17)


### Features

* name unnamed saves after the session title Claude Code generates ([adf4ffd](https://github.com/galeone/session-vault-plugin/commit/adf4ffde2a36afacf9f88ceedd9aa4cb4eaa5846))

## [1.1.1](https://github.com/galeone/session-vault-plugin/compare/v1.1.0...v1.1.1) (2026-11-17)


### Bug Fixes

* stop gitignoring the cli source directory; test: check Close error ([f653692](https://github.com/galeone/session-vault-plugin/commit/f65369207eda38f89c671f3b296071f20fdba6db))

## [1.1.0](https://github.com/galeone/session-vault-plugin/compare/v1.0.0...v1.1.0) (2026-11-17)


### Features

* auto-generate the age key on first save ([82bda04](https://github.com/galeone/session-vault-plugin/commit/82bda044a69264ae61ed3464ef32f7973392e699))

## 1.0.0 (2026-11-15)


### Features

* add slog structured logging with daily file output ([81987ff](https://github.com/galeone/session-vault-plugin/commit/81987ffade2bfbeae44833f70610b96fa683610f))
* add tests, CI, linting and release-please workflows ([2ac0b6b](https://github.com/galeone/session-vault-plugin/commit/2ac0b6be4d428e6df09592c1552d7a2a57f85b7c))
