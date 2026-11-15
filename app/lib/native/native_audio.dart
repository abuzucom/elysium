import 'package:flutter/services.dart';

/// Dart side of the native audio bridge.
///
/// Native code owns WebRTC, PCM, and rendering (D-012, D-013). Dart only
/// reads status and the approved one-second diagnostics summary
/// (docs/contracts/pcm-block-provenance-v0.md). No audio crosses this bridge.

/// State of the native renderer.
enum NativeRendererState {
  /// Native code is a stub. No renderer exists yet.
  stub,
}

/// Recovery state reported in the diagnostics summary.
enum RecoveryState { normal, gradualCorrection, majorRecovery, degraded }

const _rendererStates = {'stub': NativeRendererState.stub};

const _recoveryStates = {
  'normal': RecoveryState.normal,
  'gradual_correction': RecoveryState.gradualCorrection,
  'major_recovery': RecoveryState.majorRecovery,
  'degraded': RecoveryState.degraded,
};

/// D-051 stage scores in integer percent. Zero marks an invalid stage.
const _validScores = {0, 25, 50, 75, 100};

/// Native bridge status.
class NativeStatus {
  const NativeStatus({
    required this.platform,
    required this.renderer,
    required this.contractMajor,
    required this.contractMinor,
  });

  /// Decodes a status map from native code. Throws [FormatException] on a
  /// missing field or an unknown value.
  factory NativeStatus.fromMap(Map<Object?, Object?> map) {
    return NativeStatus(
      platform: _string(map, 'platform'),
      renderer: _lookup(map, 'renderer', _rendererStates),
      contractMajor: _nonNegativeInt(map, 'contractMajor'),
      contractMinor: _nonNegativeInt(map, 'contractMinor'),
    );
  }

  final String platform;
  final NativeRendererState renderer;

  /// PCM block and provenance contract version the native side implements.
  final int contractMajor;
  final int contractMinor;
}

/// Rolling one-second diagnostics summary (approved contract v0).
class DiagnosticsSummary {
  const DiagnosticsSummary({
    required this.latencyNs,
    required this.uncertaintyNs,
    required this.minimumStageScore,
    required this.recoveryState,
    required this.syntheticFrames,
    required this.insertedFrames,
    required this.removedFrames,
  });

  /// Decodes a summary map from native code. Throws [FormatException] on a
  /// missing field, an unknown recovery state, a score outside the D-051
  /// values, or a negative count or uncertainty.
  factory DiagnosticsSummary.fromMap(Map<Object?, Object?> map) {
    final score = _nonNegativeInt(map, 'minimumStageScore');
    if (!_validScores.contains(score)) {
      throw FormatException('minimumStageScore $score is not a D-051 value');
    }
    return DiagnosticsSummary(
      latencyNs: _int(map, 'latencyNs'),
      uncertaintyNs: _nonNegativeInt(map, 'uncertaintyNs'),
      minimumStageScore: score,
      recoveryState: _lookup(map, 'recoveryState', _recoveryStates),
      syntheticFrames: _nonNegativeInt(map, 'syntheticFrames'),
      insertedFrames: _nonNegativeInt(map, 'insertedFrames'),
      removedFrames: _nonNegativeInt(map, 'removedFrames'),
    );
  }

  /// Capture-to-output latency estimate in nanoseconds.
  final int latencyNs;
  final int uncertaintyNs;

  /// Lowest stage score over the last second.
  final int minimumStageScore;
  final RecoveryState recoveryState;

  /// Frame counts over the last second.
  final int syntheticFrames;
  final int insertedFrames;
  final int removedFrames;
}

/// Client for the native audio bridge channels.
class NativeAudio {
  const NativeAudio();

  static const methodChannel = MethodChannel('com.abuzucom.listener/native_audio');
  static const diagnosticsChannel = EventChannel('com.abuzucom.listener/diagnostics');

  /// Reads the native bridge status.
  Future<NativeStatus> status() async {
    final result = await methodChannel.invokeMapMethod<Object?, Object?>('getStatus');
    if (result == null) {
      throw const FormatException('getStatus returned no status');
    }
    return NativeStatus.fromMap(result);
  }

  /// Streams diagnostics summaries, about one per second.
  Stream<DiagnosticsSummary> diagnostics() {
    return diagnosticsChannel.receiveBroadcastStream().map((event) {
      if (event is! Map<Object?, Object?>) {
        throw FormatException('diagnostics event is not a map: ${event.runtimeType}');
      }
      return DiagnosticsSummary.fromMap(event);
    });
  }
}

Object _require(Map<Object?, Object?> map, String key) {
  final value = map[key];
  if (value == null) {
    throw FormatException('missing field $key');
  }
  return value;
}

String _string(Map<Object?, Object?> map, String key) {
  final value = _require(map, key);
  if (value is! String) {
    throw FormatException('field $key is not a string');
  }
  return value;
}

int _int(Map<Object?, Object?> map, String key) {
  final value = _require(map, key);
  if (value is! int) {
    throw FormatException('field $key is not an integer');
  }
  return value;
}

int _nonNegativeInt(Map<Object?, Object?> map, String key) {
  final value = _int(map, key);
  if (value < 0) {
    throw FormatException('field $key is negative: $value');
  }
  return value;
}

T _lookup<T>(Map<Object?, Object?> map, String key, Map<String, T> values) {
  final name = _string(map, key);
  final value = values[name];
  if (value == null) {
    throw FormatException('field $key has unknown value $name');
  }
  return value;
}
