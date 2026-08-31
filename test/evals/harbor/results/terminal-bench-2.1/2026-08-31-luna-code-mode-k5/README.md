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

## Long-term context

The primary comparison is Stella's own release timeline. The 2026-08-20
bash-only Stella run resolved 211 / 445 (47.4%), a raw +5.4-point movement from
this run. That is descriptive context, not an improvement claim: candidate
commit and capability treatment differ. A matched same-harness Stella reference
run is required to attribute movement to a particular change. See the
[historical Stella baseline](../2026-08-20-luna-vs-pi/).

The complete 2026-08-21 Pi Luna run, 259 / 445 (58.2%), remains an optional
peer reference. Dataset, model, `k`, concurrency, deadline multiplier, and AWS
instance class match, but Pi is a different agent implementation and does not
define Stella's release KPI. See [`../2026-08-21-pi-k5/`](../2026-08-21-pi-k5/).

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
