# Tasks: db-close-on-shutdown

## Phase 1: Core
- [x] 1.1 Hoist `db` to function scope and add `db.Close()` in shutdown section of `cmd/carrier-gateway/main.go` — REQ-1, REQ-2, REQ-3
