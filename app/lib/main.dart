import 'package:flutter/material.dart';

/// Entry point of the Elysium listener app.
///
/// This shell holds the UI only. Audio never flows through Dart (D-013).
/// Native code owns WebRTC and rendering.
void main() => runApp(const ListenerApp());

/// Root widget of the listener app.
class ListenerApp extends StatelessWidget {
  const ListenerApp({super.key});

  @override
  Widget build(BuildContext context) {
    return MaterialApp(
      title: 'Elysium',
      theme: ThemeData(colorSchemeSeed: Colors.indigo, useMaterial3: true),
      home: const HomeScreen(),
    );
  }
}

/// Home screen before the listener joins an event.
///
/// Joining stays disabled until the approved Gate 5 admission and session
/// flow exists (docs/design/event-flow-v0.md).
class HomeScreen extends StatelessWidget {
  const HomeScreen({super.key});

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    return Scaffold(
      appBar: AppBar(title: const Text('Elysium')),
      body: Center(
        child: Column(
          mainAxisSize: MainAxisSize.min,
          children: [
            Text('Not joined to an event', style: theme.textTheme.titleMedium),
            const SizedBox(height: 16),
            const FilledButton(onPressed: null, child: Text('Join event')),
          ],
        ),
      ),
    );
  }
}
