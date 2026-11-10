# Lighthouse

[![CI](https://github.com/nosaomorodion2017-glitch/lighthouse/actions/workflows/ci.yml/badge.svg)](https://github.com/nosaomorodion2017-glitch/lighthouse/actions/workflows/ci.yml)

**An internal account-health platform: one Go service, three data sources, one dashboard you can explain.**

Customer-success teams answer "which accounts need attention?" by checking a CRM,
a help desk and product analytics one at a time. Lighthouse joins them into a single view.
It gives each account an explainable health score, a ranked dashboard, and a one-click
LLM brief for the account owner.

> Built by Nosa Omorodion as a working sample for a Go / full-stack engineering role.
> Read [`docs/SPEC.md`](docs/SPEC.md) first. It has the problem, the requirements and the tests that prove each one.

## Run it

```bash
go run ./cmd/server
```

Open http://localhost:8080. It needs Go 1.23+ and has no third-party dependencies.

To turn on Claude-written briefs (without a key it uses rule-based briefs):

```bash
ANTHROPIC_API_KEY=sk-ant-... go run ./cmd/server
```

| Variable | Default | |
|---|---|---|
| `ADDR` / `PORT` | `:8080` | Listen address |
| `DATA_DIR` | `data` | Folder with the source exports |
| `REFRESH_INTERVAL` | `5m` | How often sources are re-read |
| `ANTHROPIC_API_KEY` | — | Enables Claude briefs |
| `LLM_MODEL` | `claude-sonnet-5-5` | Model for briefs |

## Test it

```bash
go test -race ./...
go vet ./...
```

## What's inside

```
cmd/server          entrypoint: config, wiring, graceful shutdown
internal/model      domain types (Account is the root; tickets and usage reference it)
internal/source     one adapter per upstream system: CRM JSON, help-desk CSV, analytics JSON
internal/aggregate  concurrent fetch: per-source timeout, panic recovery, integrity checks
internal/health     explainable 0–100 score; every deduction comes with a reason
internal/service    state, refresh loop, derived views (pure functions)
internal/insight    Claude brief → rule-based fallback
internal/obs        Prometheus metrics, no dependencies
internal/api        JSON API, middleware (request ID, logs, metrics, security headers)
web/static          dashboard: plain HTML/CSS/JS, embedded in the binary
docs/SPEC.md        spec, acceptance criteria, estimates, risks
```

## API

| Method | Path | |
|---|---|---|
| GET | `/api/summary` | Portfolio KPIs and weekly ticket trend |
| GET | `/api/accounts?label=&q=&sort=` | Ranked accounts. `label`: healthy, watch, at-risk. `sort`: health, mrr, renewal, name |
| GET | `/api/accounts/{id}` | One account with its tickets and weekly usage |
| POST | `/api/accounts/{id}/brief` | LLM brief (cached for each data version) |
| GET | `/api/sources` | Per-source status, latency, record count, integrity warnings |
| POST | `/api/refresh` | Re-read all sources now |
| GET | `/healthz` · `/readyz` · `/metrics` | Liveness, readiness (reports stale data), Prometheus |

Errors are always `{"error": {"code": "...", "message": "..."}}`.

## Design choices

- **Partial failure is visible.** If one source is down, the others still load. The dashboard
  shows a banner, and `/api/sources` says what failed and why. If *every* source fails, the
  last good data is kept and `/readyz` reports `stale: true`.
- **No silent data loss.** A ticket that points at an unknown account is dropped *and* reported.
  A malformed CSV row fails the import with its line number, because quietly skipping tickets
  would make accounts look healthier than they are.
- **The score is explainable.** A CS lead can check any score by hand. The weights are in one
  function (`internal/health`), are covered by table tests, and are written down in the spec.
- **The LLM only sees facts.** The prompt is a compact JSON of platform data, and the system
  prompt forbids inventing anything. If the LLM is off or failing, the feature still works.
- **Standard library only.** It's easy to audit, there are no dependencies to update, and it
  builds as one static binary.

## Deploy

```bash
docker build -t lighthouse .
docker run -p 8080:8080 lighthouse
```

Any container host will run this image as is (Render, Fly.io, Railway, Cloud Run).
