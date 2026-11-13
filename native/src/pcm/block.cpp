#include "elysium/pcm/v0/block.hpp"

namespace elysium::pcm::v0 {

namespace {

// Approved D-051 score values in integer percent. Zero marks an invalid
// stage.
bool IsValidScore(uint8_t score) noexcept {
  return score == 0 || score == 25 || score == 50 || score == 75 || score == 100;
}

bool HasFlag(uint32_t flags, BlockFlag flag) noexcept { return (flags & flag) != 0; }

bool ScoresValid(const StageScores& scores) noexcept {
  return IsValidScore(scores.capture) && IsValidScore(scores.gateway_mapping) &&
         IsValidScore(scores.webrtc_processing) && IsValidScore(scores.native_output);
}

// Sums the categories that occupy output frames. Removed frames are
// absent from the output and do not count.
uint64_t OutputAdjustedFrames(const SampleAdjustments& adjustments) noexcept {
  return uint64_t{adjustments.inserted_frames} + adjustments.concealed_frames + adjustments.comfort_noise_frames +
         adjustments.sfu_synthetic_frames;
}

uint64_t SyntheticFrames(const SampleAdjustments& adjustments) noexcept {
  return uint64_t{adjustments.concealed_frames} + adjustments.comfort_noise_frames +
         adjustments.sfu_synthetic_frames;
}

BlockError ValidateHeader(const BlockHeader& header) noexcept {
  if (header.major_version != kMajorVersion) {
    return BlockError::kUnknownMajorVersion;
  }
  if (header.sample_rate_hz != kSampleRateHz || header.channels != kChannels ||
      header.frame_count != kFramesPerBlock) {
    return BlockError::kWrongFormat;
  }
  if (header.reserved0 != 0 || header.reserved1 != 0) {
    return BlockError::kNonzeroReserved;
  }
  const bool capture = HasFlag(header.flags, kCaptureTimeValid);
  const bool mapping = HasFlag(header.flags, kVenueMappingValid);
  if ((mapping && !capture) || (HasFlag(header.flags, kMappingHoldover) && !mapping)) {
    return BlockError::kInconsistentTimeFlags;
  }
  if (capture && header.capture_uncertainty_ns < 0) {
    return BlockError::kNegativeUncertainty;
  }
  return BlockError::kNone;
}

}  // namespace

BlockError ValidateBlock(const PcmBlock& block) noexcept {
  const Provenance& provenance = block.provenance;
  const BlockError header_error = ValidateHeader(provenance.header);
  if (header_error != BlockError::kNone) {
    return header_error;
  }
  if (block.samples == nullptr) {
    return BlockError::kNullSamples;
  }
  if (provenance.reserved2 != 0) {
    return BlockError::kNonzeroReserved;
  }
  if (!ScoresValid(provenance.scores)) {
    return BlockError::kInvalidScore;
  }
  if (OutputAdjustedFrames(provenance.adjustments) > provenance.header.frame_count) {
    return BlockError::kAdjustmentOverflow;
  }
  if (SyntheticFrames(provenance.adjustments) > 0 && !HasFlag(provenance.header.flags, kContainsSynthetic)) {
    return BlockError::kSyntheticNotFlagged;
  }
  return BlockError::kNone;
}

}  // namespace elysium::pcm::v0
