import 'dart:io';

import 'package:duanju_app/diary_service.dart';
import 'package:duanju_app/playback_diagnostics.dart';
import 'package:flutter_test/flutter_test.dart';

void main() {
  test('localhost session, credentials and query never enter playback logs', () {
    const token = '0123456789abcdef0123456789abcdef0123456789abcdef';
    final safe = playbackDiagnosticUrl(
      'http://user:password@127.0.0.1:51037/$token/media.m3u8?token=secret#private',
    );
    expect(safe, contains('id=${playbackDiagnosticId(token)}'));
    expect(safe, contains('media.m3u8'));
    for (final secret in [token, 'user', 'password', 'secret', 'private']) {
      expect(safe, isNot(contains(secret)));
    }
    final error = playbackDiagnosticError(
      'failed https://user:password@cdn.test/media.ts?token=secret authorization: Bearer private',
    );
    expect(error, contains('cdn.test/media.ts'));
    expect(error, isNot(contains('secret')));
    expect(error, isNot(contains('private')));
    expect(playbackDiagnosticUrl('file:///private/local.mp4'), '[local-file]');
  });

  test(
    'diary copies bounded native stream events and skips partial records',
    () async {
      final directory = await Directory.systemTemp.createTemp('stream-diary-');
      addTearDown(() async {
        DiaryService.nativeLogPath = null;
        DiaryService.clear();
        await directory.delete(recursive: true);
      });
      final file = File('${directory.path}/app.log');
      DiaryService.nativeLogPath = file.path;
      await file.writeAsString(
        '{"event":"stream.session_cancel","message":"reason=release"}\n',
      );
      await file.rename('${file.path}.1');
      await file.writeAsString(
        '${'x' * (128 * 1024)}\n'
        '{"event":"catalog.other","message":"not a playback event"}\n'
        '{"event":"stream.local_response","httpStatus":200}\n'
        '{"event":"stream.incomplete"',
      );
      await DiaryService.refreshNativeLogs();
      expect(DiaryService.fullText, contains('stream.session_cancel'));
      expect(DiaryService.fullText, contains('stream.local_response'));
      expect(DiaryService.fullText, isNot(contains('catalog.other')));
      expect(DiaryService.fullText, isNot(contains('stream.incomplete')));
      expect(DiaryService.entries.last, contains('stream.local_response'));
    },
  );
}
