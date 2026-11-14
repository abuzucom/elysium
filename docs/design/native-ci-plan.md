# Hosted native build CI plan, 2026-11-14

Status: Draft plan for approval. No workflow exists yet. Each workflow
below needs its own approval because it uses paid runner minutes.

## Goal

The human chose hosted macOS CI for Gate 3 native work. This plan
checks what GitHub-hosted runners can build, compares a from-source
libwebrtc build with prebuilt WebRTC binaries, and proposes a phased
path.

Labels: **[source]** marks a fact from a cited page. **[derived]** marks
arithmetic or reasoning from those facts. **[unverified]** marks an
assumption that needs checking.

## Runner limits

| Runner | CPU | RAM | Storage | Source |
| --- | --- | --- | --- | --- |
| `ubuntu-24.04` (x64) | 4 vCPU | 16 GB | 14 GB | [source] |
| `macos-15` (arm64) | 3 cores (M1) | 7 GB | 14 GB | [source] |
| `macos-15-intel` | 4 vCPU | 14 GB | 14 GB | [source] |

- One job may run at most 6 hours on a GitHub-hosted runner **[source]**.
- Self-hosted runners allow 5 days per job **[source]**.

## Cost

- Included minutes count macOS at 10 times and Linux at 1 times
  **[source]**.
- Reported per-minute rates after January 2026: Linux about $0.006,
  macOS about $0.062 **[source, third-party summary]**. GitHub's pricing
  page needs a check before any budget decision.
- Billing rounds each job up to whole minutes **[source]**.
- Whether this repository is public or private decides whether standard
  runner minutes cost anything **[unverified]**.

## From-source libwebrtc on hosted runners

- The official WebRTC docs estimate about 16 GB for a Linux checkout with
  Android and about 5.6 GB for Mac with iOS **[source]**.
- Community build guides recommend 30 GB to 150 GB including build
  output **[source]**.
- Android builds only run on Linux hosts **[source]**.
- Standard runners offer 14 GB of storage. An Android checkout alone
  exceeds it. An iOS checkout plus build output, Xcode, and caches very
  likely exceeds it too **[derived]**.
- Build time on 3 to 4 cores was not found. It may approach or exceed
  the 6-hour job limit **[unverified]**.

Conclusion: the patched libwebrtc build (D-016) cannot run on standard
hosted runners **[derived]**. It needs larger paid runners with more
storage or a self-hosted build machine.

## Prebuilt WebRTC

- `webrtc-sdk/android` publishes prebuilt WebRTC to Maven Central as
  `io.github.webrtc-sdk:android`, recently version 137.7151.05
  **[source]**. A shadowed variant moves the `org.webrtc` package to
  avoid class collisions **[source]**.
- The same organization publishes an iOS and macOS CocoaPods spec
  **[source]**. A Swift Package Manager distribution was not found
  **[unverified]**.
- Google removed its official prebuilt Android binaries after JCenter
  shut down **[source]**.
- Prebuilt binaries are unpatched. They lack the D-016 metadata and PCM
  instrumentation. They still suffice to build and test everything
  around the patch: the Flutter shell, the native bridges, the C++ core
  on device architectures, and an unpatched sink path **[derived]**.
- Whether LiveKit's Flutter SDK builds on these binaries needs a check
  of its dependency files **[unverified]**.

## Proposed phases

### Phase A: native core and app shell on standard runners

- Android job on `ubuntu-24.04`: build the C++ core (`native/`) with the
  Android NDK for arm64-v8a and x86_64, and run its tests under an
  x86_64 emulator or as host tests.
- iOS job on `macos-15`: build the C++ core for iOS device and
  simulator, and run its tests on the simulator.
- Later in Phase A: a Flutter shell with native plugin stubs linked
  against prebuilt, unpatched WebRTC.
- Needs approval: Flutter SDK version, Android NDK version, Xcode
  version, and the prebuilt WebRTC versions as dependencies.

### Phase B: patched libwebrtc artifacts

- Build the D-016 patch on a larger hosted runner or a self-hosted
  machine with at least 100 GB of storage **[derived from community
  guidance]**.
- Publish versioned build artifacts that Phase A jobs consume.
- Needs approval: the runner type and its cost, or self-hosted
  infrastructure, plus the libwebrtc version to patch.

### Phase C: device tests

- Hosted runners offer simulators and emulators only. Latency,
  headphone, Bluetooth, and screen-locked playback tests need physical
  phones **[derived]**.
- Options: phones operated by the human with scripted procedures, or a
  paid device farm. This stays a later decision.

## Rough minute estimate for Phase A

- Assumption: each pull request touching `native/` runs one 10-minute
  Android job and one 15-minute macOS job **[unverified]**.
- Included-minute use per pull request: 10 + 15 x 10 = 160 minutes
  **[derived]**.
- Billed cost per pull request at the reported rates: about $0.06 +
  $0.93 = about $1.00 **[derived]**, only where minutes are billed.

## Decisions requested

1. Approve Phase A with prebuilt, unpatched WebRTC, or wait for patched
   builds.
2. Choose the Phase B build host: larger hosted runners or a self-hosted
   machine.
3. Set a monthly Actions budget or minute cap.
4. Approve tool versions (Flutter, NDK, Xcode) when Phase A starts.

## Sources

- [GitHub-hosted runners reference](https://docs.github.com/en/actions/reference/runners/github-hosted-runners)
- [Choose the runner for a job](https://docs.github.com/en/enterprise-cloud@latest/actions/how-tos/write-workflows/choose-where-workflows-run/choose-the-runner-for-a-job)
- [GitHub Actions limits](https://docs.github.com/en/actions/reference/limits)
- [How use of GitHub Actions is measured](https://docs.github.com/en/billing/concepts/product-billing/github-actions)
- [GitHub Actions pricing changes 2026 (third party)](https://cicdcost.com/github-actions-pricing-changes-2026)
- [WebRTC development docs (Firefox mirror)](https://searchfox.org/firefox-main/source/third_party/libwebrtc/docs/native-code/development/README.md)
- [WebRTC Android docs (Firefox mirror)](https://searchfox.org/firefox-main/source/third_party/libwebrtc/docs/native-code/android/README.md)
- [Getstream WebRTC Android build guide](https://getstream.io/resources/projects/webrtc/library/android/)
- [webrtc-sdk/android](https://github.com/webrtc-sdk/android)
- [webrtc-sdk organization](https://repos.ecosyste.ms/hosts/GitHub/owners/webrtc-sdk)
