# Terminal-Bench 2.1: Stella Code Mode, 2026-08-31

This directory preserves the complete current-harness Luna rerun for the Code
Mode capability treatment. It is a benchmark record and a reproducibility
artifact, not evidence that a particular product change caused a score change.

## Configuration

| Setting                  | Value                                                                                                        |
| ------------------------ | ------------------------------------------------------------------------------------------------------------ |
| Dataset                  | `terminal-bench/terminal-bench-2-1` (89 tasks)                                                               |
| Dataset hash             | `sha256:7d7bdc1cbedad549fc1140404bd4dc45e5fd0ea7c4186773687d177ad3a0699a`                                    |
| Model                    | `gateway/gpt-5.6-luna`                                                                                       |
| Attempts                 | `k=5`, five independent `k=1` passes merged in order                                                         |
| Concurrency              | `16`                                                                                                         |
| Agent timeout multiplier | `1.0`                                                                                                        |
| Host                     | AWS `c7i.8xlarge`                                                                                            |
| Candidate commit         | `6ea8142c7d957670261112e60a1cfd1d8a55e1bd`                                                                   |
| Tool treatment           | All registered Stella Code Mode capabilities; `view_image,vllm` excluded; bridge-attributable bash execution |

## Result

The runner selected exactly five scoreable trials for every task. One additional
attempt was adapter-invalid and excluded before selection, so it does not enter
the score denominator.

| Population                 | Stella resolved | Resolution rate | pass^5 | Trials        |
| -------------------------- | --------------: | --------------- | -----: | ------------- |
| Full Code Mode run, 89 × 5 |       235 / 445 | **52.8% ±4.6**  |  37.1% | 445 scoreable |

The Wilson 95% interval is 48.2–57.4%. Thirty-nine selected trials timed out.
Provider-reported cost was $6.7952 across 406 priced trials; 39 trials had no
reported cost and are not treated as $0.

## Baselines and target

The performance target is the complete 2026-08-21 Pi Luna baseline: 259 / 445
(58.2%). This Stella run is **24 resolved trials and 5.4 percentage points
behind Pi**. Its `pass^5` is 37.1%, 1.1 points above Pi's 36.0%, but that
stricter per-task stability measure does not erase the resolution gap.

Dataset, model, `k`, concurrency, deadline multiplier, and AWS instance class
match the Pi baseline. Pi is a different agent implementation by design, so it
is the target baseline, not a causal before/after reference for a Stella code
change. See [`../2026-08-21-pi-k5/`](../2026-08-21-pi-k5/).

For Stella's historical context, the 2026-08-20 bash-only baseline resolved
211 / 445 (47.4%), a raw +5.4-point difference from this run. That is also
not an improvement claim: the candidate commit and capability treatment differ.
A matched same-harness Stella reference run is required to attribute either
movement to this change. See the [historical Stella baseline](../2026-08-20-luna-vs-pi/).

## Evidence bundles

- `results-redacted.tgz`: result/configuration/transcript payloads processed by
  the archive redactor. It includes 445 transcripts and drops 104 unclassified
  secret-shaped values rather than retaining them.
- `report.txt`: rendered per-trial report and aggregate outcome/failure tables.
- `run-metadata.json`: candidate, host, model, and tool-treatment identity.
- `selection.json`: deterministic first-five scoreable selection and invalid
  attempt accounting.
- `archive-summary.txt`: redaction accounting.

`SHA256SUMS` records every stored artifact digest.
