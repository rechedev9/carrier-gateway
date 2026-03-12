# Spec: db-lifecycle

## Requirements

### REQ-1: DB Closed on Shutdown
The `*sql.DB` connection MUST be closed during graceful shutdown.

**Scenario 1: Normal shutdown with DB**
- GIVEN a running server with `DATABASE_URL` set and a live DB connection
- WHEN SIGTERM is received
- THEN `db.Close()` MUST be called after the HTTP server has drained and the cleanup ticker has stopped

### REQ-2: Nil-Safe When No DB
The shutdown sequence MUST NOT panic when no DB connection exists.

**Scenario 2: Shutdown without DB**
- GIVEN a running server with `DATABASE_URL` unset (db is nil)
- WHEN SIGTERM is received
- THEN the shutdown sequence completes without error and `db.Close()` is NOT called

### REQ-3: Correct Shutdown Order
DB close MUST happen after all DB-dependent components have stopped.

**Scenario 3: Order enforcement**
- GIVEN a running server with DB, cleanup ticker, and HTTP server all active
- WHEN SIGTERM is received
- THEN shutdown order MUST be: cleanup ticker stop → HTTP server drain → DB close
