# Lighthouse — Account Health Platform

**Status:** Delivered (v1) · **Author:** Nosa Omorodion · **Reviewers:** —

## 1. Problem

Customer-success leads answer "which accounts need attention this week?" by opening
three tools: the CRM (plan, revenue, renewal date), the support desk (tickets) and
product analytics (weekly active users). It takes ~30 minutes per account review,
the answer differs depending on who does it, and nobody sees a renewal at risk until
it is too late.

## 2. Users

| User | Need |
|------|------|
| CS lead (non-technical) | One ranked list of accounts with a clear, explainable health score |
| Account owner | A one-minute brief before a customer call |
| Engineering | A service that is easy to extend with new data sources and easy to operate |

## 3. Goals and non-goals

**Goals**
- G1. Combine CRM, support and usage data into one account model.
- G2. A health score (0–100) that is **explainable**: every point lost has a written reason.
- G3. A dashboard a non-technical user can read without training.
- G4. An LLM-generated account brief that only uses facts from the platform.
- G5. Operable: health checks, metrics, structured logs, graceful degradation.

**Non-goals (v1)**
- Writing back to source systems.
- Authentication/SSO (it sits behind the company's existing access proxy).
- Historical storage — v1 reads current state from sources on a schedule.

## 4. Requirements and acceptance criteria

| ID | Requirement | Acceptance criteria | Test |
|----|-------------|---------------------|------|
| R1 | Read from N independent sources concurrently | One slow or failing source does not block the others; each source has a timeout | `aggregate_test.go` |
| R2 | Partial failure is visible, not silent | `/api/sources` shows per-source status, latency, record count, error | `aggregate_test.go`, `api_test.go` |
| R3 | Data integrity | Records pointing at unknown accounts are dropped **and** reported as warnings | `aggregate_test.go` |
| R4 | Explainable health score | Same input → same score; each deduction adds a signal string | `health_test.go` |
| R5 | Ranked account list with filter/search/sort | `GET /api/accounts?label=&q=&sort=`; bad params → 400 with a JSON error | `api_test.go` |
| R6 | Weekly ticket trend for dashboard | Last 8 ISO weeks (Mon–Sun, UTC), opened vs closed | `views_test.go` |
| R7 | AI brief | Uses Claude when configured; falls back to rule-based brief on error or no key; response says which was used | `insight_test.go` |
| R8 | LLM cost control | Brief cached per account per data refresh | `api_test.go` |
| R9 | Observability | `/healthz`, `/readyz`, Prometheus `/metrics` (per-route counts and latency), JSON logs with request ID | `api_test.go` |
| R10 | Stale data beats no data | If every source fails on refresh, the previous snapshot is kept and readiness reports it | `service_test.go` |

## 5. Design

```
            ┌──────────┐  ┌────────────┐  ┌───────────┐
            │ CRM JSON │  │ Support CSV│  │ Usage JSON│   ← Source interface
            └────┬─────┘  └─────┬──────┘  └─────┬─────┘
                 └──────── aggregate.Collect ───┘          concurrent, timeout, panic-safe
                               │
                        model.Snapshot (+ statuses, warnings)
                               │
                    service.BuildViews / Summary            pure functions, fully tested
                               │
                 ┌─────────────┴──────────────┐
             JSON API (net/http)          insight.Briefer   Claude → rules fallback
                 │
          Dashboard (static, embedded in the binary)
```

**Data model.** `Account` (CRM) is the root. `Ticket` and `UsagePoint` reference it by
`account_id`. Views (`AccountView`, `AccountDetail`, `Summary`) are derived, never stored.

**Adding a source** = implement `Name()` and `Fetch(ctx)` returning a partial snapshot.
No other code changes.

**Health score.** Start at 100, subtract:
- 6 per open ticket (max 30)
- 15 per open urgent ticket (max 30)
- 25 if active users fell ≥ 25% over 4 weeks, 10 if ≥ 10%; +5 if they grew ≥ 10%
- 10 if there is no usage data

Labels: `healthy` ≥ 75, `watch` ≥ 50, else `at-risk`. A renewal inside 45 days on a
non-healthy account adds a signal. The weights are deliberately simple so a CS lead can
check them by hand; they are one function and easy to tune.

**LLM brief.** The prompt contains only a compact JSON of platform facts, and the system
prompt forbids inventing facts. Output is 3 bullets + 1 next action. If no key is set
or the call fails, a deterministic rule-based brief is returned with a note.

## 6. Estimate

| Work item | Estimate |
|-----------|----------|
| Spec + data model | 0.5d |
| Sources + aggregator | 1d |
| Health score + views | 0.5d |
| API + observability | 1d |
| LLM brief + fallback | 0.5d |
| Dashboard UI | 1.5d |
| Tests + docs | 1d |
| **Total** | **6d** |

## 7. Risks and follow-ups

- **Score weights are opinion.** Next step: back-test against last year's churned accounts.
- **Sources are files in v1.** Swapping to real APIs only touches `internal/source`.
- **LLM output is not verified.** Next step: an eval set of 20 accounts with expected briefs.
