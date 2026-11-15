import Flutter
import UIKit

@main
@objc class AppDelegate: FlutterAppDelegate, FlutterImplicitEngineDelegate {
  override func application(
    _ application: UIApplication,
    didFinishLaunchingWithOptions launchOptions: [UIApplication.LaunchOptionsKey: Any]?
  ) -> Bool {
    return super.application(application, didFinishLaunchingWithOptions: launchOptions)
  }

  func didInitializeImplicitFlutterEngine(_ engineBridge: FlutterImplicitEngineBridge) {
    GeneratedPluginRegistrant.register(with: engineBridge.pluginRegistry)
    if let registrar = engineBridge.pluginRegistry.registrar(forPlugin: "NativeAudioBridge") {
      NativeAudioBridge.register(with: registrar)
    }
  }
}

/// iOS side of the native audio bridge (stub).
///
/// Native code owns WebRTC, PCM, and rendering (D-012, D-013). The bridge only
/// reports status and, later, the approved one-second diagnostics summary.
/// No audio crosses it. The diagnostics stream emits nothing until a native
/// renderer exists. The class lives in this file to avoid hand-editing the
/// Xcode project.
final class NativeAudioBridge: NSObject, FlutterPlugin, FlutterStreamHandler {
  static let methodChannelName = "com.abuzucom.listener/native_audio"
  static let diagnosticsChannelName = "com.abuzucom.listener/diagnostics"

  // PCM block and provenance contract version (docs/contracts).
  private static let contractMajor = 0
  private static let contractMinor = 1

  static func register(with registrar: FlutterPluginRegistrar) {
    let bridge = NativeAudioBridge()
    let methods = FlutterMethodChannel(name: methodChannelName, binaryMessenger: registrar.messenger())
    registrar.addMethodCallDelegate(bridge, channel: methods)
    let diagnostics = FlutterEventChannel(name: diagnosticsChannelName, binaryMessenger: registrar.messenger())
    diagnostics.setStreamHandler(bridge)
  }

  func handle(_ call: FlutterMethodCall, result: @escaping FlutterResult) {
    switch call.method {
    case "getStatus":
      result([
        "platform": "ios",
        "renderer": "stub",
        "contractMajor": NativeAudioBridge.contractMajor,
        "contractMinor": NativeAudioBridge.contractMinor,
      ])
    default:
      result(FlutterMethodNotImplemented)
    }
  }

  func onListen(withArguments arguments: Any?, eventSink events: @escaping FlutterEventSink) -> FlutterError? {
    // The stub renderer produces no diagnostics yet.
    return nil
  }

  func onCancel(withArguments arguments: Any?) -> FlutterError? {
    // Nothing to release while the renderer is a stub.
    return nil
  }
}
