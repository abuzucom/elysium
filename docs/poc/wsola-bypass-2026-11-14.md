# WSOLA normal-speed bypass, 2026-11-14

Status: Gate 3 prototype evidence. Same host and method as
`docs/poc/wsola-offline-2026-11-13.md`. Not a phone measurement.

## Change

- Normal speed passes input through unchanged with no buffering.
- A change to an accelerated speed starts stretching from the current
  position. A virtual segment ending there crossfades into the first real
  segment over one hop (480 frames, 10 ms).
- A change back to normal speed places one final segment at or after the
  nominal position, chosen by the same similarity search. It crossfades
  over one hop into exact passthrough. Removed frames never decrease.
- Stretching needs `lookahead_frames()` (window plus search, 1200
  frames) beyond the current position. The renderer supplies them from
  its lag backlog when a correction starts.

## Findings

- At normal speed the stretcher now buffers 0 frames and releases each
  10 ms block as soon as it arrives. The 16 to 24 ms of buffering
  measured on 2026-11-13 now applies only while a correction runs.
- Normal-speed processing fell to about 0.5 us per block.
- Accelerated results match the previous run: pitch error at most
  0.004%, exact removed-frame accounting, and the same 2 of 100 lost
  clicks at 1.03.
- This run recorded worst-case blocks of 4.0 and 5.0 ms while stretching
  at 1.01 and 1.03. p99 stayed below 0.93 ms. The host is a shared cloud
  container. Worst-case timing needs phone measurements.
- Tests check that mode changes add no step larger than 1.2 times the
  largest step of the input sine, and that passthrough after a
  correction matches the input exactly at the removed-frame offset.

## Measured run

### Processing time per 10 ms block (harmonic stereo signal, 60 s)

| Speed | Mean us | p99 us | Max us | Budget share at p99 |
| --- | --- | --- | --- | --- |
| 1.00 | 0.5 | 0.8 | 42.0 | 0.01% |
| 1.01 | 482.9 | 712.8 | 3962.5 | 7.13% |
| 1.03 | 468.9 | 744.5 | 5018.8 | 7.45% |
| 1.05 | 480.3 | 925.6 | 1382.0 | 9.26% |

### Sample accounting and buffering

| Speed | Pushed | Consumed | Produced | Removed | Removed / consumed | Expected | Buffered input | First output after |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| 1.00 | 2880000 | 2880000 | 2880000 | 0 | 0.00000 | 0.00000 | 0 frames | 480 frames |
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
