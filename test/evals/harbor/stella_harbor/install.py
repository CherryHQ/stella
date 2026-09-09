"""Installation policy shared by native preflight and benchmark adapters."""
from __future__ import annotations


# Full Hermes dependencies can exceed six minutes on cold, concurrent hosts.
# Setup has its own budget; never multiply the task's execution timeout.
SETUP_TIMEOUT_SEC = 1200
HARBOR_SETUP_TIMEOUT_SEC = 360

