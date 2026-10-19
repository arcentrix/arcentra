import unittest
from verify_containerd_test_log import verify
from verify_inbox_mysql_log import expected


class InboxMySQLLogTests(unittest.TestCase):
    def test_every_case_required(self):
        for suite, count in [('provenance', 3), ('migration', 3), ('service', 9)]:
            names = expected(suite)
            self.assertEqual(len(names), count)
            rows = [f'=== RUN   {name}\n--- PASS: {name} (0.01s)' for name in names]
            text = '\n'.join(rows) + '\nPASS\n'
            verify(text, 1, names)
            for changed in [text.replace(rows[-1], ''), text + rows[-1], text.replace('--- PASS:', '--- SKIP:', 1), text.replace('--- PASS:', '--- FAIL:', 1), text.replace('\nPASS\n', '')]:
                with self.assertRaises(ValueError):
                    verify(changed, 1, names)

    def test_unknown_suite_rejected(self):
        with self.assertRaises(ValueError):
            expected('all')
