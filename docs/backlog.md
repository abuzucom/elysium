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
- The LiveKit integration test runs only with `-tags livekit` and a
  `LIVEKIT_SERVER_BIN` path. CI does not build the server or run it.
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

- Security token lifetimes and enrollment specifics.
- Offline admission expiry under venue clock failure.
- Broadcaster certificate enrollment, rotation, and revocation procedure.

## Calendar and scheduling

- Event scheduling gap and overlap rules for recurring local-time
  schedules.

## Hardware and deployment

- ESP32 board, audio ADC, and firmware capacity.
- Access point capacity and later deployment assumptions.
- Mobile and native build infrastructure: a macOS host, the Android NDK, a
  libwebrtc build host, and test phones.

## Documentation

- The source handoff named the product "Quiet Disco" in section 8. The
  committed `docs/bootstrap.md` normalizes that reference to Project
  Elysium by human decision.
- The handoff cites a temporal audit checklist with categories C1 to C26.
  The checklist text never arrived. Bootstrap section 8 serves as the full
  reference by human decision.
