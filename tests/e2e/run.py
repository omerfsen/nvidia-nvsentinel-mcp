#!/usr/bin/env python3
"""Run the scenarios on the lab through the MCP server, as an MCP client would.

  tests/e2e/run.py                 the server from this checkout
  tests/e2e/run.py --image IMAGE   the server in its container

Writes one report per scenario to stdout and exits non-zero if any fails. Needs
the running lab (make lab-up), so it is not part of `make test`.
"""

import argparse
import json
import os
import sys
from pathlib import Path

import anyio
from mcp import Client
from mcp.client.stdio import StdioServerParameters


def server(image: str | None) -> StdioServerParameters:
    home = Path.home()
    if image is None:
        env = {
            **os.environ,
            "NVSENTINEL_MCP_KUBECONFIG": str(home / ".kube/nvsentinel-lab-mcp.yaml"),
            "NVSENTINEL_MCP_ALLOW_CHANGES": "true",
        }
        return StdioServerParameters(command=sys.executable, args=["-m", "nvsentinel_mcp.server"], env=env)
    args = [
        "run", "--rm", "-i", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges",
        f"--user={os.getuid()}:{os.getgid()}", "--tmpfs=/tmp:rw,noexec,nosuid,size=32m",
        "--memory=256m", "--cpus=1", "--pids-limit=64", "--network=kind",
        "--volume", f"{home}/.kube/nvsentinel-lab-mcp-docker.yaml:/kube/config:ro",
        "--env=NVSENTINEL_MCP_ALLOW_CHANGES=true", image,
    ]  # fmt: skip
    return StdioServerParameters(command="docker", args=args, env=dict(os.environ))


async def run(image: str | None) -> bool:
    passed = True
    async with Client(server(image), read_timeout_seconds=1800) as client:
        names = (await client.call_tool("list_scenarios")).structured_content["scenarios"]
        for name in names:
            result = await client.call_tool("run_test_scenario", {"name": name})
            report = result.structured_content or {"passed": False, "error": result.content[0].text}
            print(json.dumps(report, indent=2))
            passed = passed and report["passed"]
    return passed


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--image")
    return 0 if anyio.run(run, parser.parse_args().image) else 1


if __name__ == "__main__":
    sys.exit(main())
