# Elysium listener app

Flutter shell for the venue listener app (Gate 3 Phase A). The shell holds
the UI only. Native iOS and Android code owns WebRTC and audio rendering,
and audio never flows through Dart (D-013).

- Application identifier: `com.abuzucom.listener`
- Flutter 3.47.9 (Dart 3.13.7), pinned in CI
- `livekit_client` 2.13.2 with `flutter_webrtc` 1.6.3 and prebuilt,
  unpatched WebRTC 150.7871.04
- Minimum platforms: Android 8.0 (API 26), iOS 15

Joining an event stays disabled until the Gate 5 admission and session flow
exists (`docs/design/event-flow-v0.md`).

## Checks

Run from this directory:

- `flutter analyze`
- `flutter test`
- `dart format --set-exit-if-changed lib test`
