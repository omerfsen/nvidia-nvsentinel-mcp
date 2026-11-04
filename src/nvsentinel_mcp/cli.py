"""The same operations as the MCP tools, from a shell."""

from __future__ import annotations

import argparse
import json
import sys

from .lab import Lab, LabError
from .scenarios import run_scenario


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(prog="nvsentinel-lab", description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)
    sub.add_parser("status")
    inject = sub.add_parser("inject")
    inject.add_argument("--xid", type=int, default=95)
    sub.add_parser("clear")
    wait = sub.add_parser("wait")
    wait.add_argument("stage")
    wait.add_argument("--timeout", type=int, default=180)
    logs = sub.add_parser("logs")
    logs.add_argument("component")
    logs.add_argument("--tail", type=int, default=100)
    scenario = sub.add_parser("scenario")
    scenario.add_argument("name")
    args = parser.parse_args(argv)

    lab = Lab()
    try:
        if args.command == "status":
            result = lab.status()
        elif args.command == "inject":
            result = lab.inject_fault(args.xid)
        elif args.command == "clear":
            result = lab.clear_fault()
        elif args.command == "wait":
            result = lab.wait_for(args.stage, timeout=args.timeout)
        elif args.command == "logs":
            print(lab.logs(args.component, args.tail))
            return 0
        else:
            result = run_scenario(lab, args.name)
    except LabError as err:
        print(f"error: {err}", file=sys.stderr)
        return 1
    print(json.dumps(result, indent=2))
    return 0 if result.get("passed", result.get("reached", True)) else 1


if __name__ == "__main__":
    sys.exit(main())
