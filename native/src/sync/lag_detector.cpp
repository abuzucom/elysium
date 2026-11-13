#include "elysium/sync/lag_detector.hpp"

namespace elysium::sync {

namespace {

using std::chrono::milliseconds;
using std::chrono::nanoseconds;

// Approved architecture values (D-023, D-024).
constexpr milliseconds kGradualThreshold{100};
constexpr milliseconds kGradualConfirm{150};
constexpr milliseconds kMajorThreshold{180};
constexpr milliseconds kMajorConfirm{50};
constexpr milliseconds kRearmMargin{10};
constexpr milliseconds kRearmHold{250};

bool IsValid(const TriggerConfig& config) noexcept {
  return config.threshold > nanoseconds::zero() && config.confirm >= nanoseconds::zero() &&
         config.rearm_margin >= nanoseconds::zero() && config.rearm_margin < config.threshold &&
         config.rearm_hold >= nanoseconds::zero();
}

// Returns true once since has held for at least duration at now.
bool HeldFor(const std::optional<nanoseconds>& since, nanoseconds now, nanoseconds duration) noexcept {
  return since.has_value() && now - *since >= duration;
}

}  // namespace

std::optional<SustainedTrigger> SustainedTrigger::Create(const TriggerConfig& config) noexcept {
  if (!IsValid(config)) {
    return std::nullopt;
  }
  return SustainedTrigger(config);
}

bool SustainedTrigger::MeetsCondition(nanoseconds latency) const noexcept {
  if (config_.equality == ThresholdEquality::kFiresAtThreshold) {
    return latency >= config_.threshold;
  }
  return latency > config_.threshold;
}

bool SustainedTrigger::ClearOfThreshold(nanoseconds latency) const noexcept {
  // "At least the margin below" includes the margin boundary itself.
  return latency <= config_.threshold - config_.rearm_margin;
}

bool SustainedTrigger::Observe(nanoseconds monotonic, nanoseconds latency) noexcept {
  if (last_observation_.has_value() && monotonic < *last_observation_) {
    // A regressed clock invalidates every elapsed-time measurement.
    condition_since_.reset();
    clear_since_.reset();
  }
  last_observation_ = monotonic;
  if (armed_) {
    return ObserveArmed(monotonic, latency);
  }
  ObserveDisarmed(monotonic, latency);
  return false;
}

bool SustainedTrigger::ObserveArmed(nanoseconds monotonic, nanoseconds latency) noexcept {
  if (!MeetsCondition(latency)) {
    condition_since_.reset();
    return false;
  }
  if (!condition_since_.has_value()) {
    condition_since_ = monotonic;
  }
  if (!HeldFor(condition_since_, monotonic, config_.confirm)) {
    return false;
  }
  Disarm();
  return true;
}

void SustainedTrigger::ObserveDisarmed(nanoseconds monotonic, nanoseconds latency) noexcept {
  if (!ClearOfThreshold(latency)) {
    clear_since_.reset();
    return;
  }
  if (!clear_since_.has_value()) {
    clear_since_ = monotonic;
  }
  if (HeldFor(clear_since_, monotonic, config_.rearm_hold)) {
    armed_ = true;
    clear_since_.reset();
  }
}

void SustainedTrigger::Disarm() noexcept {
  armed_ = false;
  condition_since_.reset();
  clear_since_.reset();
}

LagDetectorConfig ApprovedLagDetectorConfig(ThresholdEquality equality) noexcept {
  return LagDetectorConfig{
      .gradual = TriggerConfig{.threshold = kGradualThreshold,
                               .confirm = kGradualConfirm,
                               .rearm_margin = kRearmMargin,
                               .rearm_hold = kRearmHold,
                               .equality = equality},
      .major = TriggerConfig{.threshold = kMajorThreshold,
                             .confirm = kMajorConfirm,
                             .rearm_margin = kRearmMargin,
                             .rearm_hold = kRearmHold,
                             .equality = equality},
  };
}

std::optional<LagDetector> LagDetector::Create(const LagDetectorConfig& config) noexcept {
  std::optional<SustainedTrigger> gradual = SustainedTrigger::Create(config.gradual);
  std::optional<SustainedTrigger> major = SustainedTrigger::Create(config.major);
  if (!gradual.has_value() || !major.has_value()) {
    return std::nullopt;
  }
  return LagDetector(*gradual, *major);
}

LagAction LagDetector::Observe(nanoseconds monotonic, nanoseconds latency) noexcept {
  // Both triggers observe every sample so their timers stay current.
  const bool major_fired = major_.Observe(monotonic, latency);
  const bool gradual_fired = gradual_.Observe(monotonic, latency);
  if (major_fired) {
    gradual_.Disarm();
    return LagAction::kMajor;
  }
  return gradual_fired ? LagAction::kGradual : LagAction::kNone;
}

}  // namespace elysium::sync
