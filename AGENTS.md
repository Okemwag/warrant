# AGENTS.md

Instructions for anyone, human or coding agent, building Warrant. Read this before writing code.

- **What we're building and why:** [README.md](README.md)
- **How it fits together:** [Architecture.md](Architecture.md). If this file and Architecture.md disagree, Architecture.md wins. Fix whichever one is wrong in the same change.

---

## 1. The project in five lines

1. A person signs a **permission**: budget, per-purchase cap, approval threshold, categories, merchants, minimum return window.
2. An AI agent proposes a purchase. The **decision engine** returns `ALLOW`, `CONFIRM` or `BLOCK` with reason codes.
3. The **ledger** holds budget so two purchases at once can't overspend.
4. The **PayPal bridge** authorizes the payment, checks the amount, then captures it (PayPal sandbox only).
5. An **evidence pack** records the permission, decision and PayPal order, and can be sent to a PayPal dispute.

Deadline: submit by the evening of **Nov 11, 2026**. Submissions close Nov 12, 2026 at 11pm GMT+3.

---

## 2. Non-negotiable rules

Breaking any of these is a bug, even if tests pass. Each rule must have a test that would catch someone breaking it.

1. **The model never decides.** `engine/` must not import any LLM client, HTTP client, database driver or clock. Decisions are a pure function: `Decide(permission, ledgerSnapshot, proposal) -> Decision`.
2. **Untrusted text can only reduce autonomy.** Merchant text, web content, tool output and the agent's own arguments can add `CONFIRM` or `BLOCK` reasons. Nothing outside the signed permission can raise a limit or remove a reason.
3. **The agent never touches PayPal.** PayPal credentials, access tokens and `vault_id`s stay on the server. The agent-facing MCP server exposes exactly two tools: `evaluate` and `checkout`. Never add a tool that creates, captures or refunds a PayPal order directly.
4. **Never report success without PayPal confirming it.** Spend is committed only when PayPal reports the capture `COMPLETED`: in its own API response (the capture call or our `GET` of the capture) or in a webhook that PayPal verified. Unverified webhooks are never acted on.
5. **Uncertainty escalates.** A missing, malformed or unverifiable field becomes `CONFIRM` (or `BLOCK` if a hard limit can't be checked). Never `ALLOW` by default.
6. **No guarantees in copy.** User-facing text must not use "safe", "protected", "guaranteed", "insured" or "fraud-proof". Use "within your permission", "blocked", "asked you".
7. **Sandbox only.** The PayPal base URL is `https://api-m.sandbox.paypal.com`. The code must refuse to start if it is configured for the live URL.

---

## 3. Repository layout

```text
warrant/
├── engine/          # Pure Go decision library. No I/O. Open source.
│   ├── permission.go    # Permission type, canonical JSON, hashing
│   ├── proposal.go      # Normalized proposal type
│   ├── decide.go        # Decide(): rule order + combine
│   ├── reasons.go       # Reason code constants + outcome mapping
│   └── *_test.go
├── internal/config/  # .env loading
├── scripts/gates/   # Week-one gate scripts (gate 1: PayPal)
├── server/          # Warrant API (Go)
│   ├── cmd/warrant/     # main: serve, migrate, keygen
│   ├── api/             # HTTP handlers, bearer-token roles, request validation
│   ├── db/              # pgx pool, migration runner, testdb helper
│   ├── ids/             # prefixed random IDs (perm_, dec_, res_)
│   ├── ledger/          # Permissions, decisions, reservations, row locking, idempotency
│   ├── checkout/        # PayPal checkout: authorize, check amount, capture/void; vault; webhooks
│   ├── compiler/        # Claude: text -> draft permission (structured output)
│   ├── merchants/       # Merchant data Warrant trusts (return policies)
│   ├── signer/          # ed25519 signing and verification
│   ├── evidence/        # Evidence pack PDF + dispute attach (Phase 6)
│   └── migrations/      # SQL migrations, numbered
├── bridge/          # Agent-facing MCP server + PayPal REST client (Go)
│   ├── cmd/warrant-mcp/ # stdio MCP server binary for agents
│   ├── mcp/             # Tools: evaluate, checkout (exactly these two)
│   └── paypal/          # OAuth, orders, authorizations, refunds, vault, disputes, webhooks
├── console/         # React 18 + TypeScript + Vite. No UI or router library
│   └── src/             # api.ts, types.ts, format.ts, router.ts (hash routes), pages/, components/
├── gauntlet/        # Scenarios (YAML), runner, three arms, report
├── demo-store/      # Sandbox merchant catalog with structured return policies
├── agent/           # Reference shopping agent
├── docs/
├── docker-compose.yml
├── Makefile
└── .env.example
```

Don't create new top-level folders without updating this file and Architecture.md.

---

## 4. Tech stack and commands

| Layer | Choice |
|---|---|
| Engine, API, bridge | Go 1.25, standard library `net/http` routing, `pgx/v5` for Postgres. No web framework, no ORM |
| Database | Postgres 16 (Docker, host port 5433). Plain SQL migrations in `server/migrations/`, embedded and applied by `warrant migrate` |
| Console | React + TypeScript + Vite |
| LLM | Claude API via the official Go SDK (`anthropic-sdk-go`). Model set by `WARRANT_LLM_MODEL` (default `claude-opus-5-5`), low effort, structured outputs, server-side refusal fallback (`fallbacks: "default"`) |
| Console | React 18 + TypeScript + Vite, served by the Go server from `console/dist` |
| Payments | PayPal REST sandbox |
| Hosting | Render |

`Makefile` targets (`make help` lists them). Add new ones when their first code lands:

```bash
make db           # start Postgres (port 5433)
make dev          # Postgres + migrations + API on :8080
make keygen       # print a WARRANT_SIGNING_KEY
make migrate      # apply server/migrations
make test         # go test -race ./... (DB tests skip if Postgres is down)
make test-engine  # engine unit + property tests only
make test-race    # ledger concurrency tests against real Postgres
make lint         # gofmt, go vet, golangci-lint (includes staticcheck, errcheck)
make console      # build the Console into console/dist
make console-dev  # Console dev server on :5173 (proxies the API)
make mcp          # build bin/warrant-mcp
make build        # everything
make gate1        # PayPal sandbox authorize + capture (interactive)
make gate2        # untrusted text can't bypass a permission
make gate3        # concurrent purchases can't overspend
# later: make gauntlet, make agent, Console lint (eslint, tsc)
```

DB tests use `server/db/testdb`: each test package gets its own fresh schema (`test_<name>`) in `WARRANT_TEST_DATABASE_URL`, so packages can run in parallel.

Before saying any task is done, run `make lint test`. Report failures as they are; don't hide them.

---

## 5. Environment variables

Keep `.env.example` up to date with every variable. Never commit `.env`.

| Variable | Purpose |
|---|---|
| `DATABASE_URL` | Postgres connection |
| `WARRANT_TEST_DATABASE_URL` | Postgres for tests (`warrant_test` database) |
| `WARRANT_ADDR` | API listen address. Default `:8080` |
| `WARRANT_CONSOLE_TOKEN` | Bearer token for the person (Console). Must differ from the agent token |
| `WARRANT_AGENT_TOKEN` | Bearer token for agents: evaluate and read decisions only |
| `PAYPAL_BASE_URL` | Must be `https://api-m.sandbox.paypal.com` |
| `PAYPAL_CLIENT_ID`, `PAYPAL_CLIENT_SECRET` | Sandbox REST app |
| `PAYPAL_WEBHOOK_ID` | Used to verify webhook signatures |
| `PAYPAL_MODE` | `approve` (Mode A) or `vault` (Mode B) |
| `ANTHROPIC_API_KEY` | Plain-language compiler (optional; templates work without it) |
| `WARRANT_LLM_MODEL` | Claude model ID. Default `claude-opus-5-5` |
| `WARRANT_PUBLIC_URL` | This server's public URL; PayPal redirects buyers back here |
| `WARRANT_CONSOLE_URL` | Where PayPal return pages link back to. `/` when the server hosts the Console |
| `WARRANT_CONSOLE_DIR` | Built Console to serve at `/`. Default `console/dist` |
| `WARRANT_API_URL` | API base URL for `warrant-mcp` |
| `WARRANT_SIGNING_KEY` | ed25519 private key, base64 |
| `WARRANT_RESERVATION_TTL` | Default `15m` |
| `WARRANT_PRICE_TOLERANCE_CENTS` | Allowed gap between quote and authorized amount. Default `0` |

---

## 6. Coding conventions

### Money and time

- Money is **`int64` cents** everywhere in Go and in Postgres (`BIGINT`). Never use floats. Convert to PayPal's string `"18.00"` only at the PayPal client boundary, and back again the same way.
- Currency is `USD` only in the MVP. Anything else → `CURRENCY_MISMATCH`.
- The engine takes `now` as an argument. It never calls `time.Now()`.
- Store timestamps in UTC (`TIMESTAMPTZ`).

### Engine

- Reason codes are constants in `engine/reasons.go`, written exactly as in Architecture.md § 6. Adding a code means updating the table there, the shopper sentence, and the Console help text.
- Run every check and collect every reason, then combine: any `BLOCK` → `BLOCK`, else any `CONFIRM` → `CONFIRM`, else `ALLOW`.
- Return reasons in a stable order: the rule order in Architecture.md § 6.
- Canonical permission JSON: sorted keys, no extra whitespace, amounts in cents. Sign the hash of this exact byte string.

### Ledger

- Reserve, commit and release run inside a single transaction with `SELECT ... FOR UPDATE` on the permission's budget row.
- Available = monthly cap − committed − held. Never compute it outside the transaction.
- Every write endpoint takes an `Idempotency-Key`. A replay returns the original result and creates no second reservation.
- A background sweeper releases reservations past their TTL and voids any PayPal authorization they had.

### PayPal client (`bridge/paypal`)

- One small typed client written from the official OpenAPI specs (Orders v2, Payments v2, Payment Tokens v3, Webhooks v1, Disputes v1). Don't add a third-party PayPal SDK.
- Cache the OAuth token and refresh it before `expires_in` runs out.
- Send `PayPal-Request-Id` on every create, authorize and capture. Derive it from our `decision_id`, so retries can't create a second payment.
- Always set `purchase_units[0].custom_id = decision_id` and a unique `invoice_id`.
- Orders use `intent: AUTHORIZE`. Before capturing, compare the authorized amount with the decision's quoted amount. A mismatch beyond tolerance → void, then re-evaluate with `PRICE_DRIFT`.
- Verify every webhook with `POST /v1/notifications/verify-webhook-signature` before acting on it. Store the event ID and ignore duplicates.
- Log PayPal `debug_id` from error responses. Never log access tokens, client secrets or full `vault_id`s.

### LLM use

- The LLM is used in exactly one place: the compiler (`server/compiler`). Shopper sentences come from the engine's templates (`engine/explain.go`); an LLM rewording step was deliberately not built (it would add latency and a way for wording to drift from the decision).
- Compiler output is a *draft*. Validate it against the permission schema, show it to the human, and sign only what the human approved.
- Never put untrusted merchant or web text into the compiler prompt.

### Go style

- `gofmt`, `go vet`, `golangci-lint` clean.
- Errors are wrapped with context (`fmt.Errorf("capture %s: %w", id, err)`). No panics in request paths.
- Use table-driven tests. Test names describe the rule (`TestDecide_BlocksWhenOverPurchaseCap`).

### Console

- Follow the design principles in the product doc: limits readable in ten seconds, one-sentence reasons, pause and revoke reachable from every screen.
- Status is never shown by color alone. WCAG AA contrast. Full keyboard use.
- A "Sandbox, no real money" banner is on every page.
- PayPal's approval page must look clearly different from Warrant's own screens.
- Show amounts the same way everywhere: `$48.00`, merchant name first.

---

## 7. Tests that must exist

| Area | Test |
|---|---|
| Engine | One test per reason code, at the boundary (exactly at cap, one cent over). |
| Engine | Property test: no proposal whose amount exceeds the per-purchase cap ever returns `ALLOW`. |
| Engine | Untrusted `instruction_source` never produces `ALLOW`. |
| Engine | Changing the permission bytes after signing → `PERMISSION_INACTIVE`. |
| Ledger | 50 concurrent evaluations against one permission never push committed + held past the monthly cap (real Postgres, `-race`). |
| Ledger | Idempotent replay returns the same `decision_id` and no second reservation. |
| Ledger | Pause or revoke releases every open reservation. |
| Bridge | Price drift between quote and authorized amount → void + `PRICE_DRIFT`, never capture. |
| Bridge | Unverified webhook is rejected. Duplicate webhook is ignored. |
| Bridge | Startup fails if `PAYPAL_BASE_URL` is the live URL. |
| MCP | The tool list is exactly `evaluate` and `checkout`. |
| Gauntlet | Every scenario that has failed becomes a regression test in `gauntlet/scenarios/regressions/`. |

PayPal sandbox tests are tagged `//go:build sandbox` and run with `make test-sandbox`. Default `make test` uses recorded fixtures, so it works offline.

---

## 8. Week-one gates (Oct 21–23)

Do these first. Each one is a small script in `scripts/gates/` that prints PASS or FAIL.

1. **Payment path.** Mode A: create order → buyer approves → authorize → capture in sandbox. Mode B: save a PayPal account (Vault v3 setup token → payment token) → create order with `vault_id` and `intent: AUTHORIZE` with no redirect → capture. Record which modes pass. If both fail, stop and rethink the payment path.
2. **Injection can't bypass policy.** An `instruction_source: untrusted` proposal, or one whose merchant text asks to raise limits, never yields `ALLOW`.
3. **Concurrency.** Simultaneous reservations can't exceed a shared limit.
4. **Clarity.** A stranger explains the product's benefit in under a minute from the README intro.

Write the results into Architecture.md § 15 (open questions).

---

## 9. Build order

| Dates | Work |
|---|---|
| Oct 21–23 | Gates above. Repo skeleton, Makefile, docker-compose, migrations. |
| Oct 24–Nov 1 | Engine with all reason codes · ledger · signer · compiler · PayPal client · bridge MCP server · webhooks. |
| Nov 2–6 | Console (five screens + playground) · approval card · evidence pack (JSON, HTML, PDF) · dispute evidence · Gauntlet with three arms. |
| Nov 7–11 | Usability test with 5–8 testers · fix list · demo video · write-up · deploy to Render · submit. |

If time runs short, cut in this order: email delivery of approval cards → PDF evidence (keep HTML and JSON) → dispute attach → Mode B (keep Mode A). Never cut the engine tests, the concurrency test, or the injection refusal. The demo depends on them.

---

## 10. Definition of done

A change is done when:

- `make lint test` passes, and the output is in the PR description.
- New behavior has tests, including the failure path.
- No rule in § 2 is weakened.
- User-facing text follows the copy rules (§ 2.6) and uses the same words as the reason-code table.
- Architecture.md, README.md and `.env.example` are updated if the change affects them.
- No secrets, tokens or full `vault_id`s appear in code, logs, fixtures or commits.

---

## 11. Things not to do

- Don't call the LLM from `engine/`, or let LLM output change a decision.
- Don't give the agent PayPal tools, PayPal tokens, or the PayPal Agent Toolkit.
- Don't use the live PayPal URL or real money.
- Don't store money as floats.
- Don't mark a purchase complete before the verified capture webhook arrives.
- Don't claim fraud reduction, buyer protection, or that PayPal will accept the evidence.
- Don't trust anything the agent says about return terms; only `server/merchants` data counts as verified.
- Don't add features from the v1 list (passkeys, real merchant policies, buying from any merchant, AP2 adapter) before the MVP is submitted.

---

## 12. Git

- Branch from `main`: `feat/<area>-<short-name>`, `fix/...`, `chore/...`.
- Small commits with imperative messages: `engine: block on short return window`.
- One PR per feature. The PR description says what changed, how it was tested, and which rules in § 2 it touches.
