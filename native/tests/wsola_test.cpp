#include "elysium/dsp/wsola.hpp"

#include <doctest.h>

#include <cmath>
#include <cstddef>
#include <cstdint>
#include <numbers>
#include <optional>
#include <vector>

using elysium::dsp::IsAllowedSpeedPpm;
using elysium::dsp::Wsola;
using elysium::dsp::WsolaConfig;

namespace {

// Test-only configuration. The window and search sizes are experimental.
constexpr uint32_t kRate = 48000;
constexpr uint16_t kStereo = 2;
constexpr uint32_t kBlockFrames = 480;  // one 10 ms playout pull

WsolaConfig TestConfig() {
  return WsolaConfig{
      .sample_rate_hz = kRate,
      .channels = kStereo,
      .window_frames = 960,  // 20 ms
      .search_frames = 240,  // 5 ms
      .max_push_frames = kBlockFrames,
  };
}

Wsola MakeWsola(uint32_t speed_ppm) {
  std::optional<Wsola> wsola = Wsola::Create(TestConfig());
  REQUIRE(wsola.has_value());
  REQUIRE(wsola->SetSpeedPpm(speed_ppm));
  return *wsola;
}

// Interleaved stereo sine. The right channel optionally inverts the left.
std::vector<float> StereoSine(double hz, size_t frames, bool invert_right) {
  std::vector<float> samples(frames * kStereo);
  for (size_t i = 0; i < frames; ++i) {
    const double phase = 2.0 * std::numbers::pi * hz * static_cast<double>(i) / kRate;
    const auto left = static_cast<float>(0.5 * std::sin(phase));
    samples[i * kStereo] = left;
    samples[i * kStereo + 1] = invert_right ? -left : left;
  }
  return samples;
}

// Streams input through wsola in 10 ms blocks and returns all output.
std::vector<float> Run(Wsola& wsola, const std::vector<float>& input) {
  std::vector<float> output;
  std::vector<float> pulled(4 * kBlockFrames * kStereo);
  const size_t frames = input.size() / kStereo;
  for (size_t start = 0; start < frames; start += kBlockFrames) {
    const size_t count = std::min<size_t>(kBlockFrames, frames - start);
    const std::span<const float> block(input.data() + start * kStereo, count * kStereo);
    REQUIRE(wsola.Push(block) == count);
    size_t got = 0;
    while ((got = wsola.Pull(pulled)) > 0) {
      output.insert(output.end(), pulled.begin(), pulled.begin() + static_cast<std::ptrdiff_t>(got * kStereo));
    }
  }
  return output;
}

// Estimates the frequency of the left channel from upward zero
// crossings between first and last frame.
double LeftFrequency(const std::vector<float>& samples, size_t first, size_t last) {
  size_t crossings = 0;
  size_t first_crossing = 0;
  size_t last_crossing = 0;
  for (size_t i = first + 1; i < last; ++i) {
    if (samples[(i - 1) * kStereo] < 0.0f && samples[i * kStereo] >= 0.0f) {
      if (crossings == 0) {
        first_crossing = i;
      }
      last_crossing = i;
      ++crossings;
    }
  }
  REQUIRE(crossings > 1);
  const double cycles = static_cast<double>(crossings - 1);
  return cycles * kRate / static_cast<double>(last_crossing - first_crossing);
}

}  // namespace

TEST_CASE("speed limits follow D-025") {
  CHECK(IsAllowedSpeedPpm(1'000'000));
  CHECK(IsAllowedSpeedPpm(1'010'000));
  CHECK(IsAllowedSpeedPpm(1'050'000));
  CHECK_FALSE(IsAllowedSpeedPpm(1'005'000));
  CHECK_FALSE(IsAllowedSpeedPpm(1'050'001));
  CHECK_FALSE(IsAllowedSpeedPpm(990'000));
}

TEST_CASE("set speed rejects values outside the safety limits") {
  Wsola wsola = MakeWsola(1'030'000);

  CHECK_FALSE(wsola.SetSpeedPpm(1'060'000));

  CHECK(wsola.speed_ppm() == 1'030'000);
}

TEST_CASE("normal speed reconstructs the input after the start-up ramp") {
  Wsola wsola = MakeWsola(1'000'000);
  const std::vector<float> input = StereoSine(440.0, kRate, false);

  const std::vector<float> output = Run(wsola, input);

  // The first hop fades in from a single window. Later frames sum two
  // periodic-Hann windows to unity.
  const size_t hop = wsola.hop_frames();
  REQUIRE(output.size() > 4 * hop * kStereo);
  float max_error = 0.0f;
  for (size_t i = hop * kStereo; i < output.size(); ++i) {
    max_error = std::max(max_error, std::fabs(output[i] - input[i]));
  }
  CHECK(max_error < 1e-5f);
  CHECK(wsola.removed_frames() == 0);
}

TEST_CASE("acceleration preserves pitch") {
  for (uint32_t speed : {1'010'000u, 1'030'000u, 1'050'000u}) {
    CAPTURE(speed);
    Wsola wsola = MakeWsola(speed);
    const std::vector<float> input = StereoSine(440.0, 2 * kRate, false);

    const std::vector<float> output = Run(wsola, input);

    const size_t frames = output.size() / kStereo;
    const double hz = LeftFrequency(output, frames / 4, frames - frames / 4);
    // Test-only tolerance. Plain resampling at 1.05 would read 462 Hz.
    CHECK(std::fabs(hz - 440.0) / 440.0 < 0.005);
  }
}

TEST_CASE("sample accounting is exact at the configured speed") {
  Wsola wsola = MakeWsola(1'050'000);
  const std::vector<float> input = StereoSine(440.0, 2 * kRate, false);

  const std::vector<float> output = Run(wsola, input);

  const uint64_t produced = wsola.produced_output_frames();
  CHECK(produced == output.size() / kStereo);
  CHECK(produced % wsola.hop_frames() == 0);
  CHECK(wsola.consumed_input_frames() == produced * 1'050'000 / 1'000'000);
  CHECK(wsola.removed_frames() == wsola.consumed_input_frames() - produced);
  // All but the buffered tail of the input reaches the output timeline.
  const uint64_t pushed = 2 * kRate;
  const uint64_t tail = TestConfig().window_frames + TestConfig().search_frames + wsola.hop_frames();
  CHECK(wsola.consumed_input_frames() + tail >= pushed);
}

TEST_CASE("speed change applies to later segments") {
  Wsola wsola = MakeWsola(1'000'000);
  const std::vector<float> input = StereoSine(440.0, kRate, false);
  Run(wsola, input);
  const uint64_t produced_before = wsola.produced_output_frames();
  const uint64_t consumed_before = wsola.consumed_input_frames();

  REQUIRE(wsola.SetSpeedPpm(1'020'000));
  Run(wsola, input);

  const uint64_t produced = wsola.produced_output_frames() - produced_before;
  const uint64_t consumed = wsola.consumed_input_frames() - consumed_before;
  CHECK(consumed == produced * 1'020'000 / 1'000'000);
}

TEST_CASE("every channel uses the same segment shift") {
  Wsola wsola = MakeWsola(1'040'000);
  const std::vector<float> input = StereoSine(523.25, kRate, true);

  const std::vector<float> output = Run(wsola, input);

  REQUIRE_FALSE(output.empty());
  bool mirrored = true;
  for (size_t i = 0; i + 1 < output.size(); i += kStereo) {
    mirrored = mirrored && output[i + 1] == -output[i];
  }
  CHECK(mirrored);
}

TEST_CASE("push accepts at most the configured frame count") {
  Wsola wsola = MakeWsola(1'000'000);
  const std::vector<float> input = StereoSine(440.0, 2 * kBlockFrames, false);

  CHECK(wsola.Push(input) == kBlockFrames);
}

TEST_CASE("create rejects invalid configuration") {
  WsolaConfig config = TestConfig();

  SUBCASE("zero sample rate") { config.sample_rate_hz = 0; }
  SUBCASE("zero channels") { config.channels = 0; }
  SUBCASE("odd window") { config.window_frames = 961; }
  SUBCASE("tiny window") { config.window_frames = 2; }
  SUBCASE("search beyond half window") { config.search_frames = 481; }
  SUBCASE("zero push size") { config.max_push_frames = 0; }

  CHECK_FALSE(Wsola::Create(config).has_value());
}
