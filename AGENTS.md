# AGENTS.md — rules for agents working on Pulse

Pulse is a personal, security-sensitive event bus (NATS JetStream + `pulsed`). These rules apply to every agent session here.

## Before working

1. Read `docs/GOAL.md` (canonical contract) and `docs/PLAN.md` (execution plan).
2. Check `docs/PLAN.md` §5.2 (milestone status) and `docs/DECISIONS.md` §2 (open review items) for the next milestone and blockers.
3. Consult `docs/THREAT_MODEL.md` and `docs/TEST_STRATEGY.md` for the milestone's security and test obligations.

## Contract rules

- `docs/GOAL.md` wins over PLAN.md and code until a recorded decision changes it.
- **Never silently weaken security requirements.** Relaxations need an ADR in `docs/DECISIONS.md` labeled **security-relevant change**.
- **Never mark unfinished work done.** Distinguish implemented, tested and externally verified (GOAL.md §7).
- Record deviations from PLAN.md/GOAL.md in DECISIONS.md in the same commit.
- Requirement IDs (`PL-<AREA>-NNN`) are stable: never renumber or reuse; withdraw with a DECISIONS.md reference.
- Events live in JetStream; pulsed state lives in SQLite (ADR-0024). No other datastore.
- Crypto: Go standard library and `golang.org/x/crypto` only; nkeys/jwt from nats-io. No `InsecureSkipVerify`.
- Never commit secrets, creds, seeds, master keys or private infrastructure details. Generate test keys at test time.
- No LICENSE file until the operator decides (R-01).

## Git workflow (commit locally, never push)

- Work on `main`. Commit each completed milestone (one or more local commits).
- Stage only relevant files by explicit path after reviewing `git status` and the diff.
- Generate the message with the `/cc` skill, then run `git commit` yourself. If `/cc` is unavailable, leave changes uncommitted and say so.
- **Never `git push`**, create remotes, push tags or open PRs without a specific explicit instruction.
- Never amend, rebase, force-switch branches or discard changes. Report commit hashes.
