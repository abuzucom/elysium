# ESP32 broadcaster desk study, 2026-11-14

Status: Gate 4 desk study. No hardware was tested. The board choice stays
deferred to the human (decision register).

## Question

Can an ESP32 broadcaster capture stereo audio, encode 48 kHz stereo Opus
in 10 ms frames at 256 kbps with DTX disabled, protect it with
DTLS-SRTP using pinned mutual certificates, and send it with timing
provenance to the venue gateway (D-003, D-005, D-006, D-008)?

Labels used below: **[source]** marks a fact from a cited page.
**[derived]** marks arithmetic from those facts. **[unverified]** marks
an assumption that needs a hardware test. **[confirmed]** marks a fact
the human confirmed on 2026-11-14.

## Short answer

- ESP32-P4 is the stronger candidate. It has a built-in Ethernet MAC,
  more CPU, and more memory. No Opus encode figures for it were found.
- ESP32-S3 can probably encode the stream, but with thin headroom. It
  lacks an Ethernet MAC, so wired ingest needs an SPI Ethernet chip.
- The DTLS-SRTP and Opus building blocks exist in Espressif's WebRTC
  component. Pinned mutual certificates and abs-capture-time remain
  integration work.
- A bench test on both boards is the next step. Buying boards costs
  money and needs approval.

## Chip comparison

| Item | ESP32-P4 | ESP32-S3 |
| --- | --- | --- |
| CPU | Dual-core RISC-V, up to 400 MHz on v3.x silicon (360 MHz before v3) **[source]** | Dual-core Xtensa LX7, up to 240 MHz **[source]** |
| On-chip RAM | 768 KB L2 memory, plus 32 KB LP SRAM **[source]** | 512 KB SRAM **[source]** |
| PSRAM | 16 MB or 32 MB in package **[source]** | Optional, up to 8 MB on chip, up to 16 MB on some modules **[source]** |
| Ethernet | 10/100 Mbit MAC with RMII, external PHY needed **[source]** | No MAC. SPI Ethernet such as W5500 needed **[source]** |
| Wi-Fi | None on chip **[confirmed]**. Boards such as the Waveshare ESP32-P4-WIFI6 add a separate radio | 2.4 GHz Wi-Fi and Bluetooth LE **[source]** |
| Audio input | I2S with PDM **[source]** | I2S **[source]** |

The P4 datasheets found are preliminary (v0.4 and v0.7). Final figures
need the production datasheet.

## Opus encoding

- No published Opus encode figures were found for ESP32-P4 or for
  Espressif's `esp_audio_codec` Opus encoder.
- The `esphome/micro-opus` benchmark on ESP32-S3 at 240 MHz reports
  48 kHz stereo music encoding at about 3.2 times real time at 64 kbps,
  complexity 0, and about 1.6 times real time at 192 kbps, complexity 8
  or higher **[source]**.
- At 1.6 times real time, encoding alone would use about 62% of one
  core **[derived]**. The project needs 256 kbps in 10 ms frames. Higher
  bitrate and shorter frames both cost more per second of audio
  **[unverified]**. Lower complexity settings trade quality for CPU.
- The P4 runs up to 400 MHz on a different core design. It likely gives
  more headroom, but no measurement exists **[unverified]**.
- The same benchmark notes that SILK encoding in floating point misses
  real time. The project uses CELT music mode, so this limit should not
  apply **[unverified]**.

## Network load

- One 10 ms frame at 256 kbps carries 320 bytes of Opus payload
  **[derived]**.
- With RTP (12 bytes), an SRTP tag (10 or 16 bytes), UDP (8 bytes),
  IPv4 (20 bytes), and Ethernet framing (about 38 bytes), each packet
  takes about 410 bytes on the wire. At 100 packets per second the
  stream needs about 0.33 Mbit/s **[derived]**.
- A community iperf test measured about 9.2 Mbit/s TCP on ESP32-S3 with
  a W5500 and the stock lwIP stack **[source]**. That leaves wide margin
  for the stream **[derived]**.
- The P4 Ethernet MAC runs at 10/100 Mbit/s **[source]**. Its practical
  throughput on ESP-IDF was not found **[unverified]**.

## Security transport

- Espressif's `esp_peer` component (esp-webrtc-solution) uses mbedTLS
  for DTLS. Its README says to enable DTLS and DTLS-SRTP support in
  mbedTLS through ESP-IDF configuration. Older versions list libSRTP as
  a dependency. It supports Opus audio **[source]**.
- The README reports two concurrent peers on an ESP32-S3 without
  external memory and under 60 KB of RAM for a minimal setup **[source]**.
- The gateway (`gateway/internal/ingest`) needs a plain DTLS-SRTP client
  without ICE, mutual certificates, extended master secret, and the
  AES128_CM_HMAC_SHA1_80 or AEAD_AES_128_GCM profile. Whether `esp_peer`
  can run without ICE, or whether a direct mbedTLS and libSRTP client is
  simpler, needs a code review **[unverified]**.
- Certificate pinning needs a per-device key pair stored with flash
  encryption. The enrollment procedure stays deferred (backlog).

## Timing provenance

- I2S DMA delivers a fixed number of frames per buffer. Counting frames
  gives an exact 64-bit source sample position **[derived]**.
- Abs-capture-time needs a capture clock tied to venue time. Options are
  SNTP from the venue time service, or IEEE 1588 PTP if the P4 Ethernet
  MAC supports hardware timestamps **[unverified]**.
- The ADC sample clock and the ESP32 system clock drift apart. The
  gateway mapper (D-010) already bounds source drift through its
  configuration.

## Audio input

- The broadcaster needs an external stereo ADC on I2S. Codec selection
  (line-level input range, noise floor, sample rate support at 48 kHz)
  stays open **[unverified]**.

## Proposed bench test

1. Obtain one ESP32-P4 board with Ethernet and one ESP32-S3 board with a
   W5500, plus a stereo I2S ADC board. This costs money and needs
   approval.
2. Measure Opus encode time for 48 kHz stereo, 10 ms frames, 256 kbps,
   DTX off, at complexity levels 0, 5, and 10.
3. Run a DTLS-SRTP client with pinned mutual certificates against the
   existing gateway. Confirm the handshake and byte-identical payloads.
4. Send abs-capture-time from an SNTP-disciplined clock and verify it at
   the gateway.
5. Measure CPU, memory, and packet timing jitter over a one-hour run.

## Sources

- [ESP32-P4 features (Zephyr)](https://docs.zephyrproject.org/latest/boards/espressif/common/soc-esp32p4-features.html)
- [Waveshare ESP32-P4-ETH (Zephyr)](https://docs.zephyrproject.org/latest/boards/waveshare/esp32p4_eth/doc/index.html)
- [ESP32-P4 chip datasheet (preliminary, mirror)](https://cdn.compacttool.ru/downloads/ESP32_P4_Chip_Datasheet_EN.pdf)
- [Espressif ESP32-P4 product page](https://www.espressif.com/ja-jp/products/socs/esp32-p4)
- [ESP32-S3 datasheet (Mouser copy)](https://mouser.com/datasheet/2/891/Espressif_ESP32_S3_Datasheet-2904763.pdf)
- [ESP32-S3 launch article (CNX Software)](https://www.cnx-software.com/2021/01/02/esp32-s3-dual-core-wifi-and-bluetooth-le-5-soc-supports-ai-acceleration-for-aiot-applications/?amp=1)
- [esp_peer component README](https://components.espressif.com/components/espressif/esp_peer/versions/1.5.5/readme?language=en)
- [esp_peer v1.3.3 README](https://components.espressif.com/components/espressif/esp_peer/versions/1.3.3/readme?language=en)
- [micro-opus component](https://components.espressif.com/components/esphome/micro-opus)
- [micro-opus encode benchmark](https://components.espressif.com/components/esphome/micro-opus/versions/0.4.1/examples/encode_benchmark)
- [esp_audio_codec component](https://components.espressif.com/components/espressif/esp_audio_codec/versions/2.0.3)
- [ESP32-S3 W5500 TOE vs lwIP iperf test (Hackster)](https://hackster.io/tjr0927/esp32-s3-wiznet-w5500-toe-vs-lwip-iperf-performance-test-274510)
- [ESP-IDF Ethernet API (ESP32-S3)](https://docs.espressif.com/projects/esp-idf/en/v5.5/esp32s3/api-reference/network/esp_eth.html)
