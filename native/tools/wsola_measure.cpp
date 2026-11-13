// Offline WSOLA measurements (Gate 3). Prints a Markdown report to
// stdout. All signals are synthetic test-only substitutes for program
// audio. Timing reflects the host that runs the tool, not a phone.
#include <algorithm>
#include <chrono>
#include <cmath>
#include <cstdint>
#include <cstdio>
#include <numbers>
#include <optional>
#include <vector>

#include "elysium/dsp/wsola.hpp"

namespace {

using elysium::dsp::Wsola;
using elysium::dsp::WsolaConfig;

constexpr uint32_t kRate = 48000;
constexpr uint16_t kChannels = 2;
constexpr uint32_t kBlockFrames = 480;
constexpr uint32_t kSignalSeconds = 60;
// Experimental prototype parameters (docs/backlog.md).
constexpr uint32_t kWindowFrames = 960;
constexpr uint32_t kSearchFrames = 240;
// One click every 100 ms for the transient check.
constexpr uint32_t kClickSpacingFrames = 4800;
constexpr float kClickThreshold = 0.5f;
constexpr uint32_t kSpeeds[] = {1'000'000, 1'010'000, 1'030'000, 1'050'000};

using Signal = std::vector<float>;

Signal Sine(double hz, uint32_t frames) {
  Signal s(size_t{frames} * kChannels);
  for (uint32_t i = 0; i < frames; ++i) {
    const auto v = static_cast<float>(0.5 * std::sin(2.0 * std::numbers::pi * hz * i / kRate));
    s[size_t{i} * kChannels] = v;
    s[size_t{i} * kChannels + 1] = v;
  }
  return s;
}

// Harmonic tone with slow vibrato and a different right channel, a
// stand-in for program audio in the timing run.
Signal Harmonic(uint32_t frames) {
  Signal s(size_t{frames} * kChannels);
  double phase = 0.0;
  for (uint32_t i = 0; i < frames; ++i) {
    const double t = static_cast<double>(i) / kRate;
    const double hz = 220.0 * (1.0 + 0.01 * std::sin(2.0 * std::numbers::pi * 5.0 * t));
    phase += 2.0 * std::numbers::pi * hz / kRate;
    double left = 0.0;
    for (int h = 1; h <= 6; ++h) {
      left += std::sin(phase * h) / (2.0 * h);
    }
    s[size_t{i} * kChannels] = static_cast<float>(0.5 * left);
    s[size_t{i} * kChannels + 1] = static_cast<float>(0.4 * left + 0.1 * std::sin(phase * 1.5));
  }
  return s;
}

Signal Clicks(uint32_t frames) {
  Signal s(size_t{frames} * kChannels, 0.0f);
  for (uint32_t i = kClickSpacingFrames / 2; i < frames; i += kClickSpacingFrames) {
    s[size_t{i} * kChannels] = 1.0f;
    s[size_t{i} * kChannels + 1] = 1.0f;
  }
  return s;
}

struct RunResult {
  Signal output;
  std::vector<double> block_micros;
  uint64_t consumed = 0;
  uint64_t produced = 0;
  uint64_t removed = 0;
  uint64_t pushed = 0;
  uint64_t first_output_after = 0;
};

std::optional<RunResult> Run(uint32_t speed_ppm, const Signal& input) {
  std::optional<Wsola> wsola = Wsola::Create(WsolaConfig{.sample_rate_hz = kRate,
                                                         .channels = kChannels,
                                                         .window_frames = kWindowFrames,
                                                         .search_frames = kSearchFrames,
                                                         .max_push_frames = kBlockFrames});
  if (!wsola.has_value() || !wsola->SetSpeedPpm(speed_ppm)) {
    return std::nullopt;
  }
  RunResult result;
  Signal pulled(size_t{4} * kBlockFrames * kChannels);
  const size_t frames = input.size() / kChannels;
  for (size_t start = 0; start + kBlockFrames <= frames; start += kBlockFrames) {
    // Time only Push and one Pull into a preallocated buffer. Appending
    // to the growing output vector allocates and stays outside the timer.
    const auto began = std::chrono::steady_clock::now();
    wsola->Push(std::span<const float>(input.data() + start * kChannels, size_t{kBlockFrames} * kChannels));
    const size_t got = wsola->Pull(pulled);
    const auto ended = std::chrono::steady_clock::now();
    result.block_micros.push_back(std::chrono::duration<double, std::micro>(ended - began).count());
    result.output.insert(result.output.end(), pulled.begin(),
                         pulled.begin() + static_cast<std::ptrdiff_t>(got * kChannels));
    const size_t block_out = got;
    result.pushed += kBlockFrames;
    if (result.first_output_after == 0 && block_out > 0) {
      result.first_output_after = result.pushed;
    }
  }
  // Drain output that one Pull per block left behind.
  size_t got = 0;
  while ((got = wsola->Pull(pulled)) > 0) {
    result.output.insert(result.output.end(), pulled.begin(), pulled.begin() + static_cast<std::ptrdiff_t>(got * kChannels));
  }
  result.consumed = wsola->consumed_input_frames();
  result.produced = wsola->produced_output_frames();
  result.removed = wsola->removed_frames();
  return result;
}

double Frequency(const Signal& s) {
  const size_t frames = s.size() / kChannels;
  size_t crossings = 0;
  size_t first = 0;
  size_t last = 0;
  for (size_t i = frames / 4 + 1; i < frames - frames / 4; ++i) {
    if (s[(i - 1) * kChannels] < 0.0f && s[i * kChannels] >= 0.0f) {
      first = crossings == 0 ? i : first;
      last = i;
      ++crossings;
    }
  }
  if (crossings < 2 || last == first) {
    return 0.0;
  }
  return static_cast<double>(crossings - 1) * kRate / static_cast<double>(last - first);
}

// Counts output frames that start a click: above threshold after a
// frame below it.
size_t CountClicks(const Signal& s) {
  size_t count = 0;
  for (size_t i = 1; i < s.size() / kChannels; ++i) {
    if (s[i * kChannels] >= kClickThreshold && s[(i - 1) * kChannels] < kClickThreshold) {
      ++count;
    }
  }
  return count;
}

double Percentile(std::vector<double> values, double fraction) {
  std::sort(values.begin(), values.end());
  const auto index = static_cast<size_t>(fraction * static_cast<double>(values.size() - 1));
  return values[index];
}

void ReportTiming(const Signal& harmonic) {
  std::printf("## Processing time per 10 ms block (harmonic stereo signal, %u s)\n\n", kSignalSeconds);
  std::printf("| Speed | Mean us | p99 us | Max us | Budget share at p99 |\n| --- | --- | --- | --- | --- |\n");
  for (uint32_t speed : kSpeeds) {
    std::optional<RunResult> r = Run(speed, harmonic);
    if (!r.has_value()) {
      continue;
    }
    double sum = 0.0;
    for (double v : r->block_micros) {
      sum += v;
    }
    const double mean = sum / static_cast<double>(r->block_micros.size());
    const double p99 = Percentile(r->block_micros, 0.99);
    const double max = *std::max_element(r->block_micros.begin(), r->block_micros.end());
    std::printf("| %.2f | %.1f | %.1f | %.1f | %.2f%% |\n", speed / 1e6, mean, p99, max, p99 / 100.0);
  }
}

void ReportAccounting(const Signal& harmonic) {
  std::printf("\n## Sample accounting and buffering\n\n");
  std::printf("| Speed | Pushed | Consumed | Produced | Removed | Removed / consumed | Expected | Buffered input | First output after |\n");
  std::printf("| --- | --- | --- | --- | --- | --- | --- | --- | --- |\n");
  for (uint32_t speed : kSpeeds) {
    std::optional<RunResult> r = Run(speed, harmonic);
    if (!r.has_value()) {
      continue;
    }
    const double ratio = r->consumed == 0 ? 0.0 : static_cast<double>(r->removed) / static_cast<double>(r->consumed);
    const double expected = 1.0 - 1e6 / static_cast<double>(speed);
    std::printf("| %.2f | %llu | %llu | %llu | %llu | %.5f | %.5f | %llu frames | %llu frames |\n", speed / 1e6,
                static_cast<unsigned long long>(r->pushed), static_cast<unsigned long long>(r->consumed),
                static_cast<unsigned long long>(r->produced), static_cast<unsigned long long>(r->removed), ratio,
                expected, static_cast<unsigned long long>(r->pushed - r->consumed),
                static_cast<unsigned long long>(r->first_output_after));
  }
}

void ReportPitch() {
  std::printf("\n## Pitch preservation (sine, 10 s)\n\n");
  std::printf("| Speed | 440 Hz in -> out | Error | 1000 Hz in -> out | Error | Resampling would give (440 Hz) |\n");
  std::printf("| --- | --- | --- | --- | --- | --- |\n");
  const Signal low = Sine(440.0, 10 * kRate);
  const Signal high = Sine(1000.0, 10 * kRate);
  for (uint32_t speed : kSpeeds) {
    std::optional<RunResult> a = Run(speed, low);
    std::optional<RunResult> b = Run(speed, high);
    if (!a.has_value() || !b.has_value()) {
      continue;
    }
    const double fa = Frequency(a->output);
    const double fb = Frequency(b->output);
    std::printf("| %.2f | %.2f Hz | %.3f%% | %.2f Hz | %.3f%% | %.2f Hz |\n", speed / 1e6, fa,
                100.0 * std::fabs(fa - 440.0) / 440.0, fb, 100.0 * std::fabs(fb - 1000.0) / 1000.0,
                440.0 * speed / 1e6);
  }
}

void ReportTransients() {
  std::printf("\n## Transients (click every 100 ms, 10 s)\n\n");
  std::printf("| Speed | Clicks in consumed input | Clicks in output | Difference |\n| --- | --- | --- | --- |\n");
  const Signal clicks = Clicks(10 * kRate);
  for (uint32_t speed : kSpeeds) {
    std::optional<RunResult> r = Run(speed, clicks);
    if (!r.has_value()) {
      continue;
    }
    size_t expected = 0;
    for (uint64_t i = kClickSpacingFrames / 2; i < r->consumed; i += kClickSpacingFrames) {
      ++expected;
    }
    const size_t observed = CountClicks(r->output);
    std::printf("| %.2f | %zu | %zu | %+lld |\n", speed / 1e6, expected, observed,
                static_cast<long long>(observed) - static_cast<long long>(expected));
  }
}

}  // namespace

int main() {
  std::printf("# WSOLA offline measurements\n\n");
  std::printf("Parameters: %u Hz, %u channels, window %u frames, search +/-%u frames, hop %u frames, push %u frames.\n\n",
              kRate, kChannels, kWindowFrames, kSearchFrames, kWindowFrames / 2, kBlockFrames);
  const Signal harmonic = Harmonic(kSignalSeconds * kRate);
  ReportTiming(harmonic);
  ReportAccounting(harmonic);
  ReportPitch();
  ReportTransients();
  return 0;
}
