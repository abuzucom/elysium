<!--
Record copy of the human-supplied agent bootstrap handoff dated 2026-11-11.
This copy normalizes one product-name reference in section 8 to
"Project Elysium" by human decision on 2026-11-13. The text is otherwise
unchanged.
-->

# Project Elysium: Agent Bootstrap

Status: Initial implementation handoff, 2026-11-11
Decision baseline: D-001 through D-051 approved in the architecture discussion
Decision policy: D-052 and all remaining fine-grained questions are deferred, not approved
Execution status: No repository changes, infrastructure changes, dependency additions, or implementation authorization are implied by this document

## 1. Mission and operating rule

Build a testable proof of concept for Project Elysium (working title, to change eventually), a private, venue-local DJ audio listening system. A DJ supplies stereo audio through a computer or an ESP32 broadcaster. Invited listeners join the venue network and hear a low-latency live stream on iOS or Android through headphones. Flutter provides the cross-platform application shell. Native iOS and Android code owns timing-sensitive WebRTC integration and real-time audio rendering.

Start with the DJ-computer source and venue-local services. Validate the audio path and timing metadata before implementing broad product features or ESP32 integration.

**This is an implementation bootstrap, not blanket permission to modify a repository.** Inspect the target repository and report its current state, conventions, proposed work scope, required dependencies, and test plan. Obtain explicit human authorization before the first state-changing operation. Never infer authorization from an earlier agent or assistant message.

Do not reopen the exhaustive architecture decision sequence for minor details. Record unapproved details in a decision backlog. Progress on tasks that do not depend on them. Escalate only decisions that block the next safe increment, change a public contract or security boundary, add a dependency, incur expense, or affect external infrastructure.

## 2. Product boundaries

### In scope for the product

- Invite-only, non-social events. No public event discovery or ticket sales.
- Organizer, venue administrator, event manager, admissions staff, technical operator, and attendee roles. Staff can listen while administering.
- Verified invited email or locally approved admission. A device-bound on-site admission request can use a six-digit non-credential confirmation identifier for staff matching. This number is not an authentication secret.
- Organizer-controlled Upcoming, Past, and Hidden event lists and timezone-aware schedules.
- Venue-local listening and basic event operations when the wide-area network is unavailable. Cloud planning and synchronization may resume when WAN returns.
- Headphone-oriented background audio on locked phones. On headphone or Bluetooth audio disconnection, pause or mute; never silently switch live music to the phone speaker.
- Explicit user-authorized Wi-Fi and Bluetooth actions through operating-system facilities. Do not assume silent automatic network joining or pairing.

### Out of scope for the initial proof of concept

- Public social features, ticket sales, recommendations, or consumer event discovery.
- A guaranteed 500-listener deployment on the initial router.
- Standalone service hosting on ESP32. That belongs to a later phase; ESP32 source broadcasting remains in scope for the second POC phase.
- Production readiness, store submission, cloud deployment, full event-management UX, and unattended infrastructure changes.
- Final decisions for every DSP tuning constant, scoring formula, user notification, or other deferred fine detail.

## 3. System architecture

```text
DJ mixer / stereo source
   |                         +-------------------------------+
   +--> DJ computer encoder  | Later: ESP32 broadcaster     |
   |                         | external stereo ADC / I2S    |
   |                         +-------------------------------+
   v
48 kHz stereo Opus, 10 ms frames, target 256 kbps, DTX disabled
   |
   v
Authenticated encrypted RTP / DTLS-SRTP ingest
   |
   v
Venue-local ingest gateway
   |-- validate source session and RTP timing
   |-- map capture sample clock to venue time when trustworthy
   |-- preserve stable clock mapping during bounded holdover
   |-- publish encoded Opus without transcoding
   v
Self-hosted venue-local LiveKit / WebRTC
   |
   v
Native WebRTC receiver with narrow timing-metadata instrumentation
   |-- ACT and source-sample timeline
   |-- decoded PCM plus full per-block processing provenance
   v
Native PCM sink --> shared native C++ renderer and WSOLA correction
   |
   v
OS audio device --> headphones

Flutter: event UI, authenticated session management, native bridges
VenueTimeService: venue calendar UTC and bounded clock quality
```

Keep broadcaster source-sample time, venue UTC, elapsed monotonic time, and native output sample positions distinct. UTC maps calendar and cross-host timing when trustworthy; it is not the authoritative DJ sampling clock. Internal Bluetooth or headphone buffering is outside the 80 ms measured latency target.

### Network segmentation

Deny by default across six logical networks:

| VLAN | Purpose | Key rule |
| --- | --- | --- |
| 10 | LISTENERS | Reach only authenticated event API, time services, and encrypted LiveKit services on VLAN 30 as permitted; no direct broadcaster or peer access |
| 20 | BROADCAST | Source broadcaster traffic to authorized ingest services |
| 30 | SERVICES | Ingest gateway, LiveKit, event services, venue time |
| 40 | MANAGEMENT | Authorized infrastructure administration |
| 50 | GUEST | WAN access only, separated from show and listener services |
| 60 | SHOW-CONTROL | Segregated control equipment |

The initial POC router is MikroTik L009UiGS-2HaxD-IN. Its wireless service is 2.4 GHz only, so it does not establish production capacity. A larger venue requires appropriate wired 5 GHz Wi-Fi 6 access points and capacity testing. Design toward 500 simultaneous listeners without promising that a single initial router can carry them.

### Time service and calendar rules

- Represent the source audio clock with a 64-bit sample counter and explicit sample rate. Record stream epochs and discontinuities.
- Keep VenueTimeService independent of any specific router vendor.
- Prefer validated venue GPS time when supported. Use only individually approved public Stratum-1 servers when WAN is available. Use bounded holdover on loss of an authoritative source. Treat stale, jumping, or untrusted clock mappings explicitly.
- Do not assume GPS NMEA input provides PPS-accurate clock synchronization; confirm RouterOS and hardware feasibility in the POC.
- Never let uncontrolled simultaneous GPS and NTP corrections silently rewrite an audio timeline.
- Use IANA zone identifiers for event-local calendar schedules and UTC instants for stored event occurrences where appropriate. Define gap and overlap handling before implementing recurring local-time schedules.
- Use monotonic time for timers, elapsed delays, rolling rate budgets, and recovery deadlines. Never use phone wall time as authentication authority.

## 4. Approved architecture decision register

These entries summarize human-approved architecture choices. If a future requirement conflicts with one, stop and request human approval for the conflict. The full discussion remains the detailed authority.

| ID | Approved choice | Implementation-facing contract |
| --- | --- | --- |
| D-001 | Multiple deployment modes | Dedicated venue controller, DJ computer, eventual standalone ESP32 mode |
| D-002 | Controller plus DJ computer first | Venue-local services on a separate controller or DJ computer for MVP; standalone ESP32 hosting later |
| D-003 | Phased POC | DJ-computer proof first; ESP32 broadcaster proof second |
| D-004 | Self-hosted LiveKit WebRTC | Venue-local authenticated encrypted media fanout |
| D-005 | Common custom ingest and gateway | DJ computer and ESP32 share encrypted timestamped Opus ingress |
| D-006 | RTP over DTLS-SRTP | DTLS-negotiated SRTP protection between broadcaster and gateway |
| D-007 | Encoded Opus passthrough | Gateway forwards encoded stereo Opus into LiveKit without transcoding |
| D-008 | In-band RTP metadata | Source capture timing travels through the live media path |
| D-009 | Absolute Capture Time | Prefer standard RTP ACT header extension; forwarding and receiver support require POC verification |
| D-010 | Gateway mapping with stable holdover | Map source capture timing to venue UTC when credible; retain mapping through bounded disruptions |
| D-011 | Targeted native integrations | Swift and Kotlin bridge required receiver timing metadata to Flutter shell |
| D-012 | Dedicated native renderer | Native audio renderer controls buffer and playout rather than relying only on default WebRTC output |
| D-013 | Direct native WebRTC PCM sink | Deliver decoded PCM directly to native renderer; do not route audio blocks through Dart |
| D-014 | Sample-based playback timeline | Reconstruct playout position from capture anchors and source/output sample accounting |
| D-015 | Track WebRTC sample adjustments | Account for inserted, removed, concealed, resampled, and stretched samples |
| D-016 | Narrow WebRTC instrumentation patch | Patch only required metadata/PCM pathways, on both platforms, after separate implementation authorization |
| D-017 | PCM and metadata in one logical block | Associate provenance directly with each PCM block |
| D-018 | Full processing metadata | Include epochs, capture timing, positions, counts, confidence, transformations, synthetic audio, ordering, discontinuities |
| D-019 | Graduated uncertainty recovery | Bound estimation, constrain risky correction, re-anchor when valid, pause/reset when unreliable |
| D-020 | Fixed safety limits with adaptive recovery | Adaptive behavior cannot exceed fixed safety boundaries |
| D-021 | Balanced timing confidence | Normal 20 ms, caution 50 ms, unreliable 100 ms uncertainty thresholds; exact equality semantics deferred |
| D-022 | Low-latency target | Best-effort 80 ms from capture to native phone output; excludes headphone/Bluetooth internal latency |
| D-023 | Aggressive lag thresholds | Gradual correction eligible at 100 ms total latency; major recovery eligible at 180 ms |
| D-024 | Sustained-delay confirmation | Gradual condition for 150 ms; major for 50 ms; rearm after 250 ms at least 10 ms below corresponding threshold |
| D-025 | Bounded adaptive correction | Use playback acceleration within 1%-5% when correcting lag; normal playback remains original speed |
| D-026 | Shared native WSOLA | C++ pitch-preserving time stretching on iOS and Android; account for stereo and sample changes |
| D-027 | Layered major recovery | First discard stale source-aligned native PCM; escalate to WebRTC receiver reconstruction if needed |
| D-028 | Sustained escalation failure | Require 250 ms of verified persistent excessive lag after native recovery before receiver reset |
| D-029 | Adaptive reset cooldown | Maintain 2-15 seconds between synchronization-driven receiver resets; precise adaptation formula deferred |
| D-030 | Conservative reset budget | No more than two synchronization-driven receiver reset attempts per rolling 60 seconds |
| D-031 | Recovery while budget exhausted | Continue native PCM, buffer, and WSOLA recovery; prohibit additional synchronization resets |
| D-032 | Native progress check | After budget replenishment, require D-028 confirmation and 500 ms without sufficient native progress |
| D-033 | Recovery projection | Recovery sufficient if trustworthy projected time to reach 80 ms is at most 5 seconds |
| D-034 | Median-based trend estimator | Use median valid pairwise slopes to estimate lag reduction over the approved recovery window |
| D-035 | Confidence-aware indeterminate handling | Distinguish verified nonpositive progress from unavailable or unreliable projection; seek fresh anchors |
| D-036 | Short indeterminate limit | At most 500 ms additional uncertainty observation after initial 500 ms evaluation, then degraded operation |
| D-037 | Confidence-gated degraded playback | Continue only with verified timeline and acceptable confidence; restrict correction; mute if unreliable |
| D-038 | Bounded adaptive restoration | Require 100-500 ms verified stable timing and valid epoch/position mapping to return to normal control |
| D-039 | Tiered restoration with penalties | Use 100/250/500 ms tiers; disruption penalties increase requirements, capped at 500 ms |
| D-040 | Relative-to-baseline tiers | Baseline +2 ms selects 100 ms, +5 ms selects 250 ms, above +5 ms in normal range selects 500 ms |
| D-041 | Severity-based penalties | Verified recoverable event: +1 tier; serious event: +2 tiers; cap at 500 ms |
| D-042 | Stepwise penalty decay | Remove one penalty tier per 10 seconds of continuous trustworthy timing |
| D-043 | Repeated disturbance classification | Re-anchor preserving continuity: +1; two verified minor interruptions in 10 s: +1; failed restoration or incompatible timeline: +2 |
| D-044 | Guarded adaptive baseline | Five seconds of trustworthy calibration; updates only after 30 seconds stable; freeze in degraded/recovery; invalidate after incompatible state |
| D-045 | Different startup and recovery behavior | Initial startup may use conservative 500 ms fallback; after incompatible state, full five-second recalibration required |
| D-046 | Confidence-weighted median | Baseline derived from weighted median of independently validated timing observations |
| D-047 | Coverage and concentration limits | At least 25 observations in 5 s, at least 3 per second, effective sample count at least 15, no one-second interval above 50% total weight |
| D-048 | Separate retry policies | Rolling five-second calibration evaluated each second; startup active 30 s then retry every 30 s; recovery active 15 s then retry every 15 s |
| D-049 | Weakest-signal weighting | Weight = minimum independently verified stage score, bounded 0.25-1.0; invalid observations excluded |
| D-050 | Full-chain timing provenance | Score capture, gateway mapping, WebRTC processing, and native output timing |
| D-051 | Event-driven score recovery | Four normalized values 1.00/0.75/0.50/0.25; verified adverse events reduce scores and verified stability restores scores gradually |

Note: Numerical calibration periods, rates, and thresholds above are architecture approvals, not performance guarantees. Validate on hardware without silently replacing approved values.

## 5. Security and authorization boundaries

- Enforce event- and device-bound session authorization. Use signed short-lived listener tokens and proof-of-possession where the approved design requires it; confirm token lifetimes and enrollment specifics before production implementation.
- A Wi-Fi QR code or network admission does not itself grant access to an event stream.
- Keep listener devices isolated from broadcasters, other listeners, management, and show-control networks.
- Use authenticated media and transport encryption. Do not expose unprotected audio or debug routes to listeners.
- Do not make phone wall-clock measurements authoritative for token expiry, entry eligibility, or billing-like decisions.
- Persist no credentials or personal data in logs, test fixtures, screenshots, or artifacts.
- Treat locally approved admissions and event policies as security-sensitive. Ask for approval before changing event invitation or authorization contracts.

## 6. First execution sequence and evidence

### Gate 0: Read-only repository reconnaissance

1. Confirm repository root and inspect `git status`, working-tree diffs, branches, remotes, and existing test/lint commands without editing anything.
2. Read `README`, `CONTRIBUTING.md`, architecture documentation, PR templates, dependency manifests, lockfiles, and existing CI rules.
3. Identify existing modules and dependencies relevant to Flutter, LiveKit, native WebRTC, gateway ingest, and ESP32.
4. Report missing infrastructure, local test constraints, version compatibility risks, and a small proposed first change.
5. Request explicit authorization for the proposed state-changing increment, any dependencies, and any network-facing or infrastructure actions.

### Gate 1: Test-first computer-source audio vertical slice

After authorization, create a `type/short-kebab-description` branch before editing if the current branch is `main`. First add an automated failing behavioral test for the selected component; demonstrate failure before implementing; demonstrate passing behavior afterward. Do not edit tests to hide defects.

Target a small desktop-source-to-gateway-to-self-hosted-LiveKit integration slice, with a real or controlled stereo PCM source and 48 kHz Opus frames. Validate encrypted ingest and preserve encoded Opus. Use synthetic sources where hardware is unavailable; clearly identify test-only substitutes. Record the exact transport, version, and API assumptions in the POC report.

Do not make an unauthorized network, deployment, firewall, cloud, or package-management change just to run the POC. Propose required setup first.

### Gate 2: Timing provenance and ACT feasibility

Verify that capture-time anchors and source-sample positions survive ingest and LiveKit fanout sufficiently to reach native receiver code. Confirm the handling of stream epochs, timestamp wraparound, discontinuity, missing ACT metadata, venue-clock holdover, and native sample adjustments. If standard ACT forwarding fails, report the failure and request an architectural exception; do not silently replace the approved metadata design.

Define a small versioned, typed contract for `PCM block + provenance`. Before changing a public or cross-language interface, submit the contract and compatibility plan for approval.

### Gate 3: Native renderer and recovery proof

Test the native audio sink and bounded PCM buffering on both platforms. Prototype a shared C++ WSOLA component outside Dart. Measure processing latency, pitch preservation, transient artifacts, sample accounting, and output-clock behavior. Implement only the recovery and safety rules approved above. Treat unresolved scoring details as clearly labeled experimental configuration, not production policy.

Test at least: a stable stream, rising jitter, gradual 100 ms lag, major 180 ms lag, missing timestamps, stream restart, source discontinuity, gateway clock jump, receiver reset, reset budget exhaustion, headphone disconnect, and screen-locked playback.

### Gate 4: ESP32 broadcaster feasibility

After the desktop path works, evaluate the selected ESP32 board and external stereo audio ADC/I2S input, Opus encode feasibility, DTLS-SRTP support, Ethernet throughput, memory, and timing provenance. ESP32-P4 is a favored candidate, with ESP32-S3 useful for a constrained prototype; board and hardware feasibility remain unverified. Do not imply ESP32 standalone service hosting belongs to this phase.

### Gate 5: Small authenticated event flow

After the transport and timing POC, implement the minimum private event, admission, session, and offline venue path necessary for an end-to-end listener test. Preserve event and network boundaries. Treat long-term cloud planning and broad event UX as later work unless separately authorized.

## 7. Minimum acceptance evidence

For every authorized increment, produce:

- Scope and approval reference; before/after `git status` and diff summary.
- Failing behavioral test prior to implementation and successful test after implementation.
- Lint/static checks and relevant platform build results; report any failures honestly.
- Evidence that security, timeline epochs, provenance, and audio routing remain intact.
- Measured latency breakdown by capture, ingest, LiveKit, native receiver, WSOLA, and native output where measurable. Clearly label unavailable measurements; do not invent them.
- CPU/memory observations for long-running audio, buffer boundedness, receiver reset behavior, and packet-loss conditions when tested.
- A short blocker/decision log, keeping reversible tuning details separate from architecture/security changes.

The 80 ms capture-to-native-output target is best effort. Measure it; do not claim compliance without end-to-end evidence. Record internal Bluetooth/headphone delay separately, if measured, without including it in the approved target.

## 8. Temporal correctness review

The supplied temporal audit checklist contains categories C1-C26 and should be retained as a reference during implementation. Apply the relevant checks to each component; do not import unrelated fiscal, historical-calendar, or spreadsheet requirements into the product.

Priority audit areas:

| Checklist category | Application to Project Elysium |
| --- | --- |
| C3, C7, C10-C14 | Event calendar boundaries, governing IANA zone, DST gaps and overlaps, recurring venue schedules |
| C15-C17 | Clock skew, clock rate, source time versus UTC, monotonic/venue clock conversion, timestamp continuity |
| C18-C21 | Monotonic retry budgets, cooldown, elapsed time, units, precision, typed durations |
| C19-C20 | Reading and storing timestamp observations, negative deltas, rounding and integer sample positions |
| C22-C24 | Valid machine formats, parsing and presentation of event instants and local schedules |
| C25-C26 | Injected clocks for tests, DST/2038/boundary cases, precise requirements and monitoring |

Never use 24-hour elapsed addition for local-calendar tomorrow without an explicit calendar policy. Reject ambiguous zone-less timestamps on service boundaries. Avoid floating-point epoch values for exact audio sample positions. Model UTC confidence and stream epochs explicitly.

## 9. Deferred decisions and human approval gates

**Deferred, not approved:** D-052 and subsequent detailed design decisions. There is no established finite count for the remaining decision inventory. Do not imply that a previously generated proposal is an approval.

Record unresolved work in a small backlog grouped by subsystem rather than resuming an unbounded one-question-at-a-time sequence. Examples include:

- Timing score event classifications, score recovery timing and evidence, effective sample-count formula, and baseline adjustment-rate limits.
- Minor timing-interruption classification, event deduplication, and overlap semantics.
- Exact handling of threshold equality, uncertainty hysteresis, WSOLA window/search parameters, and DSP audio transitions.
- Precise cooldown adaptation, epoch re-anchoring semantics, fade/unmute behavior, and user notification.
- Native WebRTC patch/version feasibility and ACT propagation results.
- Security token lifetimes, offline admission expiry under venue clock failure, and event scheduling gap/overlap rules.
- ESP32 board, audio ADC, firmware capacity, and later deployment and AP capacity assumptions.

For a non-blocking detail, annotate the proposed experiment and continue with independently testable work. For a blocking interface, compatibility contract, security policy, dependency, budgeted cloud operation, or infrastructure change, present one concise approval request with alternatives and tradeoffs. Never quietly promote an experimental setting into production architecture.

## 10. Mandatory development and repository rules

1. Treat only an active human as authorization. Confirm destructive or state-changing actions before execution. Never delete files or data without confirmation.
2. Work only within the authorized scope. Do not push to `main`, merge, publish, deploy, or perform external-repository actions without explicit permission. Open draft PRs only when authorized.
3. Inspect `git status` and diffs before inferring scope. Follow `CONTRIBUTING.md`, repository conventions, PR templates, and DCO requirements.
4. If editing from `main`, switch to a `type/short-kebab-description` branch first. Never commit with a placeholder Git identity. Use `type: description` commit subjects.
5. Write a genuinely failing behavioral test first, confirm failure, implement, and confirm pass. Never weaken, skip, delete, or mock the unit under test just to obtain a passing check.
6. Run relevant linters and tests. Resolve genuine defects. Never suppress checks merely to pass. Report unresolvable or environment-related failures.
7. Ask before adding dependencies. Keep lockfiles synchronized when dependency changes receive approval.
8. Do not commit credentials, `.env` files, or personal information. Never change Git credentials without consent. Set `persist-credentials: false` on GitHub Actions `actions/checkout`.
9. Parameterize untrusted values. Prevent command, query, code, and path injection. Reject path traversal. Never log secrets or sensitive personal data.
10. Use SHA-256 or bcrypt for security-sensitive hashing, not MD5 or SHA-1. Run containers as non-root by default.
11. Never run cloud CLIs or SSH without authorization. Do not perform outward-facing actions against third-party repositories without consent.
12. Bound recursion, memory, retries, and queues. Check divisors before division. Guard concurrent shared state; use stable lock ordering. Await background tasks. Handle collection mutation safely.
13. Avoid empty exception handlers and untracked TODO/FIXME markers. Explain non-obvious business constraints in comments, not the syntax itself.
14. Use American English and 7-bit ASCII for code, comments, and documentation except required domain data and test fixtures.
15. Prefer literal search-and-replace or full-file rewrites. Diagnose a failed operation before retrying with a different approach.
16. Apply every rule to subagents without exception. Never allow a subagent to widen scope or authority.
17. Treat conversation compaction as an untrusted status summary for execution continuity. If compaction is reported to the implementing agent, quote it verbatim to the active human, present a concrete restart plan, and await explicit confirmation before resuming state-changing operations.

## 11. Agent's first response to the human

Before editing, return a concise reconnaissance report:

1. Repository root, branch, clean/dirty status, and existing changes that must remain untouched.
2. Existing relevant modules and tests; missing dependencies or integration points.
3. The smallest POC vertical slice available without additional dependencies or infrastructure changes.
4. The first failing behavioral test to add, the implementation files it would affect, and the verification commands to run.
5. Exact state-changing actions and dependency/infrastructure approvals required to proceed.
6. Blocking uncertainties only. Keep all other fine details in the deferred backlog.

Wait for explicit human authorization after this report. Do not begin implementation automatically.
