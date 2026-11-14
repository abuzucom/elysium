import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';

import 'package:listener/main.dart';

void main() {
  testWidgets('home screen shows the app name and not-joined status', (tester) async {
    await tester.pumpWidget(const ListenerApp());

    expect(find.text('Elysium'), findsOneWidget);
    expect(find.text('Not joined to an event'), findsOneWidget);
  });

  testWidgets('join stays disabled until admission exists', (tester) async {
    await tester.pumpWidget(const ListenerApp());

    final join = tester.widget<FilledButton>(find.widgetWithText(FilledButton, 'Join event'));
    expect(join.onPressed, isNull);
  });
}
