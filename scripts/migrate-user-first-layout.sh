#!/usr/bin/env bash
#
# migrate-user-first-layout.sh — migrate on-disk data from the legacy
# agent-first layout to the user-first layout introduced in #442 (PRs #455/#460).
#
# Legacy layout                                  ->  User-first layout
#   $H/workspaces/{agent}/users/{user}/{data,assets,.agents}  ->  $H/users/{user}/...            (SHARED across the user's agents — merged)
#   $H/workspaces/{agent}/users/{user}/<project trees>        ->  $H/users/{user}/agents/{agent}/...
#   $H/workspaces/{agent}/groups/{grp}/{data,assets,.agents}  ->  $H/users/group-{grp}/...       (SHARED across the group's agents — merged)
#   $H/workspaces/{agent}/groups/{grp}/<project trees>        ->  $H/users/group-{grp}/agents/{agent}/...
#   $H/workspaces/{agent}/system/*                            ->  $H/agents/{agent}/...          (user-less jobs run in the agent dir)
#   $H/workspaces/{agent}/.agents/skills                      ->  $H/agents/{agent}/.agents/skills
#   $H/groups/{grp}/.mise-tools                               ->  $H/users/group-{grp}/.mise-tools
#   (legacy $H/users/{user}/.mise-tools already matches the new layout — left untouched)
#
# It also rewrites project.base_dir (an absolute host path) in the SQLite DB:
#   $H/workspaces/{agent}/users/{user}/   ->   $H/users/{user}/agents/{agent}/
#
# CONFLICTS: when the same user/group has shared data under multiple agents,
# files are merged with NEWEST-WINS by mtime (rsync --update). Per-agent project
# trees never collide (keyed by agent).
#
# SAFE BY DEFAULT: copies (never moves), leaving the legacy tree in place so the
# migration is reversible. Re-running is idempotent (rsync --update + a DB rewrite
# that only matches the legacy prefix). Dry-run unless --apply is given.
#
# Usage:
#   scripts/migrate-user-first-layout.sh [--home DIR] [--db FILE] [--apply]
#
#   --home DIR   STELLA_HOME (default: $STELLA_HOME, else ~/.stella)
#   --db FILE    SQLite DB path (default: $HOME_DIR/stella.db)
#   --apply      perform the migration (default: dry-run, prints planned actions)
#
# PRECONDITION: stop stellad before running with --apply.
set -euo pipefail

HOME_DIR="${STELLA_HOME:-$HOME/.stella}"
DB_PATH=""
APPLY=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --home) HOME_DIR="$2"; shift 2 ;;
    --db)   DB_PATH="$2"; shift 2 ;;
    --apply) APPLY=1; shift ;;
    -h|--help) sed -n '2,40p' "$0"; exit 0 ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
done

HOME_DIR="${HOME_DIR%/}"
DB_PATH="${DB_PATH:-$HOME_DIR/stella.db}"
WS="$HOME_DIR/workspaces"

if [[ $APPLY -eq 1 ]]; then PREFIX="[apply]"; else PREFIX="[dry-run]"; fi
log() { echo "$PREFIX $*"; }

# copy_merge SRC DST — newest-wins merge (rsync --update). No-op if SRC missing.
copy_merge() {
  local src="$1" dst="$2"
  [[ -d "$src" ]] || return 0
  log "merge  $src/ -> $dst/"
  if [[ $APPLY -eq 1 ]]; then
    mkdir -p "$dst"
    rsync -a --update "$src/" "$dst/"
  fi
}

echo "STELLA_HOME : $HOME_DIR"
echo "DB          : $DB_PATH"
echo "mode        : $([[ $APPLY -eq 1 ]] && echo APPLY || echo DRY-RUN)"
echo

if [[ ! -d "$WS" ]]; then
  echo "No legacy $WS — nothing to migrate (already user-first?)."
else
  # ---- filesystem ----
  shopt -s nullglob
  for agent_path in "$WS"/*/; do
    agent="$(basename "$agent_path")"

    # agent-level skills (user-independent)
    copy_merge "$agent_path/.agents" "$HOME_DIR/agents/$agent/.agents"

    # user homes
    for user_path in "$agent_path"users/*/; do
      [[ -d "$user_path" ]] || continue
      user="$(basename "$user_path")"
      home="$HOME_DIR/users/$user"
      copy_merge "$user_path/data"    "$home/data"
      copy_merge "$user_path/assets"  "$home/assets"
      copy_merge "$user_path/.agents" "$home/.agents"
      # everything else under the legacy user root = project trees -> per-agent dir
      for entry in "$user_path"*/ "$user_path".[!.]*/; do
        [[ -d "$entry" ]] || continue
        name="$(basename "$entry")"
        case "$name" in data|assets|.agents) continue ;; esac
        copy_merge "$entry" "$home/agents/$agent/$name"
      done
    done

    # group homes (note the group- prefix in the new layout)
    for grp_path in "$agent_path"groups/*/; do
      [[ -d "$grp_path" ]] || continue
      grp="$(basename "$grp_path")"
      home="$HOME_DIR/users/group-$grp"
      copy_merge "$grp_path/data"    "$home/data"
      copy_merge "$grp_path/assets"  "$home/assets"
      copy_merge "$grp_path/.agents" "$home/.agents"
      for entry in "$grp_path"*/ "$grp_path".[!.]*/; do
        [[ -d "$entry" ]] || continue
        name="$(basename "$entry")"
        case "$name" in data|assets|.agents) continue ;; esac
        copy_merge "$entry" "$home/agents/$agent/$name"
      done
    done

    # system (user-less) workspace -> the agent's own dir
    copy_merge "$agent_path/system" "$HOME_DIR/agents/$agent"
  done

  # legacy group mise trees -> group- home (user mise trees already aligned)
  for grp_mise in "$HOME_DIR"/groups/*/.mise-tools; do
    [[ -d "$grp_mise" ]] || continue
    grp="$(basename "$(dirname "$grp_mise")")"
    copy_merge "$grp_mise" "$HOME_DIR/users/group-$grp/.mise-tools"
  done
  shopt -u nullglob
fi

# ---- database: rewrite project.base_dir ----
echo
if [[ -f "$DB_PATH" ]]; then
  # Per-row rewrite driven by the row's own agent_id/user_id columns:
  # replace the legacy prefix, preserving whatever subpath the project used.
  SQL="UPDATE project
       SET base_dir = '$HOME_DIR/users/' || user_id || '/agents/' || agent_id
                      || substr(base_dir, length('$HOME_DIR/workspaces/' || agent_id || '/users/' || user_id) + 1)
       WHERE base_dir LIKE '$HOME_DIR/workspaces/' || agent_id || '/users/' || user_id || '/%';"
  affected="$(sqlite3 "$DB_PATH" "SELECT count(*) FROM project WHERE base_dir LIKE '$HOME_DIR/workspaces/' || agent_id || '/users/' || user_id || '/%';" 2>/dev/null || echo 0)"
  log "DB: rewrite project.base_dir for $affected project row(s)"
  if [[ $APPLY -eq 1 && "${affected:-0}" -gt 0 ]]; then
    backup="$DB_PATH.bak-pre-userfirst"
    cp "$DB_PATH" "$backup"
    echo "      DB backed up to $backup"
    sqlite3 "$DB_PATH" "$SQL"
  fi
else
  echo "No DB at $DB_PATH — skipping base_dir rewrite."
fi

echo
if [[ $APPLY -eq 1 ]]; then
  echo "Done. Verify the new layout, then remove the legacy trees once satisfied:"
  echo "    rm -rf '$WS' '$HOME_DIR'/groups"
else
  echo "Dry-run complete. Re-run with --apply to perform the migration (stop stellad first)."
fi
