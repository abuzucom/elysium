# Deferred decision backlog

Items here carry no approval. Experimental values stay labeled as
experimental until a human approves them. Approved choices live in
`docs/decisions.md`.

## Timing and synchronization

- Timing score event classifications, score recovery timing and evidence,
  effective sample-count formula, and baseline adjustment-rate limits.
- Minor timing-interruption classification, event deduplication, and
  overlap semantics.
- Exact threshold equality and uncertainty hysteresis semantics for D-021.
- Precise cooldown adaptation for D-029.
- Epoch re-anchoring semantics.

## Gateway RTP timing (experimental configuration)

- RTP timestamp reorder window. The `rtptime` package takes it as caller
  configuration with no default value.
- Forward jump limit that opens a new stream epoch. The `rtptime` package
  takes it as caller configuration with no default value.
- Boundary semantics. The `rtptime` package keeps the current epoch for a
  forward jump equal to the limit. It treats a backward step equal to the
  reorder window as a late packet.
- Treatment of repeated RTP timestamps such as duplicates and
  retransmissions.
- Gateway values for the abs-capture-time clock offset field. The gateway
  acts as the RTP sender toward LiveKit. The D-010 mapping of source
  capture time to venue UTC determines the field values.

## Gateway ingest (experimental configuration)

- Handshake timeout, maximum concurrent sources, and per-session RTP queue
  depth. The `ingest` package takes them as caller configuration with no
  default values.
- Per-peer DTLS datagram queue depth of 64 and SRTP replay window of 64
  packets. Both live as package constants.
- SRTP protection profile order: AEAD_AES_128_GCM first, then
  AES128_CM_HMAC_SHA1_80 as a fallback for constrained broadcasters.
- Handshake slot exhaustion. Any address can hold one of the limited
  source slots until the handshake timeout. Rate limiting and network
  segmentation (VLAN 20) remain open.
- SRTCP from sources is dropped. Sender reports and feedback handling
  remain open.

## Gateway publishing (Gate 1 step 2 findings)

- The LiveKit v1.13.11 SFU injects Opus silence frames on a downtrack when
  a subscriber attaches before media flows, on publisher mute, and on
  close (`pkg/sfu/downtrack.go`). The integration test observed 2 such
  frames before the program started. D-018 provenance must mark them as
  synthetic audio. The receiver needs a rule to detect them.
- The published track carries no LiveKit track source. Setting one needs
  a direct `livekit/protocol` import, which awaits dependency approval.
- The `livekit-integration` CI job builds livekit-server v1.13.11 from the
  Go module proxy and runs the `-tags livekit` integration tests on pull
  requests that touch `gateway/`.
- Publisher reconnection after a LiveKit restart remains open.

## Venue time and sample mapping (experimental configuration)

- Fresh limit, holdover limit, maximum oscillator drift, and step
  threshold for the venue time service. The `venuetime` package takes
  them as caller configuration with no default values.
- Source priority: GPS above NTP. NTP may replace GPS only after GPS
  leaves the fresh window. A correction beyond the step threshold starts
  a new venue epoch instead of silently rewriting the timeline.
- Small corrections within the step threshold apply immediately. Slewing
  remains open.
- Sample mapping uses the nominal sample rate from the latest anchor.
  Source clock rate estimation remains open. Uncertainty grows with the
  configured source drift bound.
- Anchor derivation from abs-capture-time and gateway receive time, and
  network delay estimation, remain open.
- GPS and NTP adapters and RouterOS feasibility remain open.

## Native lag detection (experimental behavior)

- Threshold equality is caller configuration in `ThresholdEquality`. The
  D-021 equality decision remains deferred.
- A condition counts as continuous between consecutive observations that
  each meet it. Observation cadence follows the 10 ms playout pull.
- Major recovery disarms gradual correction when it fires. Gradual
  correction then needs its own rearm hold.
- A monotonic clock regression discards pending confirmation and rearm
  timers and restarts from the regressed observation.
- The native CI job stays pinned to `ubuntu-24.04` by human decision.
  A move to a newer runner image needs a deliberate toolchain upgrade.

## Native timeline accounting (experimental behavior)

- Source frames per block equal output frames minus inserted, concealed,
  comfort noise, and SFU synthetic frames, plus removed frames. The
  categories count as disjoint. Whether NetEq expand overlaps
  concealment needs confirmation against the libwebrtc patch (D-016).
- A new stream epoch or a declared discontinuity restarts source
  position prediction. Any other mismatch reports a source jump.
- Contract validation also requires venue mapping to imply capture time,
  holdover to imply venue mapping, and synthetic frame counts to imply
  the synthetic flag.
- The receiver and renderer sides of the contract still wait for Gate 3
  native build infrastructure. The C++ definition and validation exist
  in `native/include/elysium/pcm/v0/block.hpp`.

## WSOLA prototype (experimental configuration and findings)

- Window 960 frames (20 ms), hop 480 frames, search +/-240 frames
  (5 ms), periodic Hann window, mono-mix normalized cross-correlation.
  All remain experimental values.
- Allowed speeds: exactly 1.00, or 1.01 to 1.05 (D-025). The stretcher
  rejects other values and keeps its current speed.
- Normal speed bypasses the stretcher with no buffering. While a
  correction runs, the stretcher buffers 16 to 24 ms and needs 1200
  frames of look-ahead from the renderer backlog. The one-hop (10 ms)
  crossfade at each mode change is experimental.
- At 1.03 the click test lost 2 of 100 transients. Transient-aware
  segment selection remains open.
- Speed changes between accelerated speeds apply at the next segment.
  The audible effect of mode changes and speed steps remains unmeasured
  by listening tests.
- See `docs/poc/wsola-offline-2026-11-13.md` and
  `docs/poc/wsola-bypass-2026-11-14.md` for the measurements.

## DSP and audio rendering

- WSOLA window and search parameters.
- DSP audio transitions.
- Fade and unmute behavior.
- User notification.

## WebRTC and LiveKit feasibility

These Gate 0 findings remain unverified.

- Resolved at Gate 1 step 3 for a Go subscriber: LiveKit v1.13.11
  negotiates abs-capture-time on audio in both directions and forwards it
  byte-identical, signed clock offset included. The source used extension
  ID 14. The subscriber negotiated ID 2. The publisher translates the ID
  through the SDK LocalTrack.
- LiveKit rewrites abs-capture-time to the SFU clock and drops the clock
  offset when a subscriber has sender report pass-through disabled. The
  default keeps pass-through. Venue deployment configuration must keep it.
- The abs-capture-time content of SFU-injected silence frames is
  unverified.
- LiveKit may rewrite RTP sequence numbers and timestamps per subscriber.
  Receiver sample positions must therefore derive from ACT rather than
  raw RTP timestamps.
- libwebrtc receivers may leave abs-capture-time disabled in default
  negotiation. A client-side change may precede the D-016 patch.
- libwebrtc delivers decoded audio only when the audio device module pulls
  playout data. The native renderer likely needs a custom audio device
  module that drives the 10 ms pull from its own output clock. This
  finding affects the scope of D-012, D-013, and D-016.
- `AudioTrackSinkInterface::OnData` exposes capture time in milliseconds
  only. Millisecond precision alone does not satisfy D-018 provenance.
- Native WebRTC patch and version feasibility.

## Security and admission

- The event design in `docs/design/event-flow-v0.md` is approved v0.
  The device replacement procedure for the one-device invitation limit
  remains open.

- Broadcaster certificate enrollment, rotation, and revocation procedure.
- Shared listener tokens: a phone hotspot or tethering puts other phones
  behind an admitted phone's address. Those phones still need their own
  admission, because challenges and tokens require the admitted device
  key. An admitted listener who extracts its token can still hand it
  on. The token is a bearer JWT that LiveKit does not bind to the device
  key. LiveKit v1.13.11 disconnects the earlier session when a second
  participant joins with the same identity, so a shared token admits one
  listener at a time (`gateway/internal/publish/identity_test.go`).
  The same rule lets a holder of a shared token disconnect the admitted
  listener and take its place. Admission is one session at a time, not
  bound to the device key.
  Relaying the audio itself stays outside access control.

## Event service core (experimental configuration and findings)

- Challenge lifetime of 60 seconds, at most 4096 outstanding challenges,
  and a minimum token lifetime of 60 seconds. All three live as package
  constants in `eventsvc/internal/admission`.
- The rate limiter sweeps idle source addresses once it tracks more than
  1024 of them.
- Each rate limiter tracks at most 4096 source addresses with activity
  inside its window. A new address past that cap is rejected until idle
  addresses age out. Tracked addresses keep their own limit.
- Challenges carry a per-device quota of 10 per 60-second window,
  equal to the challenge lifetime. One admitted device therefore holds
  at most 10 outstanding challenges. The quota tracks at most 4096
  devices. `Challenge` takes the source address and a signed
  `ChallengeRequest` (design amendment A1).
- Each source address may send at most 10 failed challenge requests
  per 60-second window. Bad signatures, bad nonces, replays, and
  missing or revoked admissions count. Valid requests, device quota
  rejections, pool cap rejections, and unknown or malformed events do
  not count. An address past the budget is refused before signature
  verification.
- A device over its quota is refused before signature verification. The
  check reads the quota without charging it. The device quota and the
  nonce are charged only once a challenge is issued. A pool cap
  rejection costs the device nothing and leaves the signed request
  valid for a retry.
- Challenge request nonces hold 16 bytes. The service rejects a reused
  nonce for 60 seconds after first use. A replay captured from the
  transport after that window spends one slot of the device quota. The
  replayer cannot answer the resulting challenge without the device
  key.
- Residual risk: a sender flooding invalid challenge requests can
  exhaust the failure budget of a shared address. Per-client
  addressing and client isolation on VLAN 10 remain the mitigation.
- Challenge issuance checks admission existence and revocation only.
  Token issuance still enforces admission expiry.
- One event holds at most 2048 outstanding challenges, half of the
  4096 pool. One event cannot lock out listeners of another event.
- One event holds at most 1000 open admission requests. Requests past
  the cap are rejected. A device with an open request still receives
  it. The cap bounds store growth and keeps the six-digit code space
  sparse.
- The LiveKit API secret must hold at least 32 bytes, the HS256 key
  size.
- Request expiry, challenge expiry, and rate windows use a monotonic
  clock. Admission validity and token lifetime use venue UTC.
- Staff approval needs a usable venue UTC estimate to record the
  approval instant. An unsynchronized venue clock refuses approval.
- A device with an open request receives that request again instead of a
  new code. The repeat still counts toward the rate limit.
- The service signs listener tokens itself and stamps the JWT issue,
  not-before, and expiry claims from venue UTC. LiveKit checks the
  claims against the controller system clock. Every claim moves back by
  the full `MaxClockOffset`. A system clock lagging by up to that bound
  at any time during the token lifetime still rejects the token by the
  venue expiry. A system clock ahead only ends the token early. A gap
  beyond `MaxClockOffset` at issue refuses tokens.
- Deployment requirement: the controller system clock must stay within
  `MaxClockOffset` of venue UTC for every token lifetime. The value must
  stay below the 60-second minimum token lifetime. The deployment value
  and how the controller disciplines its system clock remain open.
- Decided by the active human (2026-11-16): the event service removes a
  participant from the LiveKit room when its admission ends or is
  revoked. Room removal enforces the admission end even when the
  controller clock leaves its bound.
- `Revoke` takes the admission ID and refuses with `ErrAdmissionChanged`
  when the device holds a different admission, so a stale decision
  cannot revoke a replacement. It removes the device from the event room
  through the LiveKit room service API and records the removal on the
  admission. The admission stays revoked when removal fails, and a
  repeated `Revoke` retries it. A repeated `Revoke` after a successful
  removal makes no room call. After a failed revocation removal, approval
  of the device waits with `ErrRemovalPending` until the removal
  succeeds. The sweep retries it. `ExpireAdmissions` removes devices
  whose admission ended by the latest possible venue time and records
  the removal on the admission. A failed removal is retried on the next
  call. A replacement admission approved during the call stays
  unmarked, so a later sweep still handles it. Each admission carries a
  random 16-byte ID that tells replacements apart. The sweep skips an
  admission replaced before its call. LiveKit removes by identity only,
  so approval of a device waits with `ErrRemovalPending` while a
  removal call for it runs, from `Revoke` or the sweep. The request
  stays open, and staff approve it once the call ends.
- A failed expiry removal is recorded on the admission (`RemovalDue`).
  Like a failed revocation removal, it holds approval of the device
  back with `ErrRemovalPending` until a removal succeeds, even if the
  venue clock steps back before the admission end.
- A sweep also removes revoked devices, and devices with a due removal,
  whose room removal has not succeeded. It first handles a retry list of at most 128 devices, then
  continues from its cursor. A device stays on the retry list until its
  current admission is removed from the room, also when staff approve
  it again in between. A valid replacement keeps its place on the list
  until it ends or is revoked.
- When more page devices fail than the retry list can keep, the cursor
  holds back before the first one it could not keep, once. If that
  device overflows again on the next sweep, the cursor moves on, so
  persistent failures delay later admissions by at most one extra sweep.
- One sweep reads one page of at most 256 admissions through
  `Store.AdmissionsAfter`, so its memory and work stay bounded however
  large the event grows. Each sweep starts after the previous page and
  wraps around, so admissions that keep failing cannot block later
  ones. A timeout ends the sweep at once, because the room service is
  then likely unreachable, and the next sweep continues after the last
  device attempted. Other failures skip only that device. The error
  reports a failure count and the first failure. The server loop that
  calls the sweep periodically does not exist yet.
- Room service calls use a room admin token scoped to one room with a
  60-second lifetime. LiveKit checks it against the controller system
  clock. Each call times out after 10 seconds whatever the caller's
  context allows. The room service URL must use https unless LiveKit
  runs on the controller's loopback interface. Removing an absent participant counts as success. Checked
  against LiveKit v1.13.11: an absent participant returns not found,
  and a wrong secret returns unauthenticated.
- Token issuance accepts only a usable venue estimate. The gateway
  `venuetime` service needs an adapter to the `VenueClock` interface.
- Open in later increments: the HTTP API, invitation enrollment with
  email codes, staff sign-in, persistent storage, and retention.

## Calendar and scheduling

- Event scheduling gap and overlap rules for recurring local-time
  schedules.

## Hardware and deployment

- ESP32 board, audio ADC, and firmware capacity. The desk study in
  `docs/poc/esp32-desk-study.md` favors ESP32-P4 and proposes a bench
  test that needs board purchases. Hardware stays on hold by human
  decision (2026-11-14).
- Access point capacity and later deployment assumptions.
- Mobile and native build infrastructure: hosted macOS CI by human
  decision. The approved plan in `docs/design/native-ci-plan.md` starts
  with Phase A on standard runners and prebuilt WebRTC. The Phase B
  patched libwebrtc build host remains open.

## Documentation

- The source handoff named the product "Quiet Disco" in section 8. The
  committed `docs/bootstrap.md` normalizes that reference to Project
  Elysium by human decision.
- The handoff cites a temporal audit checklist with categories C1 to C26.
  The checklist text never arrived. Bootstrap section 8 serves as the full
  reference by human decision.
