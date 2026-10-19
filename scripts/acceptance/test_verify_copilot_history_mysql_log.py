import unittest
from verify_copilot_history_mysql_log import ROOT, verify

class RequiredHistoryMySQLLogTest(unittest.TestCase):
    def valid(self):
        return '\n'.join(['=== RUN   '+name+'\n--- PASS: '+name+' (0.01s)' for name in
            (ROOT,ROOT+'/utf8mb4_0900_ai_ci',ROOT+'/utf8mb4_bin')])+ '\nok github.com/arcentrix/arcentra/internal/control/repo 1.0s\n'
    def test_complete(self): verify(self.valid())
    def test_skip_missing_duplicate_and_failure(self):
        for text in (self.valid().replace('/utf8mb4_bin','/other'), self.valid()+'\n--- SKIP: other (0.0s)',
                     self.valid()+'\nFAIL',self.valid()+'\n=== RUN   '+ROOT):
            with self.assertRaises(ValueError): verify(text)

if __name__ == '__main__': unittest.main()
