#include "elysium/pcm/timeline.hpp"

#include <doctest.h>

#include <array>
#include <cstdint>

#include "elysium/pcm/v0/block.hpp"

namespace v0 = elysium::pcm::v0;
using elysium::pcm::TimelineAccountant;
using elysium::pcm::TimelineStep;

namespace {

// One block of interleaved stereo silence. Tests check accounting, not
// audio content.
const std::array<int16_t, v0::kFramesPerBlock * v0::kChannels> kSilence{};

// Builds a valid program block at sequence with a source position.
v0::PcmBlock ProgramBlock(uint64_t sequence, int64_t source_position) {
  v0::PcmBlock block{};
  v0::BlockHeader& header = block.provenance.header;
  header.major_version = v0::kMajorVersion;
  header.minor_version = v0::kMinorVersion;
  header.flags = v0::kCaptureTimeValid | v0::kVenueMappingValid | v0::kSourcePositionValid;
  header.block_sequence = sequence;
  header.stream_epoch = 1;
  header.venue_epoch = 1;
  header.source_position = source_position;
  header.capture_uncertainty_ns = 2'000'000;
  header.sample_rate_hz = v0::kSampleRateHz;
  header.channels = v0::kChannels;
  header.frame_count = v0::kFramesPerBlock;
  block.provenance.scores = {100, 100, 75, 100};
  block.samples = kSilence.data();
  return block;
}

}  // namespace

TEST_CASE("valid program block passes contract validation") {
  CHECK(v0::ValidateBlock(ProgramBlock(0, 0)) == v0::BlockError::kNone);
}

TEST_CASE("contract validation rejects violations") {
  v0::PcmBlock block = ProgramBlock(0, 0);
  v0::BlockError want = v0::BlockError::kNone;

  SUBCASE("unknown major version") {
    block.provenance.header.major_version = 1;
    want = v0::BlockError::kUnknownMajorVersion;
  }
  SUBCASE("frame count other than 10 ms") {
    block.provenance.header.frame_count = 441;
    want = v0::BlockError::kWrongFormat;
  }
  SUBCASE("mono block") {
    block.provenance.header.channels = 1;
    want = v0::BlockError::kWrongFormat;
  }
  SUBCASE("44.1 kHz block") {
    block.provenance.header.sample_rate_hz = 44100;
    want = v0::BlockError::kWrongFormat;
  }
  SUBCASE("missing samples") {
    block.samples = nullptr;
    want = v0::BlockError::kNullSamples;
  }
  SUBCASE("nonzero reserved field") {
    block.provenance.reserved2 = 7;
    want = v0::BlockError::kNonzeroReserved;
  }
  SUBCASE("score outside the D-051 values") {
    block.provenance.scores.webrtc_processing = 60;
    want = v0::BlockError::kInvalidScore;
  }
  SUBCASE("output-frame counts exceed the block") {
    block.provenance.adjustments.inserted_frames = 300;
    block.provenance.adjustments.concealed_frames = 200;
    block.provenance.header.flags |= v0::kContainsSynthetic;
    want = v0::BlockError::kAdjustmentOverflow;
  }
  SUBCASE("concealment without the synthetic flag") {
    block.provenance.adjustments.concealed_frames = 480;
    want = v0::BlockError::kSyntheticNotFlagged;
  }
  SUBCASE("SFU silence without the synthetic flag") {
    block.provenance.adjustments.sfu_synthetic_frames = 480;
    want = v0::BlockError::kSyntheticNotFlagged;
  }
  SUBCASE("holdover without venue mapping") {
    block.provenance.header.flags = v0::kCaptureTimeValid | v0::kMappingHoldover | v0::kSourcePositionValid;
    want = v0::BlockError::kInconsistentTimeFlags;
  }
  SUBCASE("venue mapping without capture time") {
    block.provenance.header.flags = v0::kVenueMappingValid | v0::kSourcePositionValid;
    want = v0::BlockError::kInconsistentTimeFlags;
  }
  SUBCASE("negative uncertainty") {
    block.provenance.header.capture_uncertainty_ns = -1;
    want = v0::BlockError::kNegativeUncertainty;
  }

  CHECK(v0::ValidateBlock(block) == want);
}

TEST_CASE("contiguous program blocks advance output and source positions") {
  TimelineAccountant accountant;

  TimelineStep first = accountant.Account(ProgramBlock(0, 96000));
  TimelineStep second = accountant.Account(ProgramBlock(1, 96480));

  CHECK(first.error == v0::BlockError::kNone);
  CHECK(first.output_position == 0);
  CHECK(first.source_frames == 480);
  CHECK(second.events == 0);
  CHECK(second.output_position == 480);
  CHECK(accountant.totals().output_frames == 960);
  CHECK(accountant.totals().blocks == 2);
}

TEST_CASE("sample adjustments shift the predicted source position") {
  TimelineAccountant accountant;
  v0::PcmBlock accelerated = ProgramBlock(0, 0);
  accelerated.provenance.adjustments.removed_frames = 24;
  v0::PcmBlock expanded = ProgramBlock(1, 504);
  expanded.provenance.adjustments.inserted_frames = 30;

  TimelineStep first = accountant.Account(accelerated);
  TimelineStep second = accountant.Account(expanded);
  TimelineStep third = accountant.Account(ProgramBlock(2, 954));

  CHECK(first.source_frames == 504);
  CHECK(second.events == 0);
  CHECK(second.source_frames == 450);
  CHECK(third.events == 0);
  CHECK(accountant.totals().removed_frames == 24);
  CHECK(accountant.totals().inserted_frames == 30);
}

TEST_CASE("unexplained source position change is a source jump") {
  TimelineAccountant accountant;
  accountant.Account(ProgramBlock(0, 0));

  TimelineStep step = accountant.Account(ProgramBlock(1, 960));

  CHECK((step.events & elysium::pcm::kSourceJump) != 0);
}

TEST_CASE("declared discontinuity explains a source position change") {
  TimelineAccountant accountant;
  accountant.Account(ProgramBlock(0, 0));
  v0::PcmBlock resumed = ProgramBlock(1, 48000);
  resumed.provenance.header.flags |= v0::kDiscontinuityBefore;

  TimelineStep step = accountant.Account(resumed);

  CHECK((step.events & elysium::pcm::kDeclaredDiscontinuity) != 0);
  CHECK((step.events & elysium::pcm::kSourceJump) == 0);
}

TEST_CASE("skipped hand-off sequence is reported") {
  TimelineAccountant accountant;
  accountant.Account(ProgramBlock(0, 0));

  TimelineStep step = accountant.Account(ProgramBlock(3, 480));

  CHECK((step.events & elysium::pcm::kSequenceGap) != 0);
}

TEST_CASE("epoch changes are reported and checked against their flags") {
  TimelineAccountant accountant;
  accountant.Account(ProgramBlock(0, 0));

  SUBCASE("flagged stream epoch change") {
    v0::PcmBlock restarted = ProgramBlock(1, 0);
    restarted.provenance.header.stream_epoch = 2;
    restarted.provenance.header.flags |= v0::kStreamEpochChanged;

    TimelineStep step = accountant.Account(restarted);

    CHECK((step.events & elysium::pcm::kStreamEpochChange) != 0);
    CHECK((step.events & elysium::pcm::kUnflaggedEpochChange) == 0);
    CHECK((step.events & elysium::pcm::kSourceJump) == 0);
  }
  SUBCASE("unflagged venue epoch change") {
    v0::PcmBlock stepped = ProgramBlock(1, 480);
    stepped.provenance.header.venue_epoch = 2;

    TimelineStep step = accountant.Account(stepped);

    CHECK((step.events & elysium::pcm::kVenueEpochChange) != 0);
    CHECK((step.events & elysium::pcm::kUnflaggedEpochChange) != 0);
  }
}

TEST_CASE("synthetic frames are reported and totaled") {
  TimelineAccountant accountant;
  v0::PcmBlock silence = ProgramBlock(0, 0);
  silence.provenance.header.flags |= v0::kContainsSynthetic;
  silence.provenance.header.flags &= ~static_cast<uint32_t>(v0::kSourcePositionValid);
  silence.provenance.adjustments.sfu_synthetic_frames = 480;

  TimelineStep step = accountant.Account(silence);

  CHECK((step.events & elysium::pcm::kSyntheticAudio) != 0);
  CHECK((step.events & elysium::pcm::kSourcePositionUnknown) != 0);
  CHECK(step.source_frames == 0);
  CHECK(accountant.totals().sfu_synthetic_frames == 480);
}

TEST_CASE("invalid block is rejected without moving the timeline") {
  TimelineAccountant accountant;
  accountant.Account(ProgramBlock(0, 0));
  v0::PcmBlock invalid = ProgramBlock(1, 480);
  invalid.provenance.header.frame_count = 960;

  TimelineStep rejected = accountant.Account(invalid);
  TimelineStep next = accountant.Account(ProgramBlock(1, 480));

  CHECK(rejected.error == v0::BlockError::kWrongFormat);
  CHECK(accountant.totals().rejected_blocks == 1);
  CHECK(next.output_position == 480);
  CHECK(next.events == 0);
}
