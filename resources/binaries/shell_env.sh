# Stella-managed shell environment. Sourced by non-interactive Bash through
# BASH_ENV and by Docker login shells through /etc/profile.d.
stella_shell_prepend_path() {
  case ":${PATH:-}:" in
    *":$1:"*) ;;
    *) PATH="$1${PATH:+:$PATH}" ;;
  esac
}

stella_shell_prepend_path_list() {
  case "${1:-}" in
    "") ;;
    *:*)
      stella_shell_prepend_path_list "${1#*:}"
      stella_shell_prepend_path "${1%%:*}"
      ;;
    *) stella_shell_prepend_path "$1" ;;
  esac
}

stella_shell_home=${STELLA_HOME:-/opt/stella}

# Prepend in reverse so the final order matches the runner's PATH. Docker's
# ordered managed list includes both principal mise shims and its optional
# per-user tool cache. Other backends fall back to MISE_DATA_DIR for the
# principal shim directory.
stella_shell_prepend_path "$stella_shell_home/.mise-tools/shims"
stella_shell_prepend_path "$stella_shell_home/bin"
stella_shell_prepend_path_list "${STELLA_MANAGED_PATH:-}"
if [ -n "${MISE_DATA_DIR:-}" ] && [ "$MISE_DATA_DIR" != "$stella_shell_home/.mise-tools" ]; then
  stella_shell_prepend_path "$MISE_DATA_DIR/shims"
fi

export PATH
unset -f stella_shell_prepend_path stella_shell_prepend_path_list 2>/dev/null || true
unset stella_shell_home
