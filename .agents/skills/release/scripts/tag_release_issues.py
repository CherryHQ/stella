#!/usr/bin/env python3
"""Tag all issues in a milestone with a release label.

After tagging, run the project-tracker sync (Pass B) to pull the new label
back into the Feishu Bitable.
"""

import argparse
import json
import subprocess
import sys


def run(cmd, check=True):
    """Run a shell command and return stdout."""
    result = subprocess.run(cmd, capture_output=True, text=True, shell=True)
    if check and result.returncode != 0:
        print(f"Error: {result.stderr}", file=sys.stderr)
        sys.exit(1)
    return result.stdout.strip()


def main():
    parser = argparse.ArgumentParser(
        description="Tag milestone issues with a release label."
    )
    parser.add_argument("--repo", required=True, help="owner/repo")
    parser.add_argument("--tag", required=True, help="release tag, e.g. v1.2.0")
    parser.add_argument("--milestone", help="milestone name or number (defaults to tag)")
    parser.add_argument("--label-prefix", default="release", help="label prefix")
    parser.add_argument("--dry-run", action="store_true", help="print what would be done")
    args = parser.parse_args()

    milestone = args.milestone or args.tag
    release_label = f"{args.label_prefix}:{args.tag}"
    repo = args.repo

    # 1. Find all issues in the milestone
    print(f"Fetching issues in milestone '{milestone}' for {repo}...")
    issues_json = run(
        f"gh issue list --repo {repo} --milestone '{milestone}' --state all --json number,title,state"
    )
    if not issues_json:
        print(f"No issues found in milestone '{milestone}'.")
        sys.exit(0)

    issues = json.loads(issues_json)
    print(f"Found {len(issues)} issue(s).\n")

    # 2. Check for open issues
    open_issues = [i for i in issues if i["state"] == "OPEN"]
    if open_issues:
        print(f"⚠️  {len(open_issues)} issue(s) still open:")
        for i in open_issues:
            print(f"   #{i['number']}: {i['title']}")
        print()

    # 3. Add release label to each issue
    for issue in issues:
        number = issue["number"]
        if args.dry_run:
            print(f"[dry-run] Would add label '{release_label}' to issue #{number}")
            continue

        # Check existing labels first (optional, avoids redundant edits)
        detail_json = run(
            f"gh issue view {number} --repo {repo} --json labels", check=False
        )
        if detail_json:
            labels = json.loads(detail_json).get("labels", [])
            label_names = [l["name"] for l in labels]
            if release_label in label_names:
                print(f"Skipping #{number}: already has '{release_label}'")
                continue

        run(f"gh issue edit {number} --repo {repo} --add-label '{release_label}'")
        print(f"Tagged #{number} with '{release_label}'")

    print(f"\nDone. Run project-tracker sync to update Feishu:")
    print(f"  python3 scripts/feishu_github_sync.py --repo {repo} --pass b")


if __name__ == "__main__":
    main()
