# Tasks: Plugin Runtime Migration

## Phase 1: Runtime Foundation

- [x] Define manifest schema, protocol envelopes, and capability constants
- [x] Implement subprocess supervisor and stdio transport
- [x] Implement bundled plugin catalog/discovery
- [x] Add protocol and supervisor tests

## Phase 2: Tool Plugin Migration

- [x] Add subprocess tool adapter to the runner/tool registry path
- [x] Migrate `read` to a bundled plugin
- [x] Migrate `bash` to a bundled plugin
- [x] Migrate `edit` to a bundled plugin
- [x] Migrate `write` to a bundled plugin
- [x] Migrate `webfetch` to a bundled plugin
- [x] Add tool integration coverage

## Phase 3: Channel Plugin Migration

- [ ] Define channel plugin contract and host adapter
- [ ] Replace hard-coded channel loading with catalog-driven startup
- [ ] Migrate Telegram channel plugin
- [ ] Migrate QQ channel plugin
- [ ] Migrate Feishu channel plugin
- [ ] Migrate Weixin channel plugin
- [ ] Add channel supervision and restart coverage

## Phase 4: Integration, Compatibility, and Docs

- [ ] Add plugin binding config for bundled tools/channels
- [ ] Preserve JS plugin compatibility
- [ ] Add plugin status/log visibility
- [ ] Document runtime, bundled plugin layout, and migration boundaries
- [ ] Run full test suite
