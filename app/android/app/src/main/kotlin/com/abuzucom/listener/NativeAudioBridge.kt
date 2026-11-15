package com.abuzucom.listener

import io.flutter.plugin.common.BinaryMessenger
import io.flutter.plugin.common.EventChannel
import io.flutter.plugin.common.MethodCall
import io.flutter.plugin.common.MethodChannel

/**
 * Android side of the native audio bridge (stub).
 *
 * Native code owns WebRTC, PCM, and rendering (D-012, D-013). The bridge only
 * reports status and, later, the approved one-second diagnostics summary.
 * No audio crosses it. The diagnostics stream emits nothing until a native
 * renderer exists.
 */
class NativeAudioBridge private constructor() : MethodChannel.MethodCallHandler, EventChannel.StreamHandler {
    companion object {
        const val METHOD_CHANNEL = "com.abuzucom.listener/native_audio"
        const val DIAGNOSTICS_CHANNEL = "com.abuzucom.listener/diagnostics"

        // PCM block and provenance contract version (docs/contracts).
        private const val CONTRACT_MAJOR = 0
        private const val CONTRACT_MINOR = 1

        /** Registers the bridge channels on [messenger]. */
        fun register(messenger: BinaryMessenger) {
            val bridge = NativeAudioBridge()
            MethodChannel(messenger, METHOD_CHANNEL).setMethodCallHandler(bridge)
            EventChannel(messenger, DIAGNOSTICS_CHANNEL).setStreamHandler(bridge)
        }
    }

    override fun onMethodCall(call: MethodCall, result: MethodChannel.Result) {
        when (call.method) {
            "getStatus" -> result.success(
                mapOf(
                    "platform" to "android",
                    "renderer" to "stub",
                    "contractMajor" to CONTRACT_MAJOR,
                    "contractMinor" to CONTRACT_MINOR,
                ),
            )
            else -> result.notImplemented()
        }
    }

    override fun onListen(arguments: Any?, events: EventChannel.EventSink?) {
        // The stub renderer produces no diagnostics yet.
    }

    override fun onCancel(arguments: Any?) {
        // Nothing to release while the renderer is a stub.
    }
}
