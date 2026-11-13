#include "elysium/dsp/wsola.hpp"

#include <algorithm>
#include <cmath>
#include <limits>
#include <numbers>

namespace elysium::dsp {

namespace {

constexpr int64_t kMicroPerFrame = 1'000'000;
// Smallest window that leaves a meaningful overlap for similarity search.
constexpr uint32_t kMinWindowFrames = 64;
// Upper bound on channels keeps buffer sizes and mono mixing bounded.
constexpr uint16_t kMaxChannels = 8;
// Guards the normalized cross-correlation against silent segments.
constexpr double kEnergyFloor = 1e-12;

bool IsValid(const WsolaConfig& config) noexcept {
  return config.sample_rate_hz > 0 && config.channels > 0 && config.channels <= kMaxChannels &&
         config.window_frames >= kMinWindowFrames && config.window_frames % 2 == 0 &&
         config.search_frames <= config.window_frames / 2 && config.max_push_frames > 0;
}

}  // namespace

bool IsAllowedSpeedPpm(uint32_t speed_ppm) noexcept {
  return speed_ppm == kNormalSpeedPpm ||
         (speed_ppm >= kMinAcceleratedSpeedPpm && speed_ppm <= kMaxAcceleratedSpeedPpm);
}

std::optional<Wsola> Wsola::Create(const WsolaConfig& config) {
  if (!IsValid(config)) {
    return std::nullopt;
  }
  return Wsola(config);
}

Wsola::Wsola(const WsolaConfig& config) : config_(config), hop_(config.window_frames / 2) {
  const size_t channels = config.channels;
  const size_t window = config.window_frames;
  // Periodic Hann: windows at half-window spacing sum to exactly one.
  window_.resize(window);
  for (size_t i = 0; i < window; ++i) {
    const double phase = 2.0 * std::numbers::pi * static_cast<double>(i) / static_cast<double>(window);
    window_[i] = static_cast<float>(0.5 - 0.5 * std::cos(phase));
  }
  // Retained input spans the previous segment continuation, the search
  // range, and one window, plus one push.
  const size_t input_capacity = config.max_push_frames + 2 * window + 2 * size_t{config.search_frames};
  input_.assign(input_capacity * channels, 0.0f);
  overlap_.assign(window * channels, 0.0f);
  ready_.assign((config.max_push_frames + 2 * window) * channels, 0.0f);
}

bool Wsola::SetSpeedPpm(uint32_t speed_ppm) noexcept {
  if (!IsAllowedSpeedPpm(speed_ppm)) {
    return false;
  }
  speed_ppm_ = speed_ppm;
  return true;
}

uint64_t Wsola::consumed_input_frames() const noexcept {
  return static_cast<uint64_t>(nominal_micro_frames_ / kMicroPerFrame);
}

uint64_t Wsola::removed_frames() const noexcept { return consumed_input_frames() - produced_frames_; }

size_t Wsola::Push(std::span<const float> interleaved) noexcept {
  const size_t channels = config_.channels;
  DiscardConsumedInput();
  const size_t capacity = input_.size() / channels;
  const size_t offered = interleaved.size() / channels;
  const size_t accepted = std::min({offered, size_t{config_.max_push_frames}, capacity - input_frames_});
  std::copy_n(interleaved.begin(), accepted * channels, input_.begin() + static_cast<std::ptrdiff_t>(input_frames_ * channels));
  input_frames_ += accepted;
  while (ProduceSegment()) {
  }
  return accepted;
}

size_t Wsola::Pull(std::span<float> interleaved) noexcept {
  const size_t channels = config_.channels;
  const size_t count = std::min(interleaved.size() / channels, ready_frames_);
  const auto taken = static_cast<std::ptrdiff_t>(count * channels);
  const auto remaining = static_cast<std::ptrdiff_t>(ready_frames_ * channels);
  std::copy(ready_.begin(), ready_.begin() + taken, interleaved.begin());
  std::copy(ready_.begin() + taken, ready_.begin() + remaining, ready_.begin());
  ready_frames_ -= count;
  return count;
}

bool Wsola::ProduceSegment() noexcept {
  const size_t channels = config_.channels;
  const size_t window = config_.window_frames;
  const int64_t nominal = nominal_micro_frames_ / kMicroPerFrame;
  const int64_t input_end = input_start_ + static_cast<int64_t>(input_frames_);
  if (nominal + int64_t{config_.search_frames} + static_cast<int64_t>(window) > input_end) {
    return false;
  }
  if ((ready_frames_ + hop_) * channels > ready_.size()) {
    return false;
  }

  const int64_t start = ChooseSegmentStart(nominal);
  const auto offset = static_cast<size_t>(start - input_start_) * channels;
  for (size_t i = 0; i < window; ++i) {
    for (size_t c = 0; c < channels; ++c) {
      overlap_[i * channels + c] += window_[i] * input_[offset + i * channels + c];
    }
  }

  // The first hop of the accumulator is complete once this segment lands.
  const size_t hop_samples = size_t{hop_} * channels;
  const auto ready_end = static_cast<std::ptrdiff_t>(ready_frames_ * channels);
  std::copy_n(overlap_.begin(), hop_samples, ready_.begin() + ready_end);
  std::copy(overlap_.begin() + static_cast<std::ptrdiff_t>(hop_samples), overlap_.end(), overlap_.begin());
  std::fill(overlap_.end() - static_cast<std::ptrdiff_t>(hop_samples), overlap_.end(), 0.0f);
  ready_frames_ += hop_;
  produced_frames_ += hop_;

  previous_start_ = start;
  has_previous_ = true;
  nominal_micro_frames_ += int64_t{hop_} * speed_ppm_;
  return true;
}

int64_t Wsola::ChooseSegmentStart(int64_t nominal) const noexcept {
  // Normal speed keeps the nominal position so overlap-add reconstructs
  // the input exactly.
  if (!has_previous_ || speed_ppm_ == kNormalSpeedPpm) {
    return nominal;
  }
  const int64_t target = previous_start_ + hop_;
  const int64_t search = config_.search_frames;
  int64_t best = nominal;
  double best_score = -std::numeric_limits<double>::infinity();
  for (int64_t delta = -search; delta <= search; ++delta) {
    const int64_t candidate = nominal + delta;
    if (candidate < input_start_) {
      continue;
    }
    const double score = Similarity(candidate, target);
    if (score > best_score) {
      best_score = score;
      best = candidate;
    }
  }
  return best;
}

double Wsola::Similarity(int64_t candidate, int64_t target) const noexcept {
  // Normalized cross-correlation of the mono mix over one hop, the
  // overlap between consecutive segments.
  double cross = 0.0;
  double candidate_energy = 0.0;
  double target_energy = 0.0;
  for (int64_t i = 0; i < hop_; ++i) {
    const double a = MonoAt(candidate + i);
    const double b = MonoAt(target + i);
    cross += a * b;
    candidate_energy += a * a;
    target_energy += b * b;
  }
  return cross / std::sqrt(candidate_energy * target_energy + kEnergyFloor);
}

float Wsola::MonoAt(int64_t frame) const noexcept {
  const size_t channels = config_.channels;
  const auto offset = static_cast<size_t>(frame - input_start_) * channels;
  float sum = 0.0f;
  for (size_t c = 0; c < channels; ++c) {
    sum += input_[offset + c];
  }
  return sum / static_cast<float>(channels);
}

void Wsola::DiscardConsumedInput() noexcept {
  const size_t channels = config_.channels;
  const int64_t nominal = nominal_micro_frames_ / kMicroPerFrame;
  int64_t keep_from = nominal - int64_t{config_.search_frames};
  if (has_previous_) {
    keep_from = std::min(keep_from, previous_start_ + int64_t{hop_});
  }
  if (keep_from <= input_start_) {
    return;
  }
  const auto drop = static_cast<size_t>(keep_from - input_start_);
  const size_t discard = std::min(drop, input_frames_);
  std::copy(input_.begin() + static_cast<std::ptrdiff_t>(discard * channels),
            input_.begin() + static_cast<std::ptrdiff_t>(input_frames_ * channels), input_.begin());
  input_frames_ -= discard;
  input_start_ += static_cast<int64_t>(discard);
}

}  // namespace elysium::dsp
