#!/usr/bin/env python3
"""Run the complete Terminal-Bench 2.1 evaluation on disposable AWS compute.

The command owns the whole lifecycle: local plan preflight, temporary IAM/S3/
Secrets Manager/EC2 resources, SSM bootstrap, five ordered k=1 passes merged as
k=5, redacted artifact retrieval, and cloud cleanup. The split avoids Harbor's
per-trial resident-memory growth without changing the selected five attempts.
"""

from __future__ import annotations

import argparse
import base64
import datetime as dt
import hashlib
import json
import os
import random
import re
import shlex
import shutil
import signal
import subprocess
import sys
import tempfile
import time
from pathlib import Path
from typing import Any

DATASET = "terminal-bench/terminal-bench-2-1"
DEFAULT_INSTANCE = "c7i.8xlarge"
DEFAULT_CONCURRENCY = 16
DEFAULT_PASSES = 5
DEFAULT_TIMEOUT_HOURS = 24
AMI_PARAMETER = "/aws/service/canonical/ubuntu/server/24.04/stable/current/amd64/hvm/ebs-gp3/ami-id"


def interrupt_as_keyboard(_signum: int, _frame: Any) -> None:
    raise KeyboardInterrupt


class RunJournal:
    def __init__(self, directory: Path) -> None:
        self.directory = directory
        self.directory.mkdir(parents=True, exist_ok=True)
        self.path = directory / "journal.ndjson"

    def record(self, event: str, **fields: Any) -> None:
        entry = {
            "at": dt.datetime.now(dt.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
            "event": event,
        } | fields
        with self.path.open("a") as stream:
            stream.write(json.dumps(entry, sort_keys=True) + "\n")
        detail = fields.get("phase") or fields.get("message") or ""
        try:
            print(f"[{entry['at']}] {event}{': ' + str(detail) if detail else ''}", flush=True)
        except BrokenPipeError:
            # The console may disappear during a long run; the disk journal remains authoritative.
            # Redirect the descriptor so Python's shutdown flush cannot fail on the same pipe.
            with open(os.devnull, "w") as sink:
                os.dup2(sink.fileno(), sys.stdout.fileno())


class Aws:
    def __init__(self, region: str, journal: RunJournal) -> None:
        self.region = region
        self.journal = journal

    def run(
        self,
        *args: str,
        json_output: bool = False,
        check: bool = True,
    ) -> Any:
        command = ["aws", *args]
        env = os.environ | {"AWS_DEFAULT_REGION": self.region}
        result = subprocess.run(command, text=True, capture_output=True, env=env, check=False)
        if check and result.returncode != 0:
            message = (result.stderr or result.stdout).strip()
            raise RuntimeError(f"aws {' '.join(args[:2])} failed: {message}")
        if json_output:
            return json.loads(result.stdout)
        return result.stdout.strip()


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def atomic_json(path: Path, value: dict[str, Any]) -> None:
    temporary = path.with_suffix(path.suffix + ".tmp")
    temporary.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n")
    temporary.replace(path)


def require_environment() -> tuple[str, dict[str, str]]:
    required = (
        "AWS_REGION",
        "OPENAI_BASE_URL",
        "OPENAI_API_KEY",
        "OPENAI_MODEL",
        "EVAL_COST_INPUT",
        "EVAL_COST_OUTPUT",
        "EVAL_COST_CACHE_READ",
        "EVAL_COST_CACHE_WRITE",
    )
    missing = [name for name in required if not os.environ.get(name)]
    if missing:
        raise RuntimeError("missing environment variables: " + ", ".join(missing))
    if os.environ.get("OTEL_STELLA_RECORD_TOOL_IO"):
        raise RuntimeError("OTEL_STELLA_RECORD_TOOL_IO must be unset for Terminal-Bench")
    provider = {
        name: os.environ[name]
        for name in required
        if name.startswith(("OPENAI_", "EVAL_COST_"))
    }
    return os.environ["AWS_REGION"], provider


def repository_root() -> Path:
    output = subprocess.run(
        ["git", "rev-parse", "--show-toplevel"], text=True, capture_output=True, check=True
    ).stdout.strip()
    return Path(output).resolve()


def local_preflight(
    root: Path, commit_ref: str, concurrency: int, smoke: bool, journal: RunJournal
) -> str:
    for tool in ("aws", "git", "mise", "python3"):
        if shutil.which(tool) is None:
            raise RuntimeError(f"{tool} is required")
    subprocess.run(["git", "fetch", "origin", "main", "--quiet"], cwd=root, check=True)
    commit = subprocess.run(
        ["git", "rev-parse", f"{commit_ref}^{{commit}}"],
        cwd=root,
        text=True,
        capture_output=True,
        check=True,
    ).stdout.strip()
    source = (
        ["-c", str(root / "test/evals/harbor/tasksets/aws-smoke.yaml")]
        if smoke
        else ["-d", DATASET, "-k", "1"]
    )
    command = ["mise", "run", "eval:loop", "--", "--plan", *source, "-n", str(concurrency)]
    result = subprocess.run(command, cwd=root, text=True, capture_output=True, check=False)
    if result.returncode != 0 or "plan only, nothing is executed" not in result.stdout:
        raise RuntimeError(f"local eval plan failed: {(result.stderr or result.stdout).strip()}")
    if "OPENAI_BASE_URL (set" not in result.stdout or "OPENAI_API_KEY (set)" not in result.stdout:
        raise RuntimeError("local eval plan did not observe provider credentials")
    journal.record("local-preflight-complete", commit=commit, concurrency=concurrency)
    return commit


def update_state(path: Path, state: dict[str, Any], **fields: Any) -> None:
    state.update(fields)
    atomic_json(path, state)


def retry(action: Any, *, attempts: int = 24, delay: float = 5.0) -> None:
    error: Exception | None = None
    for _ in range(attempts):
        try:
            action()
            return
        except Exception as exc:  # noqa: BLE001  # AWS propagation errors are intentionally retried.
            error = exc
            time.sleep(delay)
    assert error is not None
    raise error


def cleanup(aws: Aws, state_path: Path, state: dict[str, Any], journal: RunJournal) -> None:
    if state.get("cleaned_at"):
        return
    errors: list[str] = []
    journal.record("cleanup-started")

    def absent(exc: Exception) -> bool:
        text = str(exc)
        return any(
            marker in text
            for marker in (
                "NoSuchEntity",
                "InvalidGroup.NotFound",
                "NoSuchBucket",
                "InvalidInstanceID.NotFound",
                "ResourceNotFoundException",
                "NotFoundException",
            )
        )

    instance = state.get("instance_id")
    if instance and not state.get("instance_deleted"):
        try:
            description = aws.run(
                "ec2",
                "describe-instances",
                "--instance-ids",
                instance,
                "--output",
                "json",
                json_output=True,
            )
            reservations = description.get("Reservations", [])
            instances = [entry for reservation in reservations for entry in reservation.get("Instances", [])]
            if not instances:
                # EC2 can return a successful empty reservation after the
                # janitor has terminated the instance. That is already clean.
                update_state(state_path, state, instance_deleted=True)
            else:
                current = instances[0].get("State", {}).get("Name")
                if current not in {"shutting-down", "terminated"}:
                    aws.run("ec2", "terminate-instances", "--instance-ids", instance)
                aws.run("ec2", "wait", "instance-terminated", "--instance-ids", instance)
                update_state(state_path, state, instance_deleted=True)
        except Exception as exc:  # noqa: BLE001  # cleanup must continue after each resource
            if not absent(exc):
                errors.append(f"instance: {exc}")

    schedule = state.get("janitor_schedule_name")
    if schedule and state.get("janitor_schedule_created"):
        try:
            aws.run("scheduler", "delete-schedule", "--name", schedule)
            update_state(state_path, state, janitor_schedule_created=False)
        except Exception as exc:  # noqa: BLE001
            if absent(exc):
                update_state(state_path, state, janitor_schedule_created=False)
            else:
                errors.append(f"janitor schedule: {exc}")

    janitor_role = state.get("janitor_role_name")
    if janitor_role and state.get("janitor_role_created") and not state.get("janitor_schedule_created"):
        try:
            aws.run(
                "iam",
                "delete-role-policy",
                "--role-name",
                janitor_role,
                "--policy-name",
                "TerminateStellaTB21Instance",
            )
            aws.run("iam", "delete-role", "--role-name", janitor_role)
            update_state(state_path, state, janitor_role_created=False)
        except Exception as exc:  # noqa: BLE001
            if absent(exc):
                update_state(state_path, state, janitor_role_created=False)
            else:
                errors.append(f"janitor role: {exc}")

    secret = state.get("secret_arn")
    if secret and not state.get("secret_deleted"):
        try:
            aws.run(
                "secretsmanager",
                "delete-secret",
                "--secret-id",
                secret,
                "--force-delete-without-recovery",
            )
            update_state(state_path, state, secret_deleted=True)
        except Exception as exc:  # noqa: BLE001
            if not absent(exc):
                errors.append(f"secret: {exc}")

    bucket = state.get("bucket")
    if bucket and state.get("bucket_created"):
        try:
            aws.run("s3", "rm", f"s3://{bucket}", "--recursive", "--only-show-errors")
            aws.run("s3api", "delete-bucket", "--bucket", bucket)
            update_state(state_path, state, bucket_created=False)
        except Exception as exc:  # noqa: BLE001
            if not absent(exc):
                errors.append(f"bucket: {exc}")

    profile = state.get("profile_name")
    role = state.get("role_name")
    if (
        profile
        and role
        and state.get("profile_created")
        and state.get("role_created")
        and not state.get("profile_role_removed")
    ):
        try:
            retry(
                lambda: aws.run(
                    "iam",
                    "remove-role-from-instance-profile",
                    "--instance-profile-name",
                    profile,
                    "--role-name",
                    role,
                )
            )
            update_state(state_path, state, profile_role_removed=True)
        except Exception as exc:  # noqa: BLE001
            if not absent(exc):
                errors.append(f"profile-role link: {exc}")
    if profile and state.get("profile_created"):
        try:
            aws.run("iam", "delete-instance-profile", "--instance-profile-name", profile)
            update_state(state_path, state, profile_created=False)
        except Exception as exc:  # noqa: BLE001
            if not absent(exc):
                errors.append(f"instance profile: {exc}")
    if role and state.get("role_created"):
        for operation in (
            (
                "delete-role-policy",
                "--role-name",
                role,
                "--policy-name",
                "StellaTB21Artifacts",
            ),
            (
                "detach-role-policy",
                "--role-name",
                role,
                "--policy-arn",
                "arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore",
            ),
            ("delete-role", "--role-name", role),
        ):
            try:
                aws.run("iam", *operation)
            except Exception as exc:  # noqa: BLE001
                if not absent(exc):
                    errors.append(f"iam {operation[0]}: {exc}")
        if not any(error.startswith("iam ") for error in errors):
            update_state(state_path, state, role_created=False)

    security_group = state.get("security_group_id")
    if security_group and not state.get("security_group_deleted"):
        try:
            retry(lambda: aws.run("ec2", "delete-security-group", "--group-id", security_group))
            update_state(state_path, state, security_group_deleted=True)
        except Exception as exc:  # noqa: BLE001
            if not absent(exc):
                errors.append(f"security group: {exc}")

    if errors:
        update_state(state_path, state, cleanup_errors=errors)
        journal.record("cleanup-incomplete", message="; ".join(errors))
        raise RuntimeError("temporary AWS cleanup incomplete: " + "; ".join(errors))
    cleaned_at = dt.datetime.now(dt.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
    update_state(state_path, state, cleaned_at=cleaned_at)
    journal.record("cleanup-complete")


def create_source_bundle(root: Path, bundle: Path, commit: str, run_id: str) -> None:
    """Create a cloneable bundle whose HEAD is exactly the candidate commit."""
    git_dir = subprocess.run(
        ["git", "rev-parse", "--absolute-git-dir"],
        cwd=root,
        text=True,
        capture_output=True,
        check=True,
    ).stdout.strip()
    with tempfile.TemporaryDirectory(prefix=f"{run_id}-bundle-") as temporary:
        bare = Path(temporary) / "source.git"
        subprocess.run(["git", "init", "--bare", "-q", str(bare)], check=True)
        alternates = bare / "objects/info/alternates"
        alternates.parent.mkdir(parents=True, exist_ok=True)
        alternates.write_text(str(Path(git_dir) / "objects") + "\n")
        subprocess.run(["git", f"--git-dir={bare}", "update-ref", "refs/heads/eval-target", commit], check=True)
        subprocess.run(
            ["git", f"--git-dir={bare}", "symbolic-ref", "HEAD", "refs/heads/eval-target"],
            check=True,
        )
        subprocess.run(["git", f"--git-dir={bare}", "bundle", "create", str(bundle), "HEAD"], check=True)
    subprocess.run(
        ["git", "bundle", "verify", str(bundle)], cwd=root, check=True, capture_output=True
    )


def write_secret_file(provider: dict[str, str], path: Path) -> None:
    path.write_text(json.dumps(provider))
    path.chmod(0o600)


def write_remote_config(path: Path, values: dict[str, Any]) -> None:
    lines = [f"{name}={shlex.quote(str(value))}" for name, value in values.items()]
    path.write_text("\n".join(lines) + "\n")
    path.chmod(0o600)


def provision(
    aws: Aws,
    root: Path,
    run_dir: Path,
    state_path: Path,
    state: dict[str, Any],
    provider: dict[str, str],
    commit_ref: str,
    journal: RunJournal,
) -> None:
    run_id = state["run_id"]
    account = aws.run("sts", "get-caller-identity", "--query", "Account", "--output", "text")
    suffix = f"{random.SystemRandom().randrange(16**8):08x}"
    bucket = f"stella-tb21-{account}-{run_id[-13:].lower()}-{suffix}"
    secret_name = f"stella/tb21/{run_id}"
    compact = re.sub(r"[^A-Za-z0-9]", "", run_id)[-36:]
    role = f"StellaTB21Role{compact}"
    profile = f"StellaTB21Profile{compact}"
    security_group_name = f"stella-tb21-{run_id.lower()}"
    update_state(
        state_path,
        state,
        account=account,
        bucket=bucket,
        secret_name=secret_name,
        role_name=role,
        profile_name=profile,
        security_group_name=security_group_name,
    )

    create_bucket = ["s3api", "create-bucket", "--bucket", bucket, "--region", state["region"]]
    if state["region"] != "us-east-1":
        create_bucket += [
            "--create-bucket-configuration",
            f"LocationConstraint={state['region']}",
        ]
    aws.run(*create_bucket)
    update_state(state_path, state, bucket_created=True)
    aws.run(
        "s3api",
        "put-public-access-block",
        "--bucket",
        bucket,
        "--public-access-block-configuration",
        "BlockPublicAcls=true,IgnorePublicAcls=true,BlockPublicPolicy=true,RestrictPublicBuckets=true",
    )
    aws.run(
        "s3api",
        "put-bucket-encryption",
        "--bucket",
        bucket,
        "--server-side-encryption-configuration",
        '{"Rules":[{"ApplyServerSideEncryptionByDefault":{"SSEAlgorithm":"AES256"}}]}',
    )
    journal.record("bucket-created", bucket=bucket)

    bundle = run_dir / "stella.bundle"
    # git clone ignores a bundle that exposes only refs/remotes/origin/main and
    # reports an empty repository. Publish the target as a normal branch.
    create_source_bundle(root, bundle, state["commit"], run_id)
    aws.run("s3", "cp", str(bundle), f"s3://{bucket}/input/stella.bundle", "--only-show-errors")
    bundle.unlink()
    for local, remote in (
        (root / "test/evals/harbor/aws_runner.sh", "input/aws_runner.sh"),
        (root / "test/evals/harbor/stella_harbor/aws_merge.py", "input/aws_merge.py"),
        (root / "test/evals/harbor/tasksets/aws-smoke.yaml", "input/aws-smoke.yaml"),
        (root / "test/evals/harbor/stella_harbor/aws_prepare.py", "input/aws_prepare.py"),
    ):
        aws.run("s3", "cp", str(local), f"s3://{bucket}/{remote}", "--only-show-errors")

    with tempfile.NamedTemporaryFile(dir=run_dir, delete=False) as stream:
        secret_file = Path(stream.name)
    try:
        write_secret_file(provider, secret_file)
        secret_arn = aws.run(
            "secretsmanager",
            "create-secret",
            "--name",
            secret_name,
            "--secret-string",
            f"file://{secret_file}",
            "--query",
            "ARN",
            "--output",
            "text",
        )
    finally:
        secret_file.unlink(missing_ok=True)
    update_state(state_path, state, secret_arn=secret_arn)
    journal.record("provider-secret-created")

    trust = run_dir / "trust.json"
    trust.write_text(
        json.dumps(
            {
                "Version": "2012-10-17",
                "Statement": [
                    {
                        "Effect": "Allow",
                        "Principal": {"Service": "ec2.amazonaws.com"},
                        "Action": "sts:AssumeRole",
                    }
                ],
            }
        )
    )
    aws.run(
        "iam",
        "create-role",
        "--role-name",
        role,
        "--assume-role-policy-document",
        f"file://{trust}",
    )
    update_state(state_path, state, role_created=True)
    aws.run(
        "iam",
        "attach-role-policy",
        "--role-name",
        role,
        "--policy-arn",
        "arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore",
    )
    access = run_dir / "access.json"
    access.write_text(
        json.dumps(
            {
                "Version": "2012-10-17",
                "Statement": [
                    {
                        "Effect": "Allow",
                        "Action": ["secretsmanager:GetSecretValue"],
                        "Resource": secret_arn,
                    },
                    {
                        "Effect": "Allow",
                        "Action": ["s3:GetObject", "s3:PutObject", "s3:ListBucket"],
                        "Resource": [f"arn:aws:s3:::{bucket}", f"arn:aws:s3:::{bucket}/*"],
                    },
                ],
            }
        )
    )
    aws.run(
        "iam",
        "put-role-policy",
        "--role-name",
        role,
        "--policy-name",
        "StellaTB21Artifacts",
        "--policy-document",
        f"file://{access}",
    )
    aws.run("iam", "create-instance-profile", "--instance-profile-name", profile)
    update_state(state_path, state, profile_created=True)
    aws.run(
        "iam",
        "add-role-to-instance-profile",
        "--instance-profile-name",
        profile,
        "--role-name",
        role,
    )
    journal.record("iam-created")

    vpcs = aws.run(
        "ec2",
        "describe-vpcs",
        "--filters",
        "Name=isDefault,Values=true",
        "--output",
        "json",
        json_output=True,
    )["Vpcs"]
    if len(vpcs) != 1:
        raise RuntimeError(f"expected one default VPC, found {len(vpcs)}")
    vpc = vpcs[0]["VpcId"]
    subnets = aws.run(
        "ec2",
        "describe-subnets",
        "--filters",
        f"Name=vpc-id,Values={vpc}",
        "Name=default-for-az,Values=true",
        "--output",
        "json",
        json_output=True,
    )["Subnets"]
    offerings = aws.run(
        "ec2",
        "describe-instance-type-offerings",
        "--location-type",
        "availability-zone",
        "--filters",
        f"Name=instance-type,Values={state['instance_type']}",
        "--output",
        "json",
        json_output=True,
    )["InstanceTypeOfferings"]
    zones = {item["Location"] for item in offerings}
    choices = sorted((item for item in subnets if item["AvailabilityZone"] in zones), key=lambda x: x["AvailabilityZone"])
    if not choices:
        raise RuntimeError(f"{state['instance_type']} has no offering in a default subnet")
    subnet = choices[0]
    security_group = aws.run(
        "ec2",
        "create-security-group",
        "--group-name",
        security_group_name,
        "--description",
        "Temporary Stella Terminal-Bench evaluation; no ingress",
        "--vpc-id",
        vpc,
        "--query",
        "GroupId",
        "--output",
        "text",
    )
    update_state(
        state_path,
        state,
        vpc_id=vpc,
        subnet_id=subnet["SubnetId"],
        availability_zone=subnet["AvailabilityZone"],
        security_group_id=security_group,
    )

    ami = aws.run("ssm", "get-parameter", "--name", AMI_PARAMETER, "--query", "Parameter.Value", "--output", "text")
    time.sleep(10)  # IAM instance-profile propagation has no waiter.
    tags = json.dumps(
        [
            {
                "ResourceType": "instance",
                "Tags": [
                    {"Key": "Name", "Value": f"stella-{run_id}"},
                    {"Key": "stella-eval", "Value": run_id},
                    {"Key": "expires-after", "Value": f"{state['timeout_hours']}h"},
                ],
            }
        ]
    )
    block = json.dumps(
        [
            {
                "DeviceName": "/dev/sda1",
                "Ebs": {
                    "VolumeSize": 300,
                    "VolumeType": "gp3",
                    "Encrypted": True,
                    "DeleteOnTermination": True,
                },
            }
        ]
    )
    launched = aws.run(
        "ec2",
        "run-instances",
        "--image-id",
        ami,
        "--instance-type",
        state["instance_type"],
        "--iam-instance-profile",
        f"Name={profile}",
        "--security-group-ids",
        security_group,
        "--subnet-id",
        subnet["SubnetId"],
        "--block-device-mappings",
        block,
        "--instance-initiated-shutdown-behavior",
        "terminate",
        "--metadata-options",
        "HttpTokens=required,HttpEndpoint=enabled",
        "--tag-specifications",
        tags,
        "--output",
        "json",
        json_output=True,
    )
    instance = launched["Instances"][0]["InstanceId"]
    update_state(state_path, state, instance_id=instance, ami_id=ami)

    # An external one-shot scheduler terminates compute even if the controller,
    # SSM agent, and guest watchdog all fail at once.
    janitor_role = f"StellaTB21Janitor{compact}"
    janitor_schedule = f"stella-tb21-{run_id.lower()}"
    janitor_trust = run_dir / "janitor-trust.json"
    janitor_trust.write_text(
        json.dumps(
            {
                "Version": "2012-10-17",
                "Statement": [
                    {
                        "Effect": "Allow",
                        "Principal": {"Service": "scheduler.amazonaws.com"},
                        "Action": "sts:AssumeRole",
                    }
                ],
            }
        )
    )
    created_janitor = aws.run(
        "iam",
        "create-role",
        "--role-name",
        janitor_role,
        "--assume-role-policy-document",
        f"file://{janitor_trust}",
        "--output",
        "json",
        json_output=True,
    )
    janitor_role_arn = created_janitor["Role"]["Arn"]
    update_state(
        state_path,
        state,
        janitor_role_name=janitor_role,
        janitor_role_created=True,
    )
    janitor_policy = run_dir / "janitor-policy.json"
    janitor_policy.write_text(
        json.dumps(
            {
                "Version": "2012-10-17",
                "Statement": [
                    {
                        "Effect": "Allow",
                        "Action": "ec2:TerminateInstances",
                        "Resource": f"arn:aws:ec2:{state['region']}:{account}:instance/{instance}",
                    }
                ],
            }
        )
    )
    aws.run(
        "iam",
        "put-role-policy",
        "--role-name",
        janitor_role,
        "--policy-name",
        "TerminateStellaTB21Instance",
        "--policy-document",
        f"file://{janitor_policy}",
    )
    expires = dt.datetime.now(dt.timezone.utc) + dt.timedelta(hours=state["timeout_hours"])
    expires_at = expires.strftime("%Y-%m-%dT%H:%M:%S")
    target = json.dumps(
        {
            "Arn": "arn:aws:scheduler:::aws-sdk:ec2:terminateInstances",
            "RoleArn": janitor_role_arn,
            "Input": json.dumps({"InstanceIds": [instance]}),
        }
    )
    time.sleep(10)
    aws.run(
        "scheduler",
        "create-schedule",
        "--name",
        janitor_schedule,
        "--schedule-expression",
        f"at({expires_at})",
        "--schedule-expression-timezone",
        "UTC",
        "--flexible-time-window",
        '{"Mode":"OFF"}',
        "--action-after-completion",
        "DELETE",
        "--target",
        target,
    )
    update_state(
        state_path,
        state,
        janitor_schedule_name=janitor_schedule,
        janitor_schedule_created=True,
        lease_expires_at=expires.strftime("%Y-%m-%dT%H:%M:%SZ"),
    )

    config = run_dir / "remote-config.env"
    write_remote_config(
        config,
        {
            "RUN_ID": run_id,
            "COMMIT": state["commit"],
            "REGION": state["region"],
            "BUCKET": bucket,
            "SECRET_ARN": secret_arn,
            "MODEL_ID": state["model_id"],
            "CONCURRENCY": state["concurrency"],
            "PASSES": state["passes"],
            "EXPECTED_TASKS": state["expected_tasks"],
            "RUN_MODE": state["run_mode"],
            "SAMPLE_MINUTES": state.get("sample_minutes", 0),
            "WARMUP_MODE": state["warmup_mode"],
            "WARMUP_CONCURRENCY": state["warmup_concurrency"],
            "TOPUP_CONCURRENCY": state["topup_concurrency"],
            "MAX_TOPUP_ROUNDS": state["max_topup_rounds"],
            "INSTANCE_TYPE": state["instance_type"],
            "INSTANCE_ID": instance,
            "AMI_ID": ami,
            "AVAILABILITY_ZONE": subnet["AvailabilityZone"],
            "CONTROLLER_COMMIT": state["controller_commit"],
            "REMOTE_RUNNER_SHA256": state["remote_runner_sha256"],
            "MERGE_HELPER_SHA256": state["merge_helper_sha256"],
            "SMOKE_TASKSET_SHA256": state["smoke_taskset_sha256"],
            "PREPARE_HELPER_SHA256": state["prepare_helper_sha256"],
        },
    )
    aws.run("s3", "cp", str(config), f"s3://{bucket}/input/remote-config.env", "--only-show-errors")
    config.unlink()
    journal.record("instance-created", instance_id=instance, availability_zone=subnet["AvailabilityZone"])


def wait_for_ssm(aws: Aws, instance: str, timeout: int, journal: RunJournal) -> None:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        response = aws.run(
            "ssm",
            "describe-instance-information",
            "--filters",
            f"Key=InstanceIds,Values={instance}",
            "--output",
            "json",
            json_output=True,
        )
        entries = response.get("InstanceInformationList", [])
        if entries and entries[0].get("PingStatus") == "Online":
            journal.record("ssm-online")
            return
        time.sleep(10)
    raise RuntimeError("SSM did not become online within 15 minutes")


def start_remote(aws: Aws, state: dict[str, Any], state_path: Path, journal: RunJournal) -> str:
    bucket = state["bucket"]
    timeout_minutes = state["timeout_hours"] * 60
    bootstrap = f"""#!/usr/bin/env bash
set -Eeuo pipefail
export HOME=/root
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq ca-certificates curl unzip
if ! command -v aws >/dev/null 2>&1; then
  curl -fsSL https://awscli.amazonaws.com/awscli-exe-linux-x86_64.zip -o /tmp/awscliv2.zip
  rm -rf /tmp/aws && unzip -q /tmp/awscliv2.zip -d /tmp
  /tmp/aws/install --update
  rm -rf /tmp/aws /tmp/awscliv2.zip
fi
mkdir -p /opt/stella-tb21/stella_harbor
aws s3 cp s3://{bucket}/input/aws_runner.sh /opt/stella-tb21/aws_runner.sh --only-show-errors
aws s3 cp s3://{bucket}/input/aws_merge.py /opt/stella-tb21/aws_merge.py --only-show-errors
aws s3 cp s3://{bucket}/input/aws-smoke.yaml /opt/stella-tb21/aws-smoke.yaml --only-show-errors
aws s3 cp s3://{bucket}/input/aws_prepare.py /opt/stella-tb21/aws_prepare.py --only-show-errors
aws s3 cp s3://{bucket}/input/remote-config.env /opt/stella-tb21/remote-config.env --only-show-errors
printf '%s  %s\n' '{state["remote_runner_sha256"]}' /opt/stella-tb21/aws_runner.sh | sha256sum -c -
printf '%s  %s\n' '{state["merge_helper_sha256"]}' /opt/stella-tb21/aws_merge.py | sha256sum -c -
printf '%s  %s\n' '{state["smoke_taskset_sha256"]}' /opt/stella-tb21/aws-smoke.yaml | sha256sum -c -
chmod 700 /opt/stella-tb21/aws_runner.sh
printf '%s  %s\n' '{state["prepare_helper_sha256"]}' /opt/stella-tb21/aws_prepare.py | sha256sum -c -
shutdown -h +{timeout_minutes} >/dev/null 2>&1
cat >/etc/systemd/system/stella-tb21.service <<'UNIT'
[Unit]
Description=Stella Terminal-Bench 2.1 full evaluation
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
Environment=HOME=/root
ExecStart=/opt/stella-tb21/aws_runner.sh /opt/stella-tb21/remote-config.env
TimeoutStartSec=infinity
StandardOutput=append:/opt/stella-tb21/controller.log
StandardError=append:/opt/stella-tb21/controller.log

[Install]
WantedBy=multi-user.target
UNIT
systemctl daemon-reload
systemctl enable stella-tb21.service
systemctl start --no-block stella-tb21.service
"""
    encoded = base64.b64encode(bootstrap.encode()).decode()
    parameters = json.dumps(
        {"commands": [f"printf %s {shlex.quote(encoded)} | base64 -d > /tmp/stella-bootstrap.sh", "bash /tmp/stella-bootstrap.sh"]}
    )
    command = aws.run(
        "ssm",
        "send-command",
        "--document-name",
        "AWS-RunShellScript",
        "--instance-ids",
        state["instance_id"],
        "--parameters",
        parameters,
        "--timeout-seconds",
        "900",
        "--comment",
        f"Run {state['run_id']}",
        "--query",
        "Command.CommandId",
        "--output",
        "text",
    )
    update_state(state_path, state, command_id=command)
    journal.record("remote-command-started", command_id=command)
    return command


def invocation(aws: Aws, command: str, instance: str) -> dict[str, Any] | None:
    try:
        return aws.run(
            "ssm",
            "get-command-invocation",
            "--command-id",
            command,
            "--instance-id",
            instance,
            "--output",
            "json",
            json_output=True,
        )
    except RuntimeError as exc:
        if "InvocationDoesNotExist" in str(exc):
            return None
        raise


def monitor(aws: Aws, state: dict[str, Any], journal: RunJournal) -> None:
    started = time.monotonic()
    deadline = started + state["timeout_hours"] * 3600
    last_phase = ""
    saw_remote_status = False
    while time.monotonic() < deadline:
        status = None
        result = subprocess.run(
            ["aws", "s3", "cp", f"s3://{state['bucket']}/status.json", "-", "--only-show-errors"],
            text=True,
            capture_output=True,
            env=os.environ | {"AWS_DEFAULT_REGION": state["region"]},
            check=False,
        )
        if result.returncode == 0:
            saw_remote_status = True
            status = json.loads(result.stdout)
            phase = status.get("phase", "")
            if phase and phase != last_phase:
                journal.record("remote-progress", phase=phase, detail=status.get("detail"))
                last_phase = phase
            if phase == "complete":
                return
            if phase == "failed":
                raise RuntimeError(f"remote runner failed: {status.get('detail', 'no detail')}")

        if not saw_remote_status and time.monotonic() - started > 1200:
            raise RuntimeError("remote systemd worker produced no status within 20 minutes")

        current = invocation(aws, state["command_id"], state["instance_id"])
        if current and current.get("Status") in {"Failed", "Cancelled", "TimedOut"}:
            stderr = (current.get("StandardErrorContent") or "").strip()[-4000:]
            stdout = (current.get("StandardOutputContent") or "").strip()[-4000:]
            raise RuntimeError(f"SSM command {current['Status']}: {stderr or stdout}")
        time.sleep(60)
    raise RuntimeError(f"evaluation exceeded {state['timeout_hours']} hours")


def download_remote_journal(aws: Aws, state: dict[str, Any], run_dir: Path) -> None:
    if not state.get("bucket_created"):
        return
    destination = run_dir / "remote-journal.ndjson"
    try:
        aws.run(
            "s3",
            "cp",
            f"s3://{state['bucket']}/journal/remote.ndjson",
            str(destination),
            "--only-show-errors",
        )
    except RuntimeError:
        return


def download_artifacts(aws: Aws, state: dict[str, Any], run_dir: Path, journal: RunJournal) -> None:
    artifacts = run_dir / "artifacts"
    artifacts.mkdir(exist_ok=True)
    aws.run("s3", "sync", f"s3://{state['bucket']}/artifacts", str(artifacts), "--only-show-errors")
    required = {
        "results-redacted.tgz",
        "report.txt",
        "report.html",
        "run-metadata.json",
        "selection.json",
        "archive-summary.txt",
        "remote-journal.ndjson",
        "SHA256SUMS",
        "performance.json",
    }
    if state.get("run_mode") in {"capacity", "throughput"}:
        required = {"capacity-summary.json", "performance.json", "remote-journal.ndjson", "SHA256SUMS"}
    missing = sorted(name for name in required if not (artifacts / name).is_file())
    if missing:
        raise RuntimeError("downloaded artifacts are incomplete: " + ", ".join(missing))
    subprocess.run(["shasum", "-a", "256", "-c", "SHA256SUMS"], cwd=artifacts, check=True)
    journal.record("artifacts-verified", directory=str(artifacts))


def parse_args(argv: list[str] | None = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--plan", action="store_true", help="validate locally and print the cloud plan")
    modes = parser.add_mutually_exclusive_group()
    modes.add_argument(
        "--smoke",
        action="store_true",
        help="run five representative tasks at k=1 through the complete AWS path",
    )
    modes.add_argument(
        "--pilot", action="store_true",
        help="performance-only: prepare 89 environments, then five smoke tasks at concurrency 1,N,N,1",
    )
    modes.add_argument("--capacity", action="store_true", help="performance-only: 89 tasks at 16,32,48,64,16 workers; no top-ups")
    modes.add_argument("--throughput", action="store_true", help="timeboxed performance sample of the 89-task queue; no top-ups")
    parser.add_argument("--sample-minutes", type=int, default=10, help="throughput sample duration, excluding host preparation (1-30, default 10)")
    parser.add_argument("--warmup", choices=("legacy", "environment"), default="legacy")
    parser.add_argument("--warmup-concurrency", type=int, default=4, help="environment preparation workers (1-4)")
    parser.add_argument("--topup-concurrency", type=int, default=1, help="missing-task batch workers (up to --concurrency)")
    parser.add_argument("--cleanup", type=Path, metavar="RUN_DIR", help="delete resources recorded in RUN_DIR/state.json")
    parser.add_argument("--commit", default="origin/main", help="commit/ref to evaluate (default: origin/main)")
    parser.add_argument("--instance-type", default=DEFAULT_INSTANCE)
    parser.add_argument("--concurrency", type=int, default=DEFAULT_CONCURRENCY)
    parser.add_argument("--passes", type=int, default=DEFAULT_PASSES)
    parser.add_argument("--max-topup-rounds", type=int, default=3)
    parser.add_argument("--timeout-hours", type=int, default=DEFAULT_TIMEOUT_HOURS)
    return parser.parse_args(argv)


def main(argv: list[str] | None = None) -> int:
    signal.signal(signal.SIGTERM, interrupt_as_keyboard)
    args = parse_args(argv)
    root = repository_root()
    if args.cleanup:
        state_path = args.cleanup.resolve() / "state.json"
        state = json.loads(state_path.read_text())
        journal = RunJournal(args.cleanup.resolve())
        cleanup(Aws(state["region"], journal), state_path, state, journal)
        return 0
    if args.passes != 5:
        raise RuntimeError("reportable Terminal-Bench 2.1 comparison requires --passes 5")
    if args.concurrency < 1 or args.max_topup_rounds < 0 or args.timeout_hours < 1:
        raise RuntimeError("concurrency and timeout must be positive; top-up rounds cannot be negative")
    if not 1 <= args.warmup_concurrency <= 4:
        raise RuntimeError("warm-up concurrency must be between 1 and 4")
    if not 1 <= args.topup_concurrency <= args.concurrency:
        raise RuntimeError("top-up concurrency must be between 1 and --concurrency")
    if args.pilot and (args.warmup != "environment" or not 2 <= args.concurrency <= 5):
        raise RuntimeError("--pilot requires --warmup environment and --concurrency between 2 and 5")
    if args.capacity and (args.warmup != "environment" or args.concurrency != 16 or args.max_topup_rounds != 0):
        raise RuntimeError("--capacity requires --warmup environment --concurrency 16 --max-topup-rounds 0")
    if args.throughput and (args.warmup != "environment" or args.max_topup_rounds != 0 or not 1 <= args.sample_minutes <= 30):
        raise RuntimeError("--throughput requires --warmup environment --max-topup-rounds 0 and --sample-minutes 1-30")

    region, provider = require_environment()
    now = dt.datetime.now(dt.timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    run_mode = "full"
    expected_tasks = 89
    pass_concurrencies = [args.concurrency] * args.passes
    if args.smoke:
        run_mode, expected_tasks, pass_concurrencies = "smoke", 5, [args.concurrency]
    elif args.pilot:
        run_mode, expected_tasks = "pilot", 5
        pass_concurrencies = [1, args.concurrency, args.concurrency, 1]
    elif args.capacity:
        run_mode, pass_concurrencies = "capacity", [16, 32, 48, 64, 16]
    elif args.throughput:
        run_mode, pass_concurrencies = "throughput", [args.concurrency]
    run_id = f"tb21-experimental-{run_mode}-{now}"
    run_dir = root / "dist" / "evals" / "aws" / run_id
    journal = RunJournal(run_dir)
    commit = local_preflight(root, args.commit, args.concurrency, args.smoke or args.pilot, journal)
    state_path = run_dir / "state.json"
    controller_commit = subprocess.run(
        ["git", "rev-parse", "HEAD"], cwd=root, text=True, capture_output=True, check=True
    ).stdout.strip()
    state: dict[str, Any] = {
        "run_id": run_id,
        "region": region,
        "commit": commit,
        "commit_ref": args.commit,
        "controller_commit": controller_commit,
        "remote_runner_sha256": sha256(root / "test/evals/harbor/aws_runner.sh"),
        "merge_helper_sha256": sha256(root / "test/evals/harbor/stella_harbor/aws_merge.py"),
        "smoke_taskset_sha256": sha256(root / "test/evals/harbor/tasksets/aws-smoke.yaml"),
        "prepare_helper_sha256": sha256(root / "test/evals/harbor/stella_harbor/aws_prepare.py"),
        "model_id": provider["OPENAI_MODEL"],
        "instance_type": args.instance_type,
        "concurrency": args.concurrency,
        "passes": len(pass_concurrencies),
        "expected_tasks": expected_tasks,
        "run_mode": run_mode,
        "pass_concurrencies": pass_concurrencies,
        "sample_minutes": args.sample_minutes if args.throughput else 0,
        "warmup_mode": args.warmup,
        "warmup_concurrency": args.warmup_concurrency,
        "topup_concurrency": args.topup_concurrency,
        "max_topup_rounds": args.max_topup_rounds,
        "timeout_hours": args.timeout_hours,
        "dataset": DATASET,
        "created_at": dt.datetime.now(dt.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
    }
    atomic_json(state_path, state)
    journal.record(
        "plan-ready",
        run_id=run_id,
        commit=commit,
        model=provider["OPENAI_MODEL"],
        instance_type=args.instance_type,
        trials=state["expected_tasks"] * state["passes"],
        run_mode=run_mode,
    )
    if args.plan:
        print(json.dumps(state, indent=2, sort_keys=True))
        return 0

    aws = Aws(region, journal)
    run_error: Exception | None = None
    cleanup_error: Exception | None = None
    try:
        try:
            provision(aws, root, run_dir, state_path, state, provider, args.commit, journal)
            wait_for_ssm(aws, state["instance_id"], 900, journal)
            start_remote(aws, state, state_path, journal)
            monitor(aws, state, journal)
            download_artifacts(aws, state, run_dir, journal)
        except (Exception, KeyboardInterrupt) as exc:  # noqa: BLE001  # cleanup is mandatory
            run_error = exc
            download_remote_journal(aws, state, run_dir)
            if state.get("run_mode") in {"capacity", "throughput"} and state.get("bucket_created"):
                try:
                    download_artifacts(aws, state, run_dir, journal)
                except RuntimeError:
                    journal.record("capacity-checkpoint-unavailable")
            journal.record("run-failed", message=str(exc))
    finally:
        try:
            cleanup(aws, state_path, state, journal)
        except Exception as exc:  # noqa: BLE001  # preserve the original run error
            cleanup_error = exc
    if run_error and cleanup_error:
        raise RuntimeError(f"run failed: {run_error}; cleanup also failed: {cleanup_error}")
    if run_error:
        raise run_error
    if cleanup_error:
        raise cleanup_error
    journal.record("run-complete", artifacts=str(run_dir / "artifacts"))
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except RuntimeError as exc:
        print(f"eval:tb21:aws: {exc}", file=sys.stderr)
        raise SystemExit(1)
