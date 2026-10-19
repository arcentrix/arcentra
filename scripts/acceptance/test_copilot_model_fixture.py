import copy
import unittest
from copilot_model_fixture import MODEL, validate_request


class ModelContractTest(unittest.TestCase):
    def body(self):
        return dict(model=MODEL, messages=[dict(role='system', content='Read only.'),
                                          dict(role='user', content='Selected redacted log.')],
                    max_completion_tokens=1024, stream=False, store=False)
    def test_exact_text_only_contract(self):
        self.assertEqual(validate_request(self.body(), ['canary']), validate_request(self.body(), ['canary']))
    def test_fields_roles_and_bounds_fail_closed(self):
        mutations = [lambda b: b.update(tools=[]), lambda b: b.update(store=True),
                     lambda b: b.update(stream=True), lambda b: b.update(model='other'),
                     lambda b: b.update(max_completion_tokens=True),
                     lambda b: b['messages'][1].update(role='tool'),
                     lambda b: b['messages'][1].update(content='x' * 32769),
                     lambda b: b['messages'][1].update(content='secret-canary')]
        for mutate in mutations:
            with self.subTest(mutation=mutate):
                value = copy.deepcopy(self.body())
                mutate(value)
                with self.assertRaises(ValueError):
                    validate_request(value, ['secret-canary'])

if __name__ == '__main__':
    unittest.main()
