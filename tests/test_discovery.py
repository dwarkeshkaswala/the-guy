"""Agent discovery & adapter configuration.

Covers: explicit overrides (env + persisted config), PATH lookup, well-known
install locations, Windows .exe/.cmd/.bat priority, .ps1 execution-policy
handling, stale configured paths, missing executables, VS Code extension
non-assumption, probe-cache rediscovery, and no-silent-fallback.
"""

from __future__ import annotations

import os
from pathlib import Path

import pytest

from conftest import (mock_task, plan_spec, register, write_fake_cli,
                      write_plan_file)


def _tasks_by_id():
    from the_guy import config
    from the_guy.store import Store
    store = Store(config.db_path())
    try:
        return {t.id: t for t in store.list_tasks()}
    finally:
        store.close()


def test_config_override_with_spaces_and_cmd_shim(hermetic, tmp_path, monkeypatch):
    from the_guy import config
    from the_guy.adapters import get

    tools = tmp_path / "tools with spaces"
    fake = write_fake_cli(tools, "claude", tokens=["--print"])
    config.set_adapter_override("claude", str(fake))
    report = get("claude").probe()
    assert report.installed and report.headless_verified
    assert Path(report.executable) == fake
    assert report.source == "config"

    # env var override beats the persisted config
    other = write_fake_cli(tmp_path / "other dir", "claude", tokens=["--print"],
                           version="env override 9.9.9")
    monkeypatch.setenv("GUY_CLAUDE_PATH", str(other))
    report = get("claude").probe()
    assert report.source == "env"
    assert Path(report.executable) == other


def test_path_discovery_with_spaces(hermetic, monkeypatch, tmp_path):
    d = tmp_path / "path dir with spaces"
    fake = write_fake_cli(d, "claude", tokens=["--print"])
    monkeypatch.setenv("PATH", str(d) + os.pathsep + os.environ.get("PATH", ""))
    from the_guy.adapters import get
    report = get("claude").probe()
    assert report.installed and report.headless_verified
    assert report.source == "PATH"
    assert Path(report.executable) == fake
    assert report.discovery["path_dirs_scanned"] >= 1


def test_well_known_root_and_exe_priority(hermetic, monkeypatch):
    from the_guy import discovery
    bin_dir = Path.home() / ".local" / "bin"  # hermetic fake home
    if os.name == "nt":
        (bin_dir / "claude.cmd").write_text("@echo off", encoding="utf-8")
        stub_exe = bin_dir / "claude.exe"
        stub_exe.write_text("MZ discovery-only stub", encoding="utf-8")
        result = discovery.resolve(["claude"], "GUY_CLAUDE_PATH", None)
        assert result.chosen == stub_exe  # .exe beats .cmd in the same dir
    else:
        stub = bin_dir / "claude"
        stub.write_text("#!/bin/sh\n", encoding="utf-8")
        stub.chmod(0o755)
        result = discovery.resolve(["claude"], "GUY_CLAUDE_PATH", None)
        assert result.chosen == stub
    assert result.chosen_source == "well-known"
    roots = [w["root"] for w in result.well_known]
    assert str(bin_dir) in roots


def test_stale_override_disables_without_fallback(hermetic, tmp_path):
    from the_guy import config
    from the_guy.adapters import get

    fake = write_fake_cli(tmp_path / "gone dir", "claude", tokens=["--print"])
    config.set_adapter_override("claude", str(fake))
    fake.unlink()
    report = get("claude").probe()
    assert not report.installed
    assert not report.headless_verified
    assert report.executable is None  # never silently fell back to anything else
    assert "no longer exists" in report.headless_note
    assert "guy agents clear" in report.headless_note
    assert "guy agents set" in report.headless_note


def test_missing_executable_reports_searched_locations(hermetic, monkeypatch):
    from the_guy import discovery
    empty = Path(os.environ["PATH"].split(os.pathsep)[0])
    result = discovery.resolve(["claude"], "GUY_CLAUDE_PATH", None)
    assert result.chosen is None
    assert not result.ps1_only
    assert result.path_dirs_scanned >= 1
    assert empty.exists()
    from the_guy.adapters import get
    report = get("claude").probe()
    assert not report.installed
    assert "no executable found" in report.headless_note
    assert "guy agents doctor" in report.headless_note


@pytest.mark.skipif(os.name != "nt", reason="Windows .ps1 shims")
class TestPowerShellPolicy:
    def test_ps1_only_is_never_chosen(self, hermetic, monkeypatch, tmp_path):
        d = tmp_path / "ps only dir"
        d.mkdir()
        (d / "claude.ps1").write_text("Write-Output 'must never run'", encoding="utf-8")
        monkeypatch.setenv("PATH", str(d) + os.pathsep + os.environ.get("PATH", ""))
        from the_guy import discovery
        from the_guy.adapters import get
        result = discovery.resolve(["claude"], "GUY_CLAUDE_PATH", None)
        assert result.chosen is None
        assert result.ps1_only is True
        assert result.ps1_path == d / "claude.ps1"
        report = get("claude").probe()
        assert not report.installed
        assert "PowerShell" in report.headless_note
        assert "execution policy" in report.headless_note

    def test_cmd_sibling_preferred_over_ps1(self, hermetic, monkeypatch, tmp_path):
        d = tmp_path / "mixed shims"
        d.mkdir()
        (d / "claude.ps1").write_text("Write-Output 'nope'", encoding="utf-8")
        fake_cmd = write_fake_cli(d, "claude", tokens=["--print"], ext=".cmd")
        monkeypatch.setenv("PATH", str(d) + os.pathsep + os.environ.get("PATH", ""))
        from the_guy.adapters import get
        report = get("claude").probe()
        assert report.installed and report.headless_verified
        assert Path(report.executable) == fake_cmd

    def test_override_pointing_at_ps1_refused(self, hermetic, tmp_path):
        d = tmp_path / "override ps"
        d.mkdir()
        ps1 = d / "claude.ps1"
        ps1.write_text("Write-Output 'nope'", encoding="utf-8")
        from the_guy import config, discovery
        result = discovery.resolve(["claude"], "GUY_CLAUDE_PATH", str(ps1))
        assert result.chosen is None
        assert result.override_error is not None
        assert "execution policy" in result.override_error


def test_vscode_extension_is_not_a_cli(hermetic, tmp_path, monkeypatch):
    ext_root = Path(os.environ["GUY_VSCODE_EXTENSIONS_DIR"])
    (ext_root / "anthropic.claude-code-1.0.0").mkdir()
    from the_guy.adapters import get
    report = get("claude").probe()
    assert not report.installed
    assert not report.headless_verified
    assert report.extension_hint == "anthropic.claude-code-1.0.0"
    assert any("NOT a global CLI" in n for n in report.notes)


def test_probe_cache_automatic_rediscovery(hermetic, tmp_path, monkeypatch):
    from the_guy import adapters, config

    dir_a = tmp_path / "cache dir a"
    fake_a = write_fake_cli(dir_a, "claude", tokens=["--print"], version="a 1.0")
    monkeypatch.setenv("PATH", str(dir_a))
    first = {r.adapter: r for r in adapters.probe_all_cached()}
    assert first["claude"].executable == str(fake_a)
    cache = config.data_dir() / "adapter-cache.json"
    assert cache.exists()

    second = {r.adapter: r for r in adapters.probe_all_cached()}
    assert second["claude"].executable == str(fake_a)  # cache hit

    # environment change -> fingerprint change -> automatic rediscovery
    fake_b = write_fake_cli(tmp_path / "cache dir b", "claude", tokens=["--print"],
                            version="b 2.0")
    monkeypatch.setenv("PATH", str(tmp_path / "cache dir b"))
    third = {r.adapter: r for r in adapters.probe_all_cached()}
    assert third["claude"].executable == str(fake_b)

    # executable deleted with no env change -> cached "installed" re-validated
    fake_b.unlink()
    fourth = {r.adapter: r for r in adapters.probe_all_cached()}
    assert fourth["claude"].installed is False


def test_agents_cli_set_clear_rescan_doctor(hermetic, guy, tmp_path, capsys):
    from the_guy import config

    fake = write_fake_cli(tmp_path / "cfg dir with spaces", "claude", tokens=["--print"])
    assert guy("agents", "set", "claude", str(fake)) == 0
    out = capsys.readouterr().out
    assert "verified, adapter ready" in out
    assert config.adapter_overrides()["claude"] == str(fake)

    assert guy("agents", "set", "claude", str(tmp_path / "missing.exe")) == 1
    assert "does not exist" in capsys.readouterr().err

    assert guy("agents", "set", "mock", str(fake)) == 1  # mock needs no override
    capsys.readouterr()

    assert guy("agents", "clear", "claude") == 0
    assert "claude" not in config.adapter_overrides()

    assert guy("agents", "rescan") == 0
    assert "adapter rescan" in capsys.readouterr().out

    assert guy("agents", "doctor") == 0
    doctor_out = capsys.readouterr().out
    assert "discovery diagnostics" in doctor_out
    assert "common install locations" in doctor_out

    assert guy("agents", "doctor", "claude") == 0
    capsys.readouterr()
    assert guy("agents", "doctor", "nope") == 1


def _make_factory_install(base: Path, version: str, with_bin: bool = True) -> Path:
    app = base / f"app-{version}"
    if with_bin:
        bin_dir = app / "resources" / "bin"
        bin_dir.mkdir(parents=True, exist_ok=True)
        (bin_dir / "droid.exe").write_text("MZ discovery-only stub", encoding="utf-8")
    else:
        app.mkdir(parents=True, exist_ok=True)
    return app


@pytest.mark.skipif(os.name != "nt", reason="Factory Droid installs are Windows-only")
class TestFactoryDroidDiscovery:
    def test_version_comparison_not_alphabetical(self, hermetic, monkeypatch, tmp_path):
        fake_local = tmp_path / "fake LocalAppData"
        base = fake_local / "Factory"
        for version in ("1.2.0", "1.9.0", "1.10.0"):  # alphabetically 1.9 > 1.10
            _make_factory_install(base, version)
        (base / "app-bogus" / "resources" / "bin").mkdir(parents=True)   # not versioned
        _make_factory_install(base, "0.9.0", with_bin=False)             # no bin dir
        monkeypatch.setenv("LOCALAPPDATA", str(fake_local))

        from the_guy import discovery
        roots = discovery.factory_droid_roots()
        assert roots[0][0] == base / "app-1.10.0" / "resources" / "bin"
        assert len(roots) == 3  # bogus + bin-less dirs excluded
        labels = [label for _p, label in roots if "app-1.10.0" in label]
        assert labels and labels[0].startswith("Factory Droid install")

        result = discovery.resolve(["droid"], "GUY_DROID_PATH", None)
        assert result.chosen == base / "app-1.10.0" / "resources" / "bin" / "droid.exe"
        assert result.chosen_source == "well-known"

    def test_newest_valid_wins_when_newest_is_broken(self, hermetic, monkeypatch, tmp_path):
        fake_local = tmp_path / "fake LocalAppData 2"
        base = fake_local / "Factory"
        _make_factory_install(base, "9.9.9", with_bin=False)  # newest but no bin
        _make_factory_install(base, "1.10.0")
        _make_factory_install(base, "1.9.0")
        monkeypatch.setenv("LOCALAPPDATA", str(fake_local))
        from the_guy import discovery
        roots = discovery.factory_droid_roots()
        assert roots[0][0] == base / "app-1.10.0" / "resources" / "bin"

    def test_override_beats_factory_dirs(self, hermetic, monkeypatch, tmp_path):
        fake_local = tmp_path / "fake LocalAppData 3"
        base = fake_local / "Factory"
        _make_factory_install(base, "1.10.0")
        monkeypatch.setenv("LOCALAPPDATA", str(fake_local))
        pinned = write_fake_cli(tmp_path / "pinned dir", "droid", tokens=["exec"])
        from the_guy import discovery
        result = discovery.resolve(["droid"], "GUY_DROID_PATH", str(pinned))
        assert result.chosen == pinned
        assert result.chosen_source == "config"

    def test_upgrade_triggers_cache_redetection(self, hermetic, monkeypatch, tmp_path):
        fake_local = tmp_path / "fake LocalAppData 4"
        base = fake_local / "Factory"
        _make_factory_install(base, "1.9.0")
        monkeypatch.setenv("LOCALAPPDATA", str(fake_local))
        from the_guy import adapters
        first = {r.adapter: r for r in adapters.probe_all_cached()}
        assert "app-1.9.0" in (first["droid"].executable or "")

        # an upgrade drops a new version dir: fingerprint changes -> automatic
        # re-detection picks the newest Factory install without `rescan`
        _make_factory_install(base, "1.10.0")
        second = {r.adapter: r for r in adapters.probe_all_cached()}
        assert "app-1.10.0" in (second["droid"].executable or "")


def test_npm_prefix_resolution_and_labels(hermetic, monkeypatch, tmp_path):
    prefix_dir = tmp_path / "custom npm prefix"
    write_fake_cli(prefix_dir, "claude", tokens=["--print"])
    npm_dir = tmp_path / "npm launcher dir"
    npm_dir.mkdir()
    if os.name == "nt":
        (npm_dir / "npm.cmd").write_text(
            "@echo off\r\necho %GUY_FAKE_NPM_PREFIX%\r\n", encoding="utf-8")
    else:
        npm_sh = npm_dir / "npm"
        npm_sh.write_text('#!/bin/sh\necho "$GUY_FAKE_NPM_PREFIX"\n', encoding="utf-8")
        npm_sh.chmod(0o755)
    monkeypatch.setenv("GUY_FAKE_NPM_PREFIX", str(prefix_dir))
    monkeypatch.setenv("PATH", str(npm_dir) + os.pathsep + os.environ.get("PATH", ""))

    from the_guy import discovery
    assert discovery.npm_global_prefix() == prefix_dir
    result = discovery.resolve(["claude"], "GUY_CLAUDE_PATH", None)
    # .cmd preferred over the .ps1 sibling; found via the resolved npm prefix
    assert result.chosen == prefix_dir / ("claude.cmd" if os.name == "nt" else "claude")
    assert result.chosen_source == "well-known"
    labels = {w["root"]: w.get("label") for w in result.well_known}
    assert labels.get(str(prefix_dir)) == "npm global prefix"


def test_no_silent_fallback_when_verification_fails(hermetic, guy, py_repo,
                                                    tmp_path, monkeypatch, capsys):
    # a 'claude' whose --help lacks --print: discovery finds it, verification
    # refuses, and NOTHING else is launched instead
    d = tmp_path / "unverified dir"
    write_fake_cli(d, "claude", tokens=["exec-wrong-mode"])
    monkeypatch.setenv("PATH", str(d) + os.pathsep + os.environ.get("PATH", ""))

    register(guy, py_repo, "r")
    spec = plan_spec([mock_task(key="a", agent="claude", write=[("x.txt", "x")])])
    plan_file = write_plan_file(tmp_path, spec)
    assert guy("plan", "--file", str(plan_file)) == 0
    assert guy("approve", "PLAN-001") == 0
    assert guy("run", "--approve") == 1
    task = _tasks_by_id()["TASK-001"]
    assert task.status == "failed"
    assert task.agent == "claude"
    assert "does not mention" in (task.exit_reason or "")
    assert task.worktree is None  # no fallback agent ran, nothing was created
    capsys.readouterr()

    # agents listing keeps it truthfully disabled
    assert guy("agents") == 0
    assert "installed, headless unverified" in capsys.readouterr().out
