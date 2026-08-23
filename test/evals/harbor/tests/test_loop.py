import os
import shlex
import subprocess
import time
from pathlib import Path


ROOT = Path(__file__).parents[4]
LOOP = ROOT / "test/evals/harbor/loop.sh"
BUILD = ROOT / "test/evals/harbor/eval_build.sh"
WRAPPER = ROOT / "test/evals/harbor/stellad_wrapper.sh"
RUN_STATE = ROOT / "test/evals/harbor/run_state.sh"


def plan(*args):
    env = os.environ | {"OPENAI_BASE_URL": "https://gateway.example.invalid/v1", "OPENAI_API_KEY": "do-not-print-this-secret"}
    return subprocess.run(["bash", str(LOOP), "--plan", *args], cwd=ROOT, env=env, text=True, capture_output=True, check=True).stdout


def test_quick_plan_enables_otel_and_keeps_the_key_out_of_output():
    output = plan("--tier", "quick")
    assert "docker run -d grafana/otel-lgtm" in output
    assert "private stellad copy" in output
    assert "explicit concurrency -n 6" in output
    assert "do-not-print-this-secret" not in output


def test_plan_canonicalizes_and_reports_excluded_tools():
    output = plan("--excluded-tools", " write,read,write,edit ")
    assert "excluded tools: edit,read,view_image,vllm,write" in output


def test_plan_always_announces_the_bash_only_capability_ceiling():
    output = plan()
    assert "excluded tools: view_image,vllm" in output
    assert "bash-only" in output


def test_plan_defaults_to_native_and_accepts_only_the_two_tool_modes():
    assert "tool mode native" in plan()
    assert "tool mode code" in plan("--tool-mode", "code")
    result = subprocess.run(
        ["bash", str(LOOP), "--plan", "--tool-mode", "bogus"], cwd=ROOT,
        env=os.environ | {"OPENAI_BASE_URL": "https://gateway.example.invalid/v1", "OPENAI_API_KEY": "key"},
        text=True, capture_output=True,
    )
    assert result.returncode != 0
    assert "unknown tool mode bogus" in result.stderr


def test_loop_keeps_provider_evidence_pat_narrow_and_out_of_manifest():
    source = LOOP.read_text()
    assert "STELLA_EVAL_PROVIDER_EVIDENCE_TOKEN=$ADMIN_PAT" in source
    assert "export STELLA_EVAL_ADMIN_TOKEN STELLA_EVAL_PROVIDER_EVIDENCE_TOKEN" in source
    assert '"requested_gateway_host"' in source
    assert '"provider_evidence' not in source


def test_full_plan_keeps_baseline_telemetry_off_unless_overridden():
    assert "disabled (full baseline default)" in plan()
    assert "docker run -d grafana/otel-lgtm" in plan("--otel")


def test_caller_source_and_concurrency_remain_supported():
    output = plan("-d", "terminal-bench/custom", "-n", "9")
    assert "source: caller-supplied" in output
    assert "caller-supplied concurrency" in output


def test_quick_plan_uses_a_private_testbed_root_before_start():
    output = plan("--tier", "quick")
    assert "private stellad copy" in output
    assert "raises OTEL_BSP_MAX_QUEUE_SIZE for the six-trial wave" in output
    assert "shared dist/bin/stellad is never modified" in output
    assert "dist/evals/runs" in output


def test_wrapper_injects_only_the_allowlisted_otel_environment(tmp_path):
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
    assert real.read_text() == original
    exports = [line for line in binary.read_text().splitlines() if line.startswith("export OTEL_")]
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
"""
    subprocess.run(["bash", "-c", script], check=True)
    assert not marker.exists()


def test_loop_wraps_only_the_private_copy_and_never_shared_dist_bin():
    source = LOOP.read_text()
    assert 'cp "$REPO_ROOT/dist/bin/stellad" "$TESTBED_ROOT/dist/bin/stellad"' in source
    assert 'stage_otel_stellad_wrapper "$TESTBED_ROOT/dist/bin/stellad"' in source
    assert 'stage_otel_stellad_wrapper "$REPO_ROOT/dist/bin/stellad"' not in source


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


def test_dead_build_lock_is_not_automatically_recovered(tmp_path):
    dead = subprocess.Popen(["true"])
    dead.wait()
    lock = tmp_path / "dist/.eval-build.lock"
    lock.mkdir(parents=True)
    (lock / "pid").write_text(str(dead.pid))
    script = f"""
set -euo pipefail
source {shlex.quote(str(BUILD))}
export STELLA_EVAL_BUILD_LOCK_TIMEOUT=1
if acquire_build_lock; then exit 1; fi
test -d ./dist/.eval-build.lock
"""
    result = subprocess.run(["bash", "-c", script], cwd=tmp_path, text=True, capture_output=True)
    assert result.returncode == 0
    assert lock.is_dir()
    assert f"owner PID: {dead.pid}" in result.stderr
    assert "confirm that process is dead, then rm -rf ./dist/.eval-build.lock" in result.stderr


def test_run_state_claim_collision_retries_with_a_distinct_job_name(tmp_path):
    jobs = tmp_path / "jobs"
    runs = tmp_path / "runs"
    jobs.mkdir()
    runs.mkdir()
    base = jobs / "quick-20260822T120000Z"
    output = tmp_path / "claims.txt"
    script = f"""
set -euo pipefail
source {shlex.quote(str(RUN_STATE))}
claim_run_state {shlex.quote(str(base))} {shlex.quote(str(runs))} 4 111
printf '%s\n%s\n' "$CLAIMED_JOB" "$CLAIMED_RUN_STATE" > {shlex.quote(str(output))}
claim_run_state {shlex.quote(str(base))} {shlex.quote(str(runs))} 4 222
printf '%s\n%s\n' "$CLAIMED_JOB" "$CLAIMED_RUN_STATE" >> {shlex.quote(str(output))}
"""
    subprocess.run(["bash", "-c", script], check=True)
    first_job, first_state, second_job, second_state = output.read_text().splitlines()
    assert first_job != second_job
    assert first_state != second_state
    assert Path(first_state).name == Path(first_job).name
    assert Path(second_state).name == Path(second_job).name
    assert (Path(first_state) / "owner.pid").read_text().strip() == "111"
    assert (Path(second_state) / "owner.pid").read_text().strip() == "222"


def test_run_state_pruning_never_removes_a_live_owner_even_when_old(tmp_path):
    root = tmp_path / "runs"
    live = root / "live"
    dead = root / "dead"
    partial = root / "partial"
    for path in (live, dead, partial):
        path.mkdir(parents=True)
    (live / "owner.pid").write_text(str(os.getpid()))
    exited = subprocess.Popen(["true"])
    exited.wait()
    (dead / "owner.pid").write_text(str(exited.pid))
    old = time.time() - 3600
    os.utime(live / "owner.pid", (old, old))
    os.utime(dead / "owner.pid", (old, old))
    os.utime(partial, (old, old))

    script = f"""
set -euo pipefail
source {shlex.quote(str(RUN_STATE))}
prune_stale_run_states {shlex.quote(str(root))} 1
"""
    subprocess.run(["bash", "-c", script], check=True)
    assert live.is_dir()
    assert not dead.exists()
    assert partial.is_dir()  # no owner PID: may be between mkdir and PID write


def test_loop_never_uses_mkdir_p_as_the_run_state_claim():
    source = LOOP.read_text()
    assert 'claim_run_state "$JOB_BASE" "$RUNS_ROOT"' in source
    assert 'JOB=$CLAIMED_JOB' in source
    assert 'RUN_STATE=$CLAIMED_RUN_STATE' in source
    assert source.index('RUN_STATE=$CLAIMED_RUN_STATE') < source.index('set_run_paths\nbuild_harbor_cmd')
    assert 'mkdir -p "$RUN_STATE"' not in source


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


def test_excluded_tools_reach_the_driver_and_manifest():
    source = LOOP.read_text()
    assert "export STELLA_EVAL_EXCLUDED_TOOLS=$EXCLUDED_TOOLS" in source
    assert '"excluded_tools": os.environ["EXCLUDED_TOOLS"].split(",")' in source
    assert "export STELLA_AGENT_TOOL_MODE=$TOOL_MODE" in source
    assert "STELLA_EVAL_TOOL_MODE=$TOOL_MODE" in source
    assert '"tool_mode": os.environ["TOOL_MODE"]' in source
    assert 'status.get("agent_tool_mode") == os.environ["TOOL_MODE"]' in source


def test_manifest_records_only_harbor_option_names_not_values():
    source = LOOP.read_text()
    assert 'harbor_flags = [arg.split("=", 1)[0]' in source
    assert '"harbor_args": harbor_flags' in source


def test_against_supplies_the_run_k_the_comparator_cannot_verify():
    # Harbor omits n_attempts from config.json when it equals the default of 1,
    # so a k=1 job records no budget and the comparator fails closed. loop.sh
    # has to restate the k it ran, read from the run's own printed config.
    source = LOOP.read_text()
    assert 'RUN_K=$(python3 -c \'import json,sys; print(json.load(open(sys.argv[1])).get("n_attempts", 1))\' "$WORK/config.json")' in source
    assert 'stella_harbor.compare "$JOB" "$AGAINST" --k "$RUN_K"' in source
    assert source.index('--print-config') < source.index('RUN_K=')


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


def test_otel_wrapper_has_only_safe_exporter_settings_and_private_cleanup():
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
    assert '(cd "$TESTBED_ROOT" && ./dist/bin/testbed stop)' in cleanup
    assert cleanup.index('./dist/bin/testbed stop') < cleanup.index('rm -rf "$RUN_STATE"')
    assert "restore_stellad_binary" not in source
