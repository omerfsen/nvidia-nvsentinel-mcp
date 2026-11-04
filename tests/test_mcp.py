"""The server as a client sees it: a real subprocess, speaking MCP over stdio."""

import json
import sys

import anyio
import pytest
from mcp import Client
from mcp.client.stdio import StdioServerParameters

EXPECTED_TOOLS = {
    "lab_status": True,
    "list_scenarios": True,
    "wait_for_stage": True,
    "component_logs": True,
    "node_events": True,
    "inject_fault": False,
    "clear_fault": False,
    "run_test_scenario": False,
}


def session(env, body, stderr=None):
    params = StdioServerParameters(command=sys.executable, args=["-m", "nvsentinel_mcp.server"], env=env)

    async def run():
        async with Client(params, read_timeout_seconds=30) as client:
            return await body(client)

    return anyio.run(run)


def call(env, tool, arguments=None):
    async def body(client):
        return await client.call_tool(tool, arguments or {})

    return session(env, body)


def test_tools_are_listed_and_marked_read_or_change(server_env):
    async def body(client):
        return (await client.list_tools()).tools

    tools = session(server_env, body)
    assert {t.name: t.annotations.read_only_hint for t in tools} == EXPECTED_TOOLS
    for tool in tools:
        assert tool.annotations.destructive_hint is (not EXPECTED_TOOLS[tool.name])
        assert tool.description


def test_schemas_name_the_allowed_values(server_env):
    async def body(client):
        return {t.name: t.input_schema for t in (await client.list_tools()).tools}

    schemas = session(server_env, body)
    assert (
        "enum" in schemas["run_test_scenario"]["properties"]["name"]
        or "$ref" in schemas["run_test_scenario"]["properties"]["name"]
    )
    assert schemas["wait_for_stage"]["properties"]["timeout_seconds"]["maximum"] == 900
    assert schemas["component_logs"]["properties"]["tail"]["maximum"] == 2000


def test_a_scenario_runs_through_the_server_with_progress(server_env, cluster):
    seen = []

    async def progress(value, total, message):
        seen.append(message)

    async def body(client):
        return await client.call_tool("run_test_scenario", {"name": "xid95"}, progress_callback=progress)

    result = session(server_env, body)
    assert not result.is_error, result
    report = result.structured_content
    assert report["passed"] is True
    assert "wait:cordoned" in [step["step"] for step in report["steps"]]
    assert "inject_xid95" in seen


def test_a_refusal_comes_back_as_a_tool_error(server_env, cluster):
    cluster.set(context="nvke-mgmt-admin")
    result = call(server_env, "inject_fault", {"xid": 95})
    assert result.is_error
    assert "not the lab's" in result.content[0].text
    assert not cluster.changed


def test_changes_off_comes_back_as_a_tool_error(server_env, cluster):
    env = {k: v for k, v in server_env.items() if k != "NVSENTINEL_MCP_ALLOW_CHANGES"}
    result = call(env, "run_test_scenario", {"name": "xid95"})
    assert result.is_error
    assert "changes are off" in result.content[0].text
    assert not cluster.changed


@pytest.mark.parametrize(
    ("tool", "arguments"),
    [
        ("run_test_scenario", {"name": "nope"}),
        ("inject_fault", {"xid": 13}),
        ("inject_fault", {"xid": "95; rm -rf /"}),
        ("wait_for_stage", {"stage": "cordoned", "timeout_seconds": 10**9}),
        ("wait_for_stage", {"stage": "--help"}),
        ("component_logs", {"component": "--help"}),
        ("component_logs", {"component": "node-drainer", "tail": 10**9}),
        ("node_events", {"limit": 10**9}),
    ],
)
def test_bad_arguments_are_refused_and_never_reach_kubectl(server_env, cluster, tool, arguments):
    result = call(server_env, tool, arguments)
    assert result.is_error
    assert cluster.calls == []


def test_a_crash_shows_no_detail(server_env, cluster):
    # A kubectl that prints something that is not JSON makes lab.py crash, not refuse.
    cluster.set(fail_get=None)
    env = {**server_env, "NVSENTINEL_MCP_KUBECTL": "/bin/echo"}
    result = call(env, "lab_status")
    assert result.is_error
    assert result.content[0].text == "Error executing tool lab_status"


def test_every_change_is_audited_on_stderr(server_env, cluster, tmp_path):
    log = tmp_path / "stderr.log"
    redirect = f"import os;os.dup2(os.open({str(log)!r},os.O_WRONLY|os.O_CREAT),2)"
    start = f"{redirect};from nvsentinel_mcp.server import main;main()"
    params = StdioServerParameters(command=sys.executable, args=["-c", start], env=server_env)

    async def run():
        async with Client(params, read_timeout_seconds=30) as client:
            await client.call_tool("inject_fault", {"xid": 95})

    anyio.run(run)
    records = [json.loads(line.split(" ", 2)[2]) for line in log.read_text().splitlines() if '"event": "change"' in line]
    assert records[0]["tool"] == "inject_fault"
    assert records[0]["arguments"] == {"xid": 95}
    assert records[0]["outcome"] == "ok"
