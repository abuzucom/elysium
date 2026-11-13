# WSOLA offline measurements, 2026-11-13

Status: Gate 3 prototype evidence. Not a phone measurement.

## Setup

- Code: `native/src/dsp/wsola.cpp`, tool `native/tools/wsola_measure.cpp`.
- Host: cloud container, Intel Xeon 2.30 GHz, 4 vCPU, x86_64, shared.
- Build: clang 18.1.3, CMake Release (`-O3`), C++20.
- Signals: synthetic test-only substitutes for program audio. A harmonic
  stereo tone with vibrato for timing and accounting, pure sines for
  pitch, and a click train for transients.
- Parameters: experimental values, not approved settings.
- Timing covers one `Push` and one `Pull` per 10 ms block. Harness
  allocations stay outside the timed region.

## Key findings

- Pitch holds within 0.004% at every allowed speed. Plain resampling at
  1.05 would shift 440 Hz to 462 Hz.
- Sample accounting is exact. Removed over consumed frames matches
  1 - 1/speed to five decimals at every speed. The counters feed the
  D-015 `removed_frames` field.
- Processing at 1.01 to 1.05 used about 0.44 to 0.49 ms per 10 ms block
  on average across three runs. p99 stayed between 0.50 and 0.83 ms. The
  maximum reached 2.9 ms once. This host is not a phone. Phone
  measurements remain Gate 3 work on devices.
- Normal speed runs only the overlap-add path: about 2 us per block.
- Buffering adds latency. The first output appears after 1440 input
  frames (30 ms). Steady-state buffered input holds 773 to 1152 frames
  (16 to 24 ms) and includes the window and search look-ahead. That
  latency sits inside the 80 ms capture-to-output target (D-022). A
  bypass at normal speed with a crossfade into stretching would remove it
  outside corrections. That design question goes to the backlog.
- Transients: at 1.03 the output lost 2 of 100 clicks. At other speeds
  every click survived. WSOLA can drop or repeat short transients when it
  removes material. Transient-aware segment selection stays open.

## Measured run

### Processing time per 10 ms block (harmonic stereo signal, 60 s)

| Speed | Mean us | p99 us | Max us | Budget share at p99 |
| --- | --- | --- | --- | --- |
| 1.00 | 1.8 | 2.0 | 37.2 | 0.02% |
| 1.01 | 486.2 | 810.8 | 1158.8 | 8.11% |
| 1.03 | 478.9 | 824.9 | 2855.4 | 8.25% |
| 1.05 | 456.7 | 715.2 | 1233.6 | 7.15% |

### Sample accounting and buffering

| Speed | Pushed | Consumed | Produced | Removed | Removed / consumed | Expected | Buffered input | First output after |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| 1.00 | 2880000 | 2879040 | 2879040 | 0 | 0.00000 | 0.00000 | 960 frames | 1440 frames |
| 1.01 | 2880000 | 2879227 | 2850720 | 28507 | 0.00990 | 0.00990 | 773 frames | 1440 frames |
| 1.03 | 2880000 | 2878891 | 2795040 | 83851 | 0.02913 | 0.02913 | 1109 frames | 1440 frames |
| 1.05 | 2880000 | 2878848 | 2741760 | 137088 | 0.04762 | 0.04762 | 1152 frames | 1440 frames |

### Pitch preservation (sine, 10 s)

| Speed | 440 Hz in -> out | Error | 1000 Hz in -> out | Error | Resampling would give (440 Hz) |
| --- | --- | --- | --- | --- | --- |
| 1.00 | 440.00 Hz | 0.000% | 1000.00 Hz | 0.000% | 440.00 Hz |
| 1.01 | 440.00 Hz | 0.001% | 1000.00 Hz | 0.000% | 444.40 Hz |
| 1.03 | 439.99 Hz | 0.003% | 1000.00 Hz | 0.000% | 453.20 Hz |
| 1.05 | 439.98 Hz | 0.004% | 1000.00 Hz | 0.000% | 462.00 Hz |

### Transients (click every 100 ms, 10 s)

| Speed | Clicks in consumed input | Clicks in output | Difference |
| --- | --- | --- | --- |
| 1.00 | 100 | 100 | +0 |
| 1.01 | 100 | 100 | +0 |
| 1.03 | 100 | 98 | -2 |
| 1.05 | 100 | 100 | +0 |
