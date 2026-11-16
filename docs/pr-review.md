# Pull request review checks

Status: Approved by the active human on 2026-11-16.

## Components

| Workflow | Source | Trigger | Role |
| --- | --- | --- | --- |
| `immutable-conflict-check.yml` (Immutable Compliance) | abuzucom/foucault 3.8.8 | `pull_request_target` on `main` | Deterministic policy gate |
| `security-review-pr.yml` | abuzucom/foucault 3.8.8 reusable `security-review.yml` | Immutable Compliance completes | Model security review against `AUDIT.md` |
| `quality-review-pr.yml` | abuzucom/euler 0.9.1 reusable `quality-review.yml` | Immutable Compliance completes | Model quality review against `QUALITY.md` |

Pins:

- foucault `d50c22c6589fa6843e1bf50d3da8b29b637a1395`
- euler `9488a8c5edc2045f2ab44f47bf12fac10f2994a8`

Each `uses:` line and its `audit_ref` or `quality_ref` input name the
same commit.

## Immutable Compliance

The gate checks out the trusted base and the pull request head. It runs
`scripts/check_compliance_tree.py` from the base checkout. A pull request
cannot weaken the checker it runs under. The gate enforces these rules:

- No secrets, weak hashes, conflict markers, or root containers.
- Every workflow action pins a full commit SHA.
- Every checkout sets `persist-credentials: false`.
- Branch names follow `<type>/<kebab-description>` with the prefixes
  `feat`, `fix`, `chore`, `docs`, and `test`.
- A branch name must not end with an opaque mixed letter and digit token.
- Commit authors resolve to GitHub accounts.
- A co-author trailer with an email address fails the gate. An agent
  label uses a name only, for example `Co-Authored-By: Claude Opus 5.5`.
- Commit subject length and prose style produce warnings only.

## Copied files

These files match foucault `d50c22c` byte for byte:

- `requirements-checkers.txt`
- `.github/workflows/immutable-conflict-check.yml`
- `scripts/check_compliance_tree.py` and the ten checkers it loads
- `scripts/trusted_git.py`, `scripts/trusted_gh.py`,
  `scripts/prose_policy.py`, `scripts/handoff_policy.py`,
  `scripts/branch_name_bans.txt`, and `scripts/prose_bans.txt`
- `scripts/check_pr_review_response.py`
- `ci/build_pr_case.py`, `ci/build_retry_case.py`,
  `ci/run_model_command.py`, `ci/call_model.py`, and
  `ci/model_providers.json`

The foucault security review runs the `ci/` and
`scripts/check_pr_review_response.py` files from the elysium checkout at
the pull request base. The euler quality review runs its scripts from its
own pinned checkout.

## Required repository settings

The active human configures these items in GitHub settings:

- Repository secret `OLLAMA_API_KEY`. Both reviews map it to
  `MODEL_API_KEY` for same-repository pull requests.
- Environment `fork-review` with required reviewers and an environment
  secret `MODEL_API_KEY`. Fork reviews run there after manual approval.
- Label `safe-to-review`. A maintainer applies it to a fork pull request
  after reading the diff.
- Branch protection on `main`. Require Immutable Compliance and the build
  checks. Never configure `security-review` or `quality-review` as the
  only required status. Model verdicts stay advisory.

## Behavior

- Each review runs once per head commit. A newer head cancels an
  obsolete run.
- `BLOCK` and `NEEDS-HUMAN` verdicts fail the review check.
- Fork pull requests without approval receive an explicit skip.
- `workflow_run` workflows run from the default branch. The reviews start
  after this wiring reaches `main`.
- The euler prescan skips `native/third_party/**`.

## Upgrade

1. Pick a new foucault or euler commit.
2. Replace every pinned SHA for that source in the caller workflow.
3. For foucault, copy the listed files again from the same commit.
4. Record the new pin in `docs/decisions.md`.
