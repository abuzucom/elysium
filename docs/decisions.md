# Decision register

This register holds the approved architecture baseline for Project
Elysium. `docs/bootstrap.md` holds the full handoff. `docs/backlog.md`
tracks deferred and experimental items.

## Status rules

- D-001 through D-051 carry human approval from the architecture
  discussion.
- D-052 and later detailed questions remain deferred. They carry no
  approval.
- A conflict with an approved entry requires human approval before
  implementation.
- Numerical values below are architecture approvals. They are not
  performance guarantees. Hardware validation must not silently replace
  them.

## Approved architecture decisions

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

## Implementation approvals, 2026-11-13

The active human approved these choices during Gate 0 review. They carry
no D-number. D-numbers stay reserved for the architecture decision
sequence.

| Topic | Approved choice | Scope |
| --- | --- | --- |
| Gateway and DJ-computer source language | Go | Gate 1 onward |
| Broadcaster authentication | Mutual DTLS certificates with gateway-pinned fingerprints | Gate 1 encrypted ingest onward. Enrollment, rotation, and revocation details remain deferred. |
| Local LiveKit server for tests | Build a pinned tagged release from source through the Go module proxy. Bind to 127.0.0.1 with test-only keys. | Gate 1 integration tests |
| Gate 1 ingest dependencies | pion/dtls/v3 v3.1.15, pion/srtp/v3 v3.1.3, pion/rtp v1.10.6 | Gate 1 step 1 onward |
| Gate 1 publishing dependencies | livekit/server-sdk-go/v2 v2.19.0 with pion/webrtc/v4 v4.2.26 | Gate 1 step 2 onward |
| Gateway Go version | go 1.26.8 in gateway/go.mod | Gate 1 onward |
| LiveKit test server version | livekit-server v1.13.11 built from source, outside gateway/go.mod | Gate 1 integration tests |
| Mobile and native build infrastructure | Decide at Gate 3 | Pending |
| ESP32 board | Decide at Gate 4 | Pending |
| Temporal audit reference | Bootstrap section 8 serves as the full reference | All gates |
| PCM block and provenance contract | v0 approved: signed 16-bit, fixed 10 ms blocks, per-block change counts, integer percent scores, one-second Dart summary. See docs/contracts/pcm-block-provenance-v0.md. | Gate 3 receiver and renderer |
| Native test framework | doctest 2.5.3 (upgraded from 2.4.11 on 2026-11-13), vendored single header in native/third_party/doctest | Gate 3 onward |
| Native CI runner | Pinned to ubuntu-24.04 for a reproducible toolchain | Gate 3 onward |
| Event, admission, and session design | v0 approved: 60-minute listener tokens refreshed after 30 minutes, admission until event end plus a 4-hour default grace (6-hour maximum, set by organizer or event manager), bounded tokens during venue time holdover, one-time email codes, one device per invitation, passkeys with Argon2id fallback for staff, retention 7 days default and 30 days maximum, request expiry 60 minutes at 10 per minute. See docs/design/event-flow-v0.md. | Gate 5 |
| Hosted native build CI | Phase A approved with prebuilt, unpatched WebRTC. Phase B host decided later. Budget managed by the human in GitHub. Tool versions presented for approval. See docs/design/native-ci-plan.md. | Gate 3 |
| Native language standard | C++20 | Gate 3 onward |
| Native CI | GitHub Actions job with clang and gcc sanitizer builds | Gate 3 onward |
