// Streaming WSOLA time stretcher for lag correction (D-025, D-026).
//
// Wsola speeds playback up without changing pitch. It overlap-adds
// periodic-Hann windowed input segments at a fixed synthesis hop. Each
// segment comes from near its nominal input position at the configured
// speed, shifted within a search radius to best match the natural
// continuation of the previous segment. Every channel uses the same
// shift, so stereo phase relations survive.
//
// At normal speed the stretcher passes input through unchanged with no
// buffering. A change to an accelerated speed starts stretching from the
// current position as if a segment had just ended there, which crossfades
// over one hop. A change back to normal speed places one final segment at
// or after the nominal position and crossfades over one hop into exact
// passthrough. The hop length is half the window.
//
// Create allocates all buffers. Push, Pull, and SetSpeedPpm never
// allocate or throw, so the real-time audio thread may call them. The
// window and search sizes are experimental configuration
// (docs/backlog.md).
#ifndef ELYSIUM_DSP_WSOLA_HPP
#define ELYSIUM_DSP_WSOLA_HPP

#include <cstddef>
#include <cstdint>
#include <optional>
#include <span>
#include <vector>

namespace elysium::dsp {

// Speeds in parts per million of normal playback speed. D-025 allows
// normal speed or acceleration between 1% and 5%.
constexpr uint32_t kNormalSpeedPpm = 1'000'000;
constexpr uint32_t kMinAcceleratedSpeedPpm = 1'010'000;
constexpr uint32_t kMaxAcceleratedSpeedPpm = 1'050'000;

// Returns true for a speed inside the D-025 safety limits.
bool IsAllowedSpeedPpm(uint32_t speed_ppm) noexcept;

struct WsolaConfig {
  uint32_t sample_rate_hz = 0;
  uint16_t channels = 0;
  // Analysis and synthesis window in frames. Even. The synthesis hop is
  // half the window.
  uint32_t window_frames = 0;
  // Largest segment shift in frames, at most half the window.
  uint32_t search_frames = 0;
  // Largest frame count one Push call accepts.
  uint32_t max_push_frames = 0;
};

class Wsola {
 public:
  // Returns no stretcher when config violates the documented bounds.
  static std::optional<Wsola> Create(const WsolaConfig& config);

  // Sets the playback speed for segments that start after this call.
  // Returns false and keeps the current speed outside the D-025 limits.
  bool SetSpeedPpm(uint32_t speed_ppm) noexcept;

  // Appends interleaved input and produces every complete output
  // segment. Returns the frames accepted, which may fall short when the
  // input or output buffer is full.
  size_t Push(std::span<const float> interleaved) noexcept;

  // Moves up to out.size() / channels ready output frames into out.
  // Returns the frames written.
  size_t Pull(std::span<float> interleaved) noexcept;

  [[nodiscard]] uint32_t speed_ppm() const noexcept { return speed_ppm_; }
  [[nodiscard]] uint32_t hop_frames() const noexcept { return hop_; }
  // Input frames the output so far represents (nominal positions).
  [[nodiscard]] uint64_t consumed_input_frames() const noexcept;
  // Output frames produced so far, ready or already pulled.
  [[nodiscard]] uint64_t produced_output_frames() const noexcept { return produced_frames_; }
  // Input frames removed by acceleration (D-015 removed_frames).
  [[nodiscard]] uint64_t removed_frames() const noexcept;
  // Input frames the stretcher needs beyond the current position before
  // accelerated output can continue. The caller supplies them from its
  // backlog when a correction starts.
  [[nodiscard]] uint32_t lookahead_frames() const noexcept;

 private:
  explicit Wsola(const WsolaConfig& config);

  bool Step() noexcept;
  bool PassThrough() noexcept;
  void EnterStretching() noexcept;
  bool ExitStretching() noexcept;
  bool ProduceSegment() noexcept;
  void EmitHop() noexcept;
  [[nodiscard]] int64_t ChooseSegmentStart(int64_t nominal, int64_t min_delta) const noexcept;
  [[nodiscard]] int64_t InputEnd() const noexcept;
  [[nodiscard]] double Similarity(int64_t candidate, int64_t target) const noexcept;
  [[nodiscard]] float MonoAt(int64_t frame) const noexcept;
  void DiscardConsumedInput() noexcept;

  WsolaConfig config_;
  uint32_t hop_ = 0;
  uint32_t speed_ppm_ = kNormalSpeedPpm;
  std::vector<float> window_;

  // Input frames [input_start_, input_start_ + input_frames_) in input_.
  std::vector<float> input_;
  int64_t input_start_ = 0;
  size_t input_frames_ = 0;

  // Overlap-add accumulator of one window.
  std::vector<float> overlap_;
  // Completed output frames waiting for Pull.
  std::vector<float> ready_;
  size_t ready_frames_ = 0;

  // Nominal input position of the next segment, in millionths of a frame.
  int64_t nominal_micro_frames_ = 0;
  // Mode flags. Bypass passes input through. Entering marks a pending
  // start of stretching. Exiting marks a pending return to bypass.
  bool bypass_ = true;
  bool entering_ = false;
  bool exiting_ = false;
  bool has_previous_ = false;
  int64_t previous_start_ = 0;
  uint64_t produced_frames_ = 0;
};

}  // namespace elysium::dsp

#endif  // ELYSIUM_DSP_WSOLA_HPP
