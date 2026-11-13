#include "elysium/sync/lag_detector.hpp"

#include <doctest.h>

#include <chrono>
#include <optional>

using elysium::sync::ApprovedLagDetectorConfig;
using elysium::sync::LagAction;
using elysium::sync::LagDetector;
using elysium::sync::LagDetectorConfig;
using elysium::sync::SustainedTrigger;
using elysium::sync::ThresholdEquality;
using elysium::sync::TriggerConfig;
using std::chrono::milliseconds;
using std::chrono::nanoseconds;

namespace {

// Observation cadence for tests. One libwebrtc pull is 10 ms.
constexpr milliseconds kStep{10};

// Test-only trigger. The values mirror the D-023 and D-024 gradual
// condition so the arithmetic in each test stays readable.
TriggerConfig GradualLikeConfig(ThresholdEquality equality) {
  return TriggerConfig{
      .threshold = milliseconds{100},
      .confirm = milliseconds{150},
      .rearm_margin = milliseconds{10},
      .rearm_hold = milliseconds{250},
      .equality = equality,
  };
}

SustainedTrigger MakeTrigger(ThresholdEquality equality = ThresholdEquality::kFiresAtThreshold) {
  std::optional<SustainedTrigger> trigger = SustainedTrigger::Create(GradualLikeConfig(equality));
  REQUIRE(trigger.has_value());
  return *trigger;
}

// Feeds a constant latency every kStep from start (inclusive) to end
// (exclusive). Returns the first time the trigger fired, if any.
std::optional<nanoseconds> Feed(SustainedTrigger& trigger, nanoseconds start, nanoseconds end,
                                milliseconds latency) {
  std::optional<nanoseconds> fired;
  for (nanoseconds now = start; now < end; now += kStep) {
    if (trigger.Observe(now, latency) && !fired) {
      fired = now;
    }
  }
  return fired;
}

}  // namespace

TEST_CASE("trigger fires once the condition holds for the confirmation time") {
  SustainedTrigger trigger = MakeTrigger();

  std::optional<nanoseconds> fired = Feed(trigger, milliseconds{0}, milliseconds{300}, milliseconds{100});

  REQUIRE(fired.has_value());
  CHECK(*fired == milliseconds{150});
  CHECK_FALSE(trigger.armed());
}

TEST_CASE("trigger restarts confirmation when the condition breaks") {
  SustainedTrigger trigger = MakeTrigger();

  CHECK_FALSE(Feed(trigger, milliseconds{0}, milliseconds{150}, milliseconds{120}).has_value());
  CHECK_FALSE(trigger.Observe(milliseconds{150}, milliseconds{99}));
  std::optional<nanoseconds> fired = Feed(trigger, milliseconds{160}, milliseconds{400}, milliseconds{120});

  REQUIRE(fired.has_value());
  CHECK(*fired == milliseconds{310});
}

TEST_CASE("trigger does not fire again before rearming") {
  SustainedTrigger trigger = MakeTrigger();
  REQUIRE(Feed(trigger, milliseconds{0}, milliseconds{160}, milliseconds{130}).has_value());

  CHECK_FALSE(Feed(trigger, milliseconds{160}, milliseconds{1000}, milliseconds{130}).has_value());
}

TEST_CASE("trigger rearms only after the hold time at least the margin below threshold") {
  SustainedTrigger trigger = MakeTrigger();
  REQUIRE(Feed(trigger, milliseconds{0}, milliseconds{160}, milliseconds{130}).has_value());

  // 95 ms sits only 5 ms below the threshold and never rearms.
  Feed(trigger, milliseconds{160}, milliseconds{700}, milliseconds{95});
  CHECK_FALSE(trigger.armed());

  // 90 ms sits exactly 10 ms below and rearms after 250 ms.
  Feed(trigger, milliseconds{700}, milliseconds{950}, milliseconds{90});
  CHECK_FALSE(trigger.armed());
  CHECK_FALSE(trigger.Observe(milliseconds{950}, milliseconds{90}));
  CHECK(trigger.armed());

  std::optional<nanoseconds> fired = Feed(trigger, milliseconds{960}, milliseconds{1300}, milliseconds{130});
  REQUIRE(fired.has_value());
  CHECK(*fired == milliseconds{1110});
}

TEST_CASE("trigger rearm hold restarts when latency climbs back") {
  SustainedTrigger trigger = MakeTrigger();
  REQUIRE(Feed(trigger, milliseconds{0}, milliseconds{160}, milliseconds{130}).has_value());

  Feed(trigger, milliseconds{160}, milliseconds{400}, milliseconds{80});
  trigger.Observe(milliseconds{400}, milliseconds{95});
  Feed(trigger, milliseconds{410}, milliseconds{650}, milliseconds{80});

  CHECK_FALSE(trigger.armed());
}

TEST_CASE("threshold equality is an explicit experimental setting") {
  SUBCASE("fires at the threshold") {
    SustainedTrigger trigger = MakeTrigger(ThresholdEquality::kFiresAtThreshold);
    CHECK(Feed(trigger, milliseconds{0}, milliseconds{300}, milliseconds{100}).has_value());
  }
  SUBCASE("requires latency above the threshold") {
    SustainedTrigger trigger = MakeTrigger(ThresholdEquality::kFiresAboveThreshold);
    CHECK_FALSE(Feed(trigger, milliseconds{0}, milliseconds{300}, milliseconds{100}).has_value());
    CHECK(Feed(trigger, milliseconds{300}, milliseconds{600}, milliseconds{101}).has_value());
  }
}

TEST_CASE("monotonic clock regression discards pending confirmation") {
  SustainedTrigger trigger = MakeTrigger();
  Feed(trigger, milliseconds{1000}, milliseconds{1140}, milliseconds{130});

  CHECK_FALSE(trigger.Observe(milliseconds{500}, milliseconds{130}));
  std::optional<nanoseconds> fired = Feed(trigger, milliseconds{510}, milliseconds{900}, milliseconds{130});

  REQUIRE(fired.has_value());
  CHECK(*fired == milliseconds{650});
}

TEST_CASE("trigger rejects invalid configuration") {
  TriggerConfig valid = GradualLikeConfig(ThresholdEquality::kFiresAtThreshold);

  SUBCASE("non-positive threshold") {
    valid.threshold = milliseconds{0};
    CHECK_FALSE(SustainedTrigger::Create(valid).has_value());
  }
  SUBCASE("negative confirmation") {
    valid.confirm = milliseconds{-1};
    CHECK_FALSE(SustainedTrigger::Create(valid).has_value());
  }
  SUBCASE("negative rearm margin") {
    valid.rearm_margin = milliseconds{-1};
    CHECK_FALSE(SustainedTrigger::Create(valid).has_value());
  }
  SUBCASE("rearm margin not below threshold") {
    valid.rearm_margin = valid.threshold;
    CHECK_FALSE(SustainedTrigger::Create(valid).has_value());
  }
  SUBCASE("negative rearm hold") {
    valid.rearm_hold = milliseconds{-1};
    CHECK_FALSE(SustainedTrigger::Create(valid).has_value());
  }
}

TEST_CASE("approved configuration matches D-023 and D-024") {
  LagDetectorConfig config = ApprovedLagDetectorConfig(ThresholdEquality::kFiresAtThreshold);

  CHECK(config.gradual.threshold == milliseconds{100});
  CHECK(config.gradual.confirm == milliseconds{150});
  CHECK(config.major.threshold == milliseconds{180});
  CHECK(config.major.confirm == milliseconds{50});
  CHECK(config.gradual.rearm_margin == milliseconds{10});
  CHECK(config.major.rearm_margin == milliseconds{10});
  CHECK(config.gradual.rearm_hold == milliseconds{250});
  CHECK(config.major.rearm_hold == milliseconds{250});
}

TEST_CASE("lag detector reports gradual correction for moderate lag") {
  std::optional<LagDetector> detector =
      LagDetector::Create(ApprovedLagDetectorConfig(ThresholdEquality::kFiresAtThreshold));
  REQUIRE(detector.has_value());

  LagAction first = LagAction::kNone;
  nanoseconds when{};
  for (nanoseconds now{}; now < milliseconds{400}; now += kStep) {
    LagAction action = detector->Observe(now, milliseconds{120});
    if (action != LagAction::kNone && first == LagAction::kNone) {
      first = action;
      when = now;
    }
  }

  CHECK(first == LagAction::kGradual);
  CHECK(when == milliseconds{150});
}

TEST_CASE("major recovery takes precedence and disarms gradual correction") {
  std::optional<LagDetector> detector =
      LagDetector::Create(ApprovedLagDetectorConfig(ThresholdEquality::kFiresAtThreshold));
  REQUIRE(detector.has_value());

  int major_count = 0;
  int gradual_count = 0;
  nanoseconds major_at{};
  for (nanoseconds now{}; now < milliseconds{600}; now += kStep) {
    LagAction action = detector->Observe(now, milliseconds{200});
    if (action == LagAction::kMajor) {
      ++major_count;
      major_at = now;
    }
    if (action == LagAction::kGradual) {
      ++gradual_count;
    }
  }

  CHECK(major_count == 1);
  CHECK(major_at == milliseconds{50});
  CHECK(gradual_count == 0);
}

TEST_CASE("lag detector rejects invalid configuration") {
  LagDetectorConfig config = ApprovedLagDetectorConfig(ThresholdEquality::kFiresAtThreshold);
  config.major.threshold = milliseconds{0};

  CHECK_FALSE(LagDetector::Create(config).has_value());
}
