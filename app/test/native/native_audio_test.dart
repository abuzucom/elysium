import 'package:flutter_test/flutter_test.dart';

import 'package:listener/native/native_audio.dart';

void main() {
  TestWidgetsFlutterBinding.ensureInitialized();
  final messenger = TestDefaultBinaryMessengerBinding.instance.defaultBinaryMessenger;

  tearDown(() {
    messenger.setMockMethodCallHandler(NativeAudio.methodChannel, null);
    messenger.setMockStreamHandler(NativeAudio.diagnosticsChannel, null);
  });

  group('status', () {
    test('decodes the native stub status', () async {
      messenger.setMockMethodCallHandler(NativeAudio.methodChannel, (call) async {
        expect(call.method, 'getStatus');
        return {'platform': 'android', 'renderer': 'stub', 'contractMajor': 0, 'contractMinor': 1};
      });

      final status = await const NativeAudio().status();

      expect(status.platform, 'android');
      expect(status.renderer, NativeRendererState.stub);
      expect(status.contractMajor, 0);
      expect(status.contractMinor, 1);
    });

    test('rejects an unknown renderer state', () async {
      messenger.setMockMethodCallHandler(NativeAudio.methodChannel, (call) async {
        return {'platform': 'ios', 'renderer': 'warp-drive', 'contractMajor': 0, 'contractMinor': 1};
      });

      expect(const NativeAudio().status(), throwsFormatException);
    });

    test('rejects a missing field', () async {
      messenger.setMockMethodCallHandler(NativeAudio.methodChannel, (call) async => {'renderer': 'stub'});

      expect(const NativeAudio().status(), throwsFormatException);
    });
  });

  group('diagnostics', () {
    test('decodes rolling summaries from the event channel', () async {
      messenger.setMockStreamHandler(
        NativeAudio.diagnosticsChannel,
        MockStreamHandler.inline(onListen: (arguments, events) => events.success(validSummary())),
      );

      final summary = await const NativeAudio().diagnostics().first;

      expect(summary.latencyNs, 72000000);
      expect(summary.recoveryState, RecoveryState.gradualCorrection);
    });
  });

  group('DiagnosticsSummary.fromMap', () {
    test('decodes every approved field', () {
      final summary = DiagnosticsSummary.fromMap(validSummary());

      expect(summary.latencyNs, 72000000);
      expect(summary.uncertaintyNs, 4000000);
      expect(summary.minimumStageScore, 75);
      expect(summary.recoveryState, RecoveryState.gradualCorrection);
      expect(summary.syntheticFrames, 0);
      expect(summary.insertedFrames, 12);
      expect(summary.removedFrames, 480);
    });

    for (final state in const {
      'normal': RecoveryState.normal,
      'gradual_correction': RecoveryState.gradualCorrection,
      'major_recovery': RecoveryState.majorRecovery,
      'degraded': RecoveryState.degraded,
    }.entries) {
      test('decodes recovery state ${state.key}', () {
        final summary = DiagnosticsSummary.fromMap(validSummary()..['recoveryState'] = state.key);

        expect(summary.recoveryState, state.value);
      });
    }

    test('rejects a score outside the D-051 values', () {
      expect(() => DiagnosticsSummary.fromMap(validSummary()..['minimumStageScore'] = 60), throwsFormatException);
    });

    test('rejects negative frame counts', () {
      expect(() => DiagnosticsSummary.fromMap(validSummary()..['removedFrames'] = -1), throwsFormatException);
    });

    test('rejects negative uncertainty', () {
      expect(() => DiagnosticsSummary.fromMap(validSummary()..['uncertaintyNs'] = -5), throwsFormatException);
    });

    test('rejects an unknown recovery state', () {
      expect(() => DiagnosticsSummary.fromMap(validSummary()..['recoveryState'] = 'panic'), throwsFormatException);
    });
  });
}

Map<String, Object?> validSummary() => {
  'latencyNs': 72000000,
  'uncertaintyNs': 4000000,
  'minimumStageScore': 75,
  'recoveryState': 'gradual_correction',
  'syntheticFrames': 0,
  'insertedFrames': 12,
  'removedFrames': 480,
};
