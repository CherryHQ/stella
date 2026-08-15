# Stella-managed shell environment. Sourced by non-interactive Bash through
# BASH_ENV and by Docker login shells through /etc/profile.d.
#
# Login profiles may replace or reorder PATH. Backends preserve their final,
# already-deduplicated executable search contract in STELLA_RUNNER_PATH. Bash
# restores it only for login shells; ordinary bash -c and shebang scripts retain
# deliberate PATH changes made by their parent process.
if [ -n "${STELLA_RUNNER_PATH:-}" ] && { [ -z "${BASH_VERSION:-}" ] || shopt -q login_shell; }; then
  PATH=$STELLA_RUNNER_PATH
  export PATH
fi
