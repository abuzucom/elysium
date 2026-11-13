// PCM block and provenance contract, version 0 (approved 2026-11-13).
//
// This header is the C++ reference definition of
// docs/contracts/pcm-block-provenance-v0.md. Any change to the layout or
// semantics requires new human approval.
#ifndef ELYSIUM_PCM_V0_BLOCK_HPP
#define ELYSIUM_PCM_V0_BLOCK_HPP

#include <cstddef>
#include <cstdint>

namespace elysium::pcm::v0 {

constexpr uint16_t kMajorVersion = 0;
constexpr uint16_t kMinorVersion = 1;

// Every block holds exactly 10 ms: 480 frames at 48 kHz, matching the
// libwebrtc playout pull.
constexpr uint32_t kFramesPerBlock = 480;
constexpr uint32_t kSampleRateHz = 48000;
constexpr uint16_t kChannels = 2;

// Bit flags for BlockHeader::flags.
enum BlockFlag : uint32_t {
  kCaptureTimeValid = 1u << 0,      // capture_utc_ns holds a value
  kVenueMappingValid = 1u << 1,     // gateway mapped capture to venue UTC
  kMappingHoldover = 1u << 2,       // mapping came from holdover (D-010)
  kSourcePositionValid = 1u << 3,   // source_position holds a value
  kDiscontinuityBefore = 1u << 4,   // timeline break precedes this block
  kStreamEpochChanged = 1u << 5,    // first block of a new stream epoch
  kVenueEpochChanged = 1u << 6,     // venue time stepped before this block
  kContainsSynthetic = 1u << 7,     // any frame not from program audio
  kOutOfOrder = 1u << 8,            // decode order differs from capture order
};

struct BlockHeader {
  uint16_t major_version;  // kMajorVersion
  uint16_t minor_version;  // kMinorVersion
  uint32_t flags;          // BlockFlag bits

  uint64_t block_sequence;  // receiver hand-off counter, starts at 0
  uint64_t stream_epoch;    // source stream epoch (rtptime)
  uint64_t venue_epoch;     // venue time epoch (venuetime)

  int64_t source_position;  // first source sample of the block
  int64_t capture_utc_ns;   // venue UTC of the first source sample
  int64_t capture_uncertainty_ns;
  int64_t receiver_monotonic_ns;  // receiver monotonic clock at hand-off

  uint32_t sample_rate_hz;  // kSampleRateHz
  uint16_t channels;        // kChannels
  uint16_t reserved0;       // zero
  uint32_t frame_count;     // always kFramesPerBlock; receivers reject others
  uint32_t reserved1;       // zero
};

// Counts of frames that WebRTC changed inside this block (D-015). Each
// count is in output frames at sample_rate_hz.
struct SampleAdjustments {
  uint32_t inserted_frames;       // NetEq expand or preemptive expand
  uint32_t removed_frames;        // NetEq accelerate
  uint32_t concealed_frames;      // packet loss concealment
  uint32_t comfort_noise_frames;  // CNG output
  uint32_t resampled_from_hz;     // 0 when no resampling occurred
  uint32_t sfu_synthetic_frames;  // frames decoded from SFU-injected silence
};

// Timing confidence scores per stage (D-050, D-051) in integer percent:
// 100, 75, 50, or 25. Zero marks an invalid stage.
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
  uint32_t reserved2;  // zero
};

// One logical block (D-017). The PCM buffer holds frame_count * channels
// interleaved signed 16-bit samples as libwebrtc delivers them. The
// receiver owns the buffer until the renderer releases it.
struct PcmBlock {
  Provenance provenance;
  const int16_t* samples;
};

// Layout checks fix the v0 wire layout shared with Swift and Kotlin.
static_assert(sizeof(BlockHeader) == 80);
static_assert(offsetof(BlockHeader, block_sequence) == 8);
static_assert(offsetof(BlockHeader, source_position) == 32);
static_assert(offsetof(BlockHeader, sample_rate_hz) == 64);
static_assert(offsetof(BlockHeader, frame_count) == 72);
static_assert(sizeof(SampleAdjustments) == 24);
static_assert(sizeof(StageScores) == 4);
static_assert(offsetof(Provenance, adjustments) == 80);
static_assert(offsetof(Provenance, scores) == 104);
static_assert(sizeof(Provenance) == 112);
static_assert(alignof(Provenance) == 8);

// Reasons a block fails contract validation.
enum class BlockError {
  kNone,
  kUnknownMajorVersion,
  kWrongFormat,            // sample rate, channels, or frame count
  kNullSamples,
  kNonzeroReserved,
  kInvalidScore,           // a score outside {0, 25, 50, 75, 100}
  kAdjustmentOverflow,     // output-frame counts exceed the block
  kSyntheticNotFlagged,    // synthetic frames without kContainsSynthetic
  kInconsistentTimeFlags,  // mapping or holdover without capture time
  kNegativeUncertainty,
};

// Validates block against the v0 contract. It never allocates or throws.
BlockError ValidateBlock(const PcmBlock& block) noexcept;

}  // namespace elysium::pcm::v0

#endif  // ELYSIUM_PCM_V0_BLOCK_HPP
