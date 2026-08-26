# Urban Sports Safety Hub

Urban Sports Safety Hub is a production-oriented Go backend for youth climbing, skateboarding, and flying-disc instruction. It coordinates guardian consent, learner prerequisites, coach qualifications, course capacity, training groups, assessments, certifications, and the safety lifecycle of fitted equipment.

The service is intentionally a backend operational system rather than an inventory product. Equipment records participate in student safety and course eligibility workflows; they are not a general warehouse or commerce catalog.

## Architecture

- `cmd/server`: HTTP process lifecycle, dependency wiring, signals, and graceful shutdown.
- `internal/domain`: roles, business records, error taxonomy, and lifecycle transition rules.
- `internal/auth`: password login and opaque, expiring, server-side sessions with logout revocation.
- `internal/enrollment`: minor consent, course prerequisites, qualification windows, capacity reservation, cancellation, and make-up entitlement transactions.
- `internal/equipment`: inspection freshness, student fit, exclusive checkout, return classification, isolation, responsibility, and maintenance transactions.
- `internal/assessment`: qualified examiner publication and atomic certification grants.
- `internal/training`: capacity-controlled training group assignment.
- `internal/storage/sqlite`: real SQL repositories, versioned migrations, optimistic updates, audit records, persistent jobs, and restart recovery.
- `internal/worker`: cancellable durable-job claiming, bounded retries, backoff, and interrupted-job recovery.
- `internal/httpapi`: typed JSON handlers, stable error envelopes, request IDs, panic recovery, and health/readiness probes.

Production dependencies point inward: HTTP and workers call services, services enforce domain invariants and transactions, and the SQLite adapter owns SQL. Core workflows do not use in-memory maps as persistence.

## Database

SQLite is configured with foreign keys, WAL, a busy timeout, and bounded connection pooling. Embedded migrations are applied in filename order and recorded in `schema_migrations`. Reapplying migrations is safe; a migration and its ledger entry commit together.

The schema includes accounts and sessions; guardians, students, and consent; coaches and qualifications; course templates, prerequisites, sessions, enrollments, groups, and members; assessments and certifications; equipment, inspections, loans, and maintenance; make-up entitlements; idempotency records; audit events; and worker jobs.

Important write paths use database transactions and conditional version updates:

- Enrollment reserves capacity only after consent, prerequisite, age, course, and coach checks.
- Cancellation changes enrollments and creates one make-up entitlement per eligible enrollment.
- Checkout acquires exclusive equipment custody only after enrollment, sport, fit, and inspection checks.
- A damaged return closes custody, isolates the asset, opens a maintenance case, and records responsibility.
- Assessment publication validates the examiner and grants certification in the same transaction.
- Training group assignment checks ownership and capacity before inserting membership.

Audit events retain actor, request ID, object, action, result, and structured detail. Privileged and asset mutations write their audit record in the business transaction.

## Authentication

Supported roles are `guardian`, `coach`, `equipment_manager`, and `administrator`. Passwords are bcrypt hashes. Login returns a random opaque token; only its SHA-256 hash is stored. Sessions expire at the server, are invalidated when an account is disabled, and are revoked immediately by logout.

Set both bootstrap variables to create an initial administrator on an empty or existing database:

```sh
export BOOTSTRAP_ADMIN_EMAIL=admin@example.test
export BOOTSTRAP_ADMIN_PASSWORD='choose-a-long-password'
```

Omitting both variables disables administrator bootstrapping. Supplying only one fails startup.

## HTTP API

All responses include `X-Request-ID`. Callers may supply a bounded request ID or let the service generate one. Errors use:

```json
{"error":{"code":"stable_code","message":"safe message","request_id":"..."}}
```

Public endpoints:

- `GET /healthz`: process liveness.
- `GET /readyz`: database-backed readiness.
- `POST /api/login`: create an expiring session.

Authenticated endpoints use `Authorization: Bearer <token>`:

- `POST /api/logout`
- `POST /api/enrollments`
- `POST /api/course-sessions/{id}/cancel`
- `POST /api/equipment/checkout`
- `POST /api/equipment/return`

Request bodies are limited to 1 MiB, unknown fields are rejected, and exactly one JSON value is accepted.

## Local Development

The module declares Go 1.26. Configuration is read from environment variables documented in `.env.example`.

```sh
go mod download
go run ./cmd/server
```

By default the server listens on `:8080` and writes `.data/sports.db`. A clean shutdown waits for HTTP requests and the worker to stop. Persistent jobs left in `running` state beyond the stale threshold are returned to retry after restart.

Run all quality gates:

```sh
go test ./... -count=1
go test -race ./... -count=1
go vet ./...
go build ./...
```

## Docker

The root `Dockerfile` builds the actual `./cmd/server` package with Go 1.26.1 and copies the target-architecture binary into a non-root distroless image. The default entrypoint is `/app/sports-hub`, listens on port 8080, and stores its SQLite database under `/tmp`.

```sh
docker build --platform linux/amd64 -t urban-sports-safety-hub:amd64 .
docker build --platform linux/arm64 -t urban-sports-safety-hub:arm64 .
docker run --rm -p 8080:8080 urban-sports-safety-hub:amd64
curl --fail http://127.0.0.1:8080/healthz
curl --fail http://127.0.0.1:8080/readyz
```

No credentials, local database, build output, authoring checkpoint, or task-specific material belongs in Git.
