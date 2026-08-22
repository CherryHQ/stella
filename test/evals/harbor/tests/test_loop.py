import os
import shlex
import subprocess
import time
from pathlib import Path


ROOT = Path(__file__).parents[4]
LOOP = ROOT / "test/evals/harbor/loop.sh"
BUILD = ROOT / "test/evals/harbor/eval_build.sh"
WRAPPER = ROOT / "test/evals/harbor/stellad_wrapper.sh"


def plan(*args):
    env = os.environ | {"OPENAI_BASE_URL": "https://gateway.example.invalid/v1", "OPENAI_API_KEY": "do-not-print-this-secret"}
    return subprocess.run(["bash", str(LOOP), "--plan", *args], cwd=ROOT, env=env, text=True, capture_output=True, check=True).stdout


def test_quick_plan_enables_otel_and_keeps_the_key_out_of_output():
    output = plan("--tier", "quick")
    assert "docker run -d grafana/otel-lgtm" in output
    assert "temporary stellad wrapper" in output
    assert "explicit concurrency -n 6" in output
    assert "do-not-print-this-secret" not in output


def test_full_plan_keeps_baseline_telemetry_off_unless_overridden():
    assert "disabled (full baseline default)" in plan()
    assert "docker run -d grafana/otel-lgtm" in plan("--otel")


def test_caller_source_and_concurrency_remain_supported():
    output = plan("-d", "terminal-bench/custom", "-n", "9")
    assert "source: caller-supplied" in output
    assert "caller-supplied concurrency" in output


def test_quick_plan_stages_the_allowlisted_wrapper_before_testbed_start():
    output = plan("--tier", "quick")
    assert "atomically move dist/bin/stellad to a private real-binary path" in output
    assert "raises OTEL_BSP_MAX_QUEUE_SIZE for the six-trial wave" in output
    assert "Before testbed start" in output


def test_wrapper_injects_only_the_allowlisted_otel_environment_and_restores(tmp_path):
    binary = tmp_path / "dist/bin/stellad"
    binary.parent.mkdir(parents=True)
    original = "#!/usr/bin/env bash\nenv | awk -F= '/^OTEL_/ {print}' | sort\n"
    binary.write_text(original)
    binary.chmod(0o755)
    real = binary.parent / ".stellad-eval-real-test"
    observed = tmp_path / "observed.txt"

    script = f"""
set -euo pipefail
source {shlex.quote(str(WRAPPER))}
stage_otel_stellad_wrapper {shlex.quote(str(binary))} {shlex.quote(str(real))} http://127.0.0.1:4318
env -i PATH="$PATH" {shlex.quote(str(binary))} check > {shlex.quote(str(observed))}
restore_stellad_binary {shlex.quote(str(binary))} {shlex.quote(str(real))}
"""
    subprocess.run(["bash", "-c", script], check=True)

    assert observed.read_text().splitlines() == [
        "OTEL_BSP_MAX_EXPORT_BATCH_SIZE=2048",
        "OTEL_BSP_MAX_QUEUE_SIZE=16384",
        "OTEL_BSP_SCHEDULE_DELAY=1000",
        "OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4318",
        "OTEL_EXPORTER_OTLP_INSECURE=true",
        "OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf",
        "OTEL_LOGS_EXPORTER=none",
        "OTEL_METRICS_EXPORTER=none",
        "OTEL_SERVICE_NAME=stella-eval",
    ]
    assert binary.read_text() == original
    assert not real.exists()

    # The wrapper existed long enough to execute the real binary. Inspect the
    # emitted script in a separate stage so assertions cover its exact allowlist.
    inspect = tmp_path / "wrapper.txt"
    script = f"""
set -euo pipefail
source {shlex.quote(str(WRAPPER))}
stage_otel_stellad_wrapper {shlex.quote(str(binary))} {shlex.quote(str(real))} http://127.0.0.1:4318
cat {shlex.quote(str(binary))} > {shlex.quote(str(inspect))}
restore_stellad_binary {shlex.quote(str(binary))} {shlex.quote(str(real))}
"""
    subprocess.run(["bash", "-c", script], check=True)
    exports = [line for line in inspect.read_text().splitlines() if line.startswith("export OTEL_")]
    assert exports == [
        "export OTEL_SERVICE_NAME=stella-eval",
        "export OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4318",
        "export OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf",
        "export OTEL_EXPORTER_OTLP_INSECURE=true",
        "export OTEL_BSP_MAX_QUEUE_SIZE=16384",
        "export OTEL_BSP_MAX_EXPORT_BATCH_SIZE=2048",
        "export OTEL_BSP_SCHEDULE_DELAY=1000",
        "export OTEL_LOGS_EXPORTER=none",
        "export OTEL_METRICS_EXPORTER=none",
    ]


def test_wrapper_quotes_repository_paths_instead_of_executing_them(tmp_path):
    marker = tmp_path / "injected"
    hostile = tmp_path / f"repo$(touch {marker})" / "dist/bin"
    hostile.mkdir(parents=True)
    binary = hostile / "stellad"
    binary.write_text("#!/usr/bin/env bash\nexit 0\n")
    binary.chmod(0o755)
    real = hostile / ".stellad-eval-real-test"
    script = f"""
set -euo pipefail
source {shlex.quote(str(WRAPPER))}
stage_otel_stellad_wrapper {shlex.quote(str(binary))} {shlex.quote(str(real))} http://127.0.0.1:4318
{shlex.quote(str(binary))}
restore_stellad_binary {shlex.quote(str(binary))} {shlex.quote(str(real))}
"""
    subprocess.run(["bash", "-c", script], check=True)
    assert not marker.exists()


def test_stale_wrapper_is_recovered_before_freshness_checks(tmp_path):
    binary = tmp_path / "dist/bin/stellad"
    binary.parent.mkdir(parents=True)
    binary.write_text("#!/usr/bin/env bash\n# stella-eval-otel-wrapper\n")
    binary.chmod(0o700)
    real = binary.parent / ".stellad-eval-real-crash"
    real.write_text("real binary")
    script = f"""
set -euo pipefail
source {shlex.quote(str(WRAPPER))}
recover_stale_stellad_binary {shlex.quote(str(binary))}
"""
    subprocess.run(["bash", "-c", script], check=True)
    assert binary.read_text() == "real binary"
    assert not real.exists()


def test_freshness_helper_reuses_newer_binary_and_detects_newer_source(tmp_path):
    binary = tmp_path / "stellad"
    source = tmp_path / "source.go"
    source.write_text("old")
    binary.write_text("binary")
    binary.chmod(0o755)
    now = time.time()
    os.utime(binary, (now + 10, now + 10))

    script = f"""
set -euo pipefail
source {shlex.quote(str(BUILD))}
if untracked_go_sources_newer {shlex.quote(str(binary))} {shlex.quote(str(tmp_path))}; then exit 1; fi
python3 -c 'import os, sys, time; os.utime(sys.argv[1], (time.time() + 20, time.time() + 20))' {shlex.quote(str(source))}
untracked_go_sources_newer {shlex.quote(str(binary))} {shlex.quote(str(tmp_path))}
"""
    subprocess.run(["bash", "-c", script], check=True)


def test_mise_keeps_only_the_eval_loop_and_fresh_build_tasks():
    mise = (ROOT / "mise.toml").read_text()
    assert '[tasks."eval:loop"]' in mise
    assert '[tasks."eval:build"]' in mise
    assert '[tasks."eval:testbed:start"]' not in mise
    assert '[tasks."eval:runtime"]' not in mise


def test_attached_short_options_select_the_right_source_and_concurrency():
    output = plan("-iterminal-bench/regex-log", "-n6")
    assert "task filter given" in output
    assert "caller-supplied concurrency" in output


def test_filtered_runs_do_not_claim_the_full_taskset_in_the_manifest():
    source = LOOP.read_text()
    assert 'using_taskset=0' in source
    assert 'TASKSET_PATH=$([ "$using_taskset" = 1 ]' in source


def test_manifest_records_only_harbor_option_names_not_values():
    source = LOOP.read_text()
    assert 'harbor_flags = [arg.split("=", 1)[0]' in source
    assert '"harbor_args": harbor_flags' in source


def test_reuse_rejects_non_loopback_urls_before_sending_credentials(tmp_path):
    credentials = tmp_path / "credentials.json"
    credentials.write_text("{}")
    env = os.environ | {
        "OPENAI_BASE_URL": "https://gateway.example.invalid/v1",
        "OPENAI_API_KEY": "sentinel",
        "STELLA_URL": "https://attacker.example",
        "STELLA_TESTBED_CREDENTIALS": str(credentials),
        "STELLA_EVAL_BRIDGE_DIR": str(tmp_path / "bridge"),
    }
    result = subprocess.run(["bash", str(LOOP), "--reuse-testbed", "--no-otel"],
                            cwd=ROOT, env=env, text=True, capture_output=True)
    assert result.returncode != 0
    assert "loopback http://" in result.stderr


def test_reusing_an_existing_testbed_requires_otel_to_be_explicitly_disabled():
    result = subprocess.run(["bash", str(LOOP), "--plan", "--tier", "quick", "--reuse-testbed"], cwd=ROOT, text=True, capture_output=True)
    assert result.returncode != 0
    assert "cannot retrofit OTel" in result.stderr


def test_otel_wrapper_has_only_safe_exporter_settings_and_cleanup_restores_the_binary():
    wrapper = WRAPPER.read_text()
    assert "printf 'exec %s \"$@\"\\n' \"$quoted_real\"" in wrapper
    for key in ("OTEL_SERVICE_NAME", "OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_EXPORTER_OTLP_PROTOCOL",
                "OTEL_EXPORTER_OTLP_INSECURE", "OTEL_BSP_MAX_QUEUE_SIZE",
                "OTEL_BSP_MAX_EXPORT_BATCH_SIZE", "OTEL_BSP_SCHEDULE_DELAY",
                "OTEL_LOGS_EXPORTER", "OTEL_METRICS_EXPORTER"):
        assert key in wrapper
    assert wrapper.count("printf 'export OTEL_") == 9
    source = LOOP.read_text()
    cleanup = source.split("cleanup() {", 1)[1].split("}\ntrap cleanup", 1)[0]
    assert cleanup.index("mise run testbed:stop") < cleanup.index("restore_stellad_binary")
