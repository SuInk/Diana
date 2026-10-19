import json
import tempfile
import unittest
from pathlib import Path

from harbor.models.agent.context import AgentContext
from diana_agent import DianaAgent


class AdapterTest(unittest.TestCase):
    def test_partial_trajectory_usage_survives_timeout(self):
        with tempfile.TemporaryDirectory() as tmp:
            logs = Path(tmp)
            events = [
                {"kind": "model", "response": {"usage": {"input_tokens": 100, "cached_input_tokens": 40, "output_tokens": 5}}},
                {"kind": "model", "error": "timeout", "response": None},
            ]
            (logs / "diana.jsonl").write_text("\n".join(json.dumps(e) for e in events))
            agent = DianaAgent(logs_dir=logs, binary="/unused", config="/unused")
            context = AgentContext()
            agent.populate_context(context)
            self.assertEqual(context.n_input_tokens, 100)
            self.assertEqual(context.n_cache_tokens, 40)
            self.assertEqual(context.n_output_tokens, 5)
            self.assertEqual(context.metadata["finish_reason"], "interrupted")


if __name__ == "__main__":
    unittest.main()
