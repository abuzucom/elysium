// Sample-based playback timeline accounting (D-014, D-015).
//
// TimelineAccountant consumes validated v0 PCM blocks in hand-off order.
// It tracks the native output position, predicts the next source
// position from per-block sample adjustments, and reports timeline
// events. It never allocates or throws, so the real-time audio thread may
// call it.
#ifndef ELYSIUM_PCM_TIMELINE_HPP
#define ELYSIUM_PCM_TIMELINE_HPP

#include <cstdint>

#include "elysium/pcm/v0/block.hpp"

namespace elysium::pcm {

// Bit flags for TimelineStep::events.
enum TimelineEvent : uint32_t {
  kSequenceGap = 1u << 0,             // block_sequence skipped values
  kStreamEpochChange = 1u << 1,       // stream epoch differs from previous
  kVenueEpochChange = 1u << 2,        // venue epoch differs from previous
  kUnflaggedEpochChange = 1u << 3,    // epoch changed without its block flag
  kDeclaredDiscontinuity = 1u << 4,   // block carried kDiscontinuityBefore
  kSourceJump = 1u << 5,              // source position missed the prediction
  kSyntheticAudio = 1u << 6,          // block contains synthetic frames
  kSourcePositionUnknown = 1u << 7,   // block lacks a valid source position
};

struct TimelineStep {
  v0::BlockError error = v0::BlockError::kNone;
  uint32_t events = 0;
  // Native output position of the first frame of the block. Rejected
  // blocks do not advance the output position.
  uint64_t output_position = 0;
  // Source frames the block represents after removing inserted and
  // synthetic frames and adding back removed frames.
  int64_t source_frames = 0;
};

struct TimelineTotals {
  uint64_t blocks = 0;
  uint64_t rejected_blocks = 0;
  uint64_t output_frames = 0;
  uint64_t inserted_frames = 0;
  uint64_t removed_frames = 0;
  uint64_t concealed_frames = 0;
  uint64_t comfort_noise_frames = 0;
  uint64_t sfu_synthetic_frames = 0;
};

class TimelineAccountant {
 public:
  // Accounts for one block. Invalid blocks report their error, count as
  // rejected, and leave the timeline unchanged.
  TimelineStep Account(const v0::PcmBlock& block) noexcept;

  [[nodiscard]] const TimelineTotals& totals() const noexcept { return totals_; }

 private:
  uint32_t ObserveEpochs(const v0::BlockHeader& header) const noexcept;
  uint32_t ObserveSource(const v0::BlockHeader& header) const noexcept;
  void Record(const v0::Provenance& provenance, int64_t source_frames) noexcept;

  TimelineTotals totals_;
  bool has_previous_ = false;
  uint64_t next_sequence_ = 0;
  uint64_t stream_epoch_ = 0;
  uint64_t venue_epoch_ = 0;
  bool has_next_source_ = false;
  int64_t next_source_position_ = 0;
};

}  // namespace elysium::pcm

#endif  // ELYSIUM_PCM_TIMELINE_HPP
