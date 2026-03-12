# Tasks: expired-quote-cleanup

## Phase 1: Foundation
- [x] 1.1 Create `internal/cleanup/cleanup.go` — Ticker struct, New, Start, Stop — REQ-1, REQ-3, REQ-5

## Phase 2: Integration
- [x] 2.1 Wire ticker into `cmd/carrier-gateway/main.go` — read CLEANUP_INTERVAL, start after repo init, stop before shutdown — REQ-2, REQ-4

## Phase 3: Testing
- [x] 3.1 Create `internal/cleanup/ticker_test.go` — unit test with stub repo, verify calls and no goroutine leaks — REQ-1, REQ-3, REQ-5
