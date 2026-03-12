# Spec: cleanup-ticker

## Requirements

### REQ-1: Periodic Cleanup
The ticker MUST call `DeleteExpired` on the repository at a configurable interval.

**Scenario 1: Normal tick cycle**
- GIVEN a running ticker with a 1-second interval and a repository
- WHEN 2 seconds elapse
- THEN `DeleteExpired` MUST have been called at least once

### REQ-2: Configurable Interval
The ticker MUST accept an interval parameter. The composition root SHOULD read it from the `CLEANUP_INTERVAL` environment variable, defaulting to 5 minutes.

**Scenario 2: Custom interval**
- GIVEN `CLEANUP_INTERVAL=10m`
- WHEN the ticker starts
- THEN the internal tick period MUST be 10 minutes

### REQ-3: Graceful Stop
The ticker MUST stop without goroutine leaks when `Stop()` is called.

**Scenario 3: Clean shutdown**
- GIVEN a running ticker
- WHEN `Stop()` is called
- THEN the background goroutine MUST exit and no further `DeleteExpired` calls occur

### REQ-4: Nil-Safety
The composition root MUST NOT start the ticker when the repository is nil.

**Scenario 4: No repo configured**
- GIVEN `DATABASE_URL` is unset (repo is nil)
- WHEN the server starts
- THEN no cleanup ticker is created

### REQ-5: Error Resilience
The ticker MUST NOT crash or stop on `DeleteExpired` errors. It MUST log the error and continue to the next tick.

**Scenario 5: Transient DB error**
- GIVEN a running ticker
- WHEN `DeleteExpired` returns an error
- THEN the error is logged and the ticker continues running
