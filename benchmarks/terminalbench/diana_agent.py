"""Harbor 0.1.44 adapter; the actual agent loop runs in Diana's Go binary."""
import json
import os
import shlex
import tempfile
import uuid
from pathlib import Path

from harbor.agents.base import BaseAgent
from harbor.models.agent.context import AgentContext


class DianaAgent(BaseAgent):
    def __init__(self, *args, binary=None, config=None, seconds=900,
                 output_budget=32768, revision="unknown", **kwargs):
        super().__init__(*args, **kwargs)
        self.binary = Path(binary or os.environ["DIANA_BENCH_BINARY"]).resolve()
        self.config_path = Path(config or os.environ["DIANA_BENCH_CONFIG"]).resolve()
        self.seconds = int(seconds)
        self.output_budget = int(output_budget)
        self.revision = revision

    @staticmethod
    def name():
        return "diana"

    def version(self):
        return self.revision

    async def setup(self, environment):
        if self.mcp_servers:
            raise ValueError("This native-tools adapter does not support task MCP servers")
        await environment.upload_file(self.binary, "/tmp/diana-terminalbench")
        result = await environment.exec("chmod 755 /tmp/diana-terminalbench && mkdir -p /logs/agent")
        if result.return_code:
            raise RuntimeError("Diana installation failed")

    async def run(self, instruction, environment, context: AgentContext):
        config = json.loads(self.config_path.read_text())
        if self.model_name and config["model"] != self.model_name.split("/", 1)[-1]:
            raise ValueError("Harbor model and Diana provider config disagree")
        payload = {"config": config, "instruction": instruction}
        remote = "/tmp/diana-input-" + uuid.uuid4().hex
        with tempfile.TemporaryDirectory(prefix="diana-bench-") as tmp:
            path = Path(tmp) / "input.json"
            path.touch(mode=0o600)
            path.write_text(json.dumps(payload))
            await environment.upload_file(path, remote)
        # Open stdin then unlink the credential-bearing payload before the agent starts.
        # No credentials in command arguments or Harbor's command logs.
        command = (
            f"exec 3<{shlex.quote(remote)}; rm -f {shlex.quote(remote)}; "
            f"/tmp/diana-terminalbench --timeout {self.seconds} "
            f"--output-budget {self.output_budget} <&3"
        )
        try:
            result = await environment.exec(command, timeout_sec=self.seconds + 30)
            self.logs_dir.mkdir(parents=True, exist_ok=True)
            execution = json.dumps(result.model_dump(), ensure_ascii=False)
            if config.get("api_key"):
                execution = execution.replace(config["api_key"], "[REDACTED]")
            (self.logs_dir / "execution.json").write_text(execution)
        finally:
            self.logs_dir.mkdir(parents=True, exist_ok=True)
            await environment.download_file("/logs/agent/diana.jsonl", self.logs_dir / "diana.jsonl")
            self.populate_context(context)
        if result.return_code:
            raise RuntimeError(f"Diana exited with code {result.return_code}; see execution.json and diana.jsonl")

    def populate_context(self, context):
        events = [json.loads(line) for line in (self.logs_dir / "diana.jsonl").read_text().splitlines() if line]
        # Model events survive a deadline even if no final result was written.
        usages = [(e.get("response") or {}).get("usage", {}) for e in events if e.get("kind") == "model"]
        context.n_input_tokens = sum(u.get("input_tokens", 0) for u in usages)
        context.n_output_tokens = sum(u.get("output_tokens", 0) for u in usages)
        context.n_cache_tokens = sum(u.get("cached_input_tokens", 0) for u in usages)
        finals = [e for e in events if e.get("kind") == "result"]
        context.metadata = {"native_log": "diana.jsonl", "revision": self.revision,
                            "finish_reason": ((finals[-1].get("response") or {}).get("finish_reason") if finals else "interrupted")}
