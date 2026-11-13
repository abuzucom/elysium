// Sustained-delay detection for lag correction (D-023, D-024).
//
// A SustainedTrigger fires once when latency stays at or above a threshold
// for a confirmation time. It then stays disarmed until latency holds at
// least a margin below the threshold for a rearm hold time. LagDetector
// combines the gradual and major triggers. All times come from an
// injected monotonic clock. The classes never allocate and never throw,
// so the real-time audio thread may call them.
#ifndef ELYSIUM_SYNC_LAG_DETECTOR_HPP
#define ELYSIUM_SYNC_LAG_DETECTOR_HPP

#include <chrono>
#include <optional>

namespace elysium::sync {

// Threshold equality semantics remain a deferred decision (D-021,
// docs/backlog.md). Callers choose one explicitly as experimental
// configuration.
enum class ThresholdEquality {
  kFiresAtThreshold,     // latency >= threshold meets the condition
  kFiresAboveThreshold,  // latency > threshold meets the condition
};

struct TriggerConfig {
  std::chrono::nanoseconds threshold{};     // condition latency, > 0
  std::chrono::nanoseconds confirm{};       // sustain time before firing, >= 0
  std::chrono::nanoseconds rearm_margin{};  // distance below threshold, >= 0 and < threshold
  std::chrono::nanoseconds rearm_hold{};    // sustain time below margin, >= 0
  ThresholdEquality equality = ThresholdEquality::kFiresAtThreshold;
};

class SustainedTrigger {
 public:
  // Returns no trigger when config violates the documented bounds.
  static std::optional<SustainedTrigger> Create(const TriggerConfig& config) noexcept;

  // Records one latency observation at a monotonic instant. Returns true
  // exactly when the trigger fires. A monotonic regression discards any
  // pending confirmation or rearm hold and restarts from this
  // observation.
  bool Observe(std::chrono::nanoseconds monotonic, std::chrono::nanoseconds latency) noexcept;

  // Disarms the trigger without firing. A superseding action uses it.
  void Disarm() noexcept;

  [[nodiscard]] bool armed() const noexcept { return armed_; }

 private:
  explicit SustainedTrigger(const TriggerConfig& config) noexcept : config_(config) {}

  [[nodiscard]] bool MeetsCondition(std::chrono::nanoseconds latency) const noexcept;
  [[nodiscard]] bool ClearOfThreshold(std::chrono::nanoseconds latency) const noexcept;
  bool ObserveArmed(std::chrono::nanoseconds monotonic, std::chrono::nanoseconds latency) noexcept;
  void ObserveDisarmed(std::chrono::nanoseconds monotonic, std::chrono::nanoseconds latency) noexcept;

  TriggerConfig config_;
  bool armed_ = true;
  std::optional<std::chrono::nanoseconds> last_observation_;
  std::optional<std::chrono::nanoseconds> condition_since_;
  std::optional<std::chrono::nanoseconds> clear_since_;
};

enum class LagAction {
  kNone,
  kGradual,  // gradual correction eligible (D-023, D-025)
  kMajor,    // major recovery eligible (D-023, D-027)
};

struct LagDetectorConfig {
  TriggerConfig gradual;
  TriggerConfig major;
};

// Returns the approved D-023 and D-024 values: gradual at 100 ms total
// latency sustained 150 ms, major at 180 ms sustained 50 ms, and rearm
// after 250 ms at least 10 ms below the corresponding threshold.
LagDetectorConfig ApprovedLagDetectorConfig(ThresholdEquality equality) noexcept;

class LagDetector {
 public:
  static std::optional<LagDetector> Create(const LagDetectorConfig& config) noexcept;

  // Returns the action that becomes eligible at this observation. Major
  // recovery takes precedence and disarms gradual correction, because
  // major recovery supersedes it.
  LagAction Observe(std::chrono::nanoseconds monotonic, std::chrono::nanoseconds latency) noexcept;

 private:
  LagDetector(const SustainedTrigger& gradual, const SustainedTrigger& major) noexcept
      : gradual_(gradual), major_(major) {}

  SustainedTrigger gradual_;
  SustainedTrigger major_;
};

}  // namespace elysium::sync

#endif  // ELYSIUM_SYNC_LAG_DETECTOR_HPP
