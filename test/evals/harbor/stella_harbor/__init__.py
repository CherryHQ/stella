"""Stella <-> Harbor evaluation adapter.

See docs/design/2026-08-18-harbor-eval-adapter.md. Modules:

- bridge: per-trial Unix-socket server that forwards Stella's sandbox
  operations into the task container through Harbor's BaseEnvironment.
- agent: the Harbor BaseInstalledAgent wrapper.
"""
