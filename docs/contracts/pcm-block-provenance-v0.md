# PCM block and provenance contract, version 0

Status: Approved v0 by the active human on 2026-11-13. Implementation
waits for Gate 3 native build infrastructure. Any change to this
interface requires new approval.

## Purpose

The native WebRTC receiver hands decoded PCM to the shared C++ renderer.
Each hand-off carries one PCM block and its full processing provenance
(D-013, D-017, D-018). The renderer reconstructs the playback timeline
from these blocks (D-014) and accounts for every sample change (D-015).

The contract crosses three languages. C++ owns the definition. Swift and
Kotlin pass blocks through without reinterpreting them. Dart never
carries audio blocks (D-013). Dart may read summary diagnostics only.

## Design rules

- Fixed-width integers only. No floating point in timing fields.
- Source sample positions are signed 64-bit counts within a stream epoch.
- Instants are signed 64-bit nanoseconds since the Unix epoch in UTC.
  The venue time epoch qualifies every instant.
- Durations and uncertainties are signed 64-bit nanoseconds.
- Absent values use explicit validity flags, never sentinel numbers.
- The layout uses natural alignment with explicit padding fields.
- Every block starts with a version field. A receiver rejects blocks with
  an unknown major version.

## Block layout (C++ reference)

```cpp
namespace elysium::pcm::v0 {

constexpr uint16_t kMajorVersion = 0;
constexpr uint16_t kMinorVersion = 1;

// Bit flags for BlockHeader::flags.
enum BlockFlag : uint32_t {
  kCaptureTimeValid      = 1u << 0,  // capture_utc_ns holds a value
  kVenueMappingValid     = 1u << 1,  // gateway mapped capture to venue UTC
  kMappingHoldover       = 1u << 2,  // mapping came from holdover (D-010)
  kSourcePositionValid   = 1u << 3,  // source_position holds a value
  kDiscontinuityBefore   = 1u << 4,  // timeline break precedes this block
  kStreamEpochChanged    = 1u << 5,  // first block of a new stream epoch
  kVenueEpochChanged     = 1u << 6,  // venue time stepped before this block
  kContainsSynthetic     = 1u << 7,  // any frame not from program audio
  kOutOfOrder            = 1u << 8,  // decode order differs from capture order
};

// Every block holds exactly 10 ms: 480 frames at 48 kHz, matching the
// libwebrtc playout pull.
constexpr uint32_t kFramesPerBlock = 480;

struct BlockHeader {
  uint16_t major_version;        // kMajorVersion
  uint16_t minor_version;        // kMinorVersion
  uint32_t flags;                // BlockFlag bits

  uint64_t block_sequence;       // receiver hand-off counter, starts at 0
  uint64_t stream_epoch;         // source stream epoch (rtptime)
  uint64_t venue_epoch;          // venue time epoch (venuetime)

  int64_t  source_position;      // first source sample of the block
  int64_t  capture_utc_ns;       // venue UTC of the first source sample
  int64_t  capture_uncertainty_ns;
  int64_t  receiver_monotonic_ns; // receiver monotonic clock at hand-off

  uint32_t sample_rate_hz;       // 48000 for the program feed
  uint16_t channels;             // 2 for the program feed
  uint16_t reserved0;            // zero
  uint32_t frame_count;          // always kFramesPerBlock; receivers reject others
  uint32_t reserved1;            // zero
};

// Counts of frames that WebRTC changed inside this block (D-015). Each
// count is in output frames at sample_rate_hz.
struct SampleAdjustments {
  uint32_t inserted_frames;      // NetEq expand or preemptive expand
  uint32_t removed_frames;       // NetEq accelerate
  uint32_t concealed_frames;     // packet loss concealment
  uint32_t comfort_noise_frames; // CNG output
  uint32_t resampled_from_hz;    // 0 when no resampling occurred
  uint32_t sfu_synthetic_frames; // frames decoded from SFU-injected silence
};

// Timing confidence scores per stage (D-050, D-051). Each score is one
// of 100, 75, 50, or 25, meaning 1.00, 0.75, 0.50, or 0.25. Zero marks
// an invalid stage.
struct StageScores {
  uint8_t capture;
  uint8_t gateway_mapping;
  uint8_t webrtc_processing;
  uint8_t native_output;
};

struct Provenance {
  BlockHeader header;
  SampleAdjustments adjustments;
  StageScores scores;
  uint32_t reserved2;            // zero
};

// One logical block (D-017). The PCM buffer holds frame_count * channels
// interleaved signed 16-bit samples as libwebrtc delivers them. The
// renderer converts to float internally where WSOLA needs it. The receiver owns the buffer until
// the renderer releases it.
struct PcmBlock {
  Provenance provenance;
  const int16_t* samples;
};

}  // namespace elysium::pcm::v0
```

## Field semantics

- `source_position` comes from the gateway timeline. LiveKit rewrites
  RTP timestamps per subscriber. The receiver therefore derives the
  position from abs-capture-time, not from the received RTP timestamp.
- `capture_utc_ns` comes from abs-capture-time after the gateway mapping.
  Gate 1 step 3 verified that LiveKit forwards abs-capture-time unchanged
  in its default configuration.
- `kContainsSynthetic` marks any frame not decoded from program audio:
  concealment, comfort noise, or SFU-injected silence frames.
- `kDiscontinuityBefore` marks a break the renderer must not bridge with
  time stretching (D-019, D-027).
- Weight derivation follows D-049: the minimum stage score, bounded to
  0.25 to 1.00. Invalid stages exclude the observation.

## Compatibility plan

- Minor versions add fields only in reserved space or at the end of
  `Provenance`. Receivers ignore unknown trailing fields.
- A major version change requires renderer and receiver updates in the
  same release on both platforms.
- Swift and Kotlin bridges carry `Provenance` by value and the sample
  buffer by pointer or direct `ByteBuffer`. Neither bridge copies PCM.
- A compile-time `static_assert` fixes `sizeof(Provenance)` per version.

## Flutter diagnostics

Dart never receives PCM or per-block provenance. Once per second the
native layer sends Dart a rolling summary over the platform bridge:

- current capture-to-output latency estimate and its uncertainty
- minimum stage score over the last second
- recovery state (normal, gradual correction, major recovery, degraded)
- counts of synthetic, inserted, and removed frames over the last second

## Resolved decisions (2026-11-13)

| Question | Decision |
| --- | --- |
| Sample format | Interleaved signed 16-bit, as libwebrtc delivers |
| Block size | Fixed 10 ms, 480 frames at 48 kHz |
| Sample change detail | Per-block counts per category |
| Score encoding | Integer percent: 100, 75, 50, 25, and 0 for invalid |
| Dart diagnostics | Rolling one-second summary only |
