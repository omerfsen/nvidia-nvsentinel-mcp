import json
import os
from pathlib import Path

import pytest

FAKE_KUBECTL = str(Path(__file__).parent / "fakes" / "kubectl")


class FakeCluster:
    def __init__(self, path: Path) -> None:
        self.path = path

    def set(self, **values) -> None:
        self.path.write_text(json.dumps({**self.read(), **values}))

    def read(self) -> dict:
        return json.loads(self.path.read_text()) if self.path.exists() else {}

    @property
    def calls(self) -> list[list[str]]:
        return self.read().get("calls", [])

    @property
    def changed(self) -> bool:
        """Whether anything but reading reached the cluster."""
        return any(call[0] in {"exec", "delete", "patch"} for call in self.calls)


@pytest.fixture
def kubeconfig(tmp_path) -> Path:
    path = tmp_path / "kubeconfig"
    path.write_text("not read by the fake kubectl\n")
    path.chmod(0o600)
    return path


@pytest.fixture
def cluster(tmp_path, kubeconfig, monkeypatch) -> FakeCluster:
    monkeypatch.setenv("NVSENTINEL_MCP_KUBECTL", FAKE_KUBECTL)
    monkeypatch.setenv("NVSENTINEL_MCP_KUBECONFIG", str(kubeconfig))
    monkeypatch.setenv("NVSENTINEL_MCP_ALLOW_CHANGES", "true")
    return FakeCluster(tmp_path / "state.json")


@pytest.fixture
def server_env(cluster) -> dict[str, str]:
    return dict(os.environ)
