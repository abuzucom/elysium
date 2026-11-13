#include "elysium/pcm/timeline.hpp"

namespace elysium::pcm {

namespace {

bool HasFlag(uint32_t flags, v0::BlockFlag flag) noexcept { return (flags & flag) != 0; }

// Source frames a block represents (D-015). Output frames include
// inserted and synthetic frames, which carry no source audio. Removed
// frames left the output but consumed source audio. The categories are
// treated as disjoint, as docs/backlog.md records.
int64_t SourceFrames(const v0::Provenance& provenance) noexcept {
  const v0::SampleAdjustments& adjustments = provenance.adjustments;
  if (!HasFlag(provenance.header.flags, v0::kSourcePositionValid)) {
    return 0;
  }
  return int64_t{provenance.header.frame_count} - adjustments.inserted_frames - adjustments.concealed_frames -
         adjustments.comfort_noise_frames - adjustments.sfu_synthetic_frames + adjustments.removed_frames;
}

}  // namespace

TimelineStep TimelineAccountant::Account(const v0::PcmBlock& block) noexcept {
  TimelineStep step;
  step.output_position = totals_.output_frames;
  step.error = v0::ValidateBlock(block);
  if (step.error != v0::BlockError::kNone) {
    ++totals_.rejected_blocks;
    return step;
  }
  const v0::BlockHeader& header = block.provenance.header;
  if (has_previous_ && header.block_sequence != next_sequence_) {
    step.events |= kSequenceGap;
  }
  if (HasFlag(header.flags, v0::kDiscontinuityBefore)) {
    step.events |= kDeclaredDiscontinuity;
  }
  if (HasFlag(header.flags, v0::kContainsSynthetic)) {
    step.events |= kSyntheticAudio;
  }
  step.events |= ObserveEpochs(header);
  step.events |= ObserveSource(header);
  step.source_frames = SourceFrames(block.provenance);
  Record(block.provenance, step.source_frames);
  return step;
}

uint32_t TimelineAccountant::ObserveEpochs(const v0::BlockHeader& header) const noexcept {
  if (!has_previous_) {
    return 0;
  }
  uint32_t events = 0;
  if (header.stream_epoch != stream_epoch_) {
    events |= kStreamEpochChange;
    if (!HasFlag(header.flags, v0::kStreamEpochChanged)) {
      events |= kUnflaggedEpochChange;
    }
  }
  if (header.venue_epoch != venue_epoch_) {
    events |= kVenueEpochChange;
    if (!HasFlag(header.flags, v0::kVenueEpochChanged)) {
      events |= kUnflaggedEpochChange;
    }
  }
  return events;
}

uint32_t TimelineAccountant::ObserveSource(const v0::BlockHeader& header) const noexcept {
  if (!HasFlag(header.flags, v0::kSourcePositionValid)) {
    return kSourcePositionUnknown;
  }
  // A new stream epoch or a declared break restarts the prediction.
  const bool expected_break =
      header.stream_epoch != stream_epoch_ || HasFlag(header.flags, v0::kDiscontinuityBefore);
  if (has_next_source_ && !expected_break && header.source_position != next_source_position_) {
    return kSourceJump;
  }
  return 0;
}

void TimelineAccountant::Record(const v0::Provenance& provenance, int64_t source_frames) noexcept {
  const v0::BlockHeader& header = provenance.header;
  const v0::SampleAdjustments& adjustments = provenance.adjustments;
  has_previous_ = true;
  next_sequence_ = header.block_sequence + 1;
  stream_epoch_ = header.stream_epoch;
  venue_epoch_ = header.venue_epoch;
  has_next_source_ = HasFlag(header.flags, v0::kSourcePositionValid);
  if (has_next_source_) {
    next_source_position_ = header.source_position + source_frames;
  }
  ++totals_.blocks;
  totals_.output_frames += header.frame_count;
  totals_.inserted_frames += adjustments.inserted_frames;
  totals_.removed_frames += adjustments.removed_frames;
  totals_.concealed_frames += adjustments.concealed_frames;
  totals_.comfort_noise_frames += adjustments.comfort_noise_frames;
  totals_.sfu_synthetic_frames += adjustments.sfu_synthetic_frames;
}

}  // namespace elysium::pcm
