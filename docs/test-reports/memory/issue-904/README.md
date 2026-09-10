# Memory #904 PersonaMem Test Report

This directory stores the committed, human-readable baseline report for the
Full Stella PersonaMem v1 representative evaluation introduced by #904.

## Data

Download the official PersonaMem v1 files from the
[PersonaMem repository](https://github.com/bowen-upenn/PersonaMem) or its
[Hugging Face dataset](https://huggingface.co/datasets/bowen-upenn/PersonaMem-v1):

- `questions_32k.csv`
- `questions_128k.csv`
- `shared_contexts_32k.jsonl`
- `shared_contexts_128k.jsonl`

Place them under `dist/benchmarks/personamem/data/`, or set
`PERSONAMEM_DATA_ROOT` to another directory containing those files. The
benchmark validates the dataset hashes recorded by its checkpoint.

## Run

The evaluator is excluded from normal builds by the `personamemeval` build tag.
Its deterministic contract tests do not call a real model:

```bash
PERSONAMEM_DATA_ROOT=/absolute/path/to/personamem-v1 \
mise exec -- go test -tags=personamemeval ./cmd/stellad \
  -run '^TestPersonaMem' -count=1
```

A real representative run additionally requires an OpenAI-compatible endpoint
that serves `deepseek/deepseek-v4-flash`:

```bash
STELLA_HOME="$(pwd)/dist/benchmarks/personamem/h/r-v2" \
PERSONAMEM_DATA_ROOT=/absolute/path/to/personamem-v1 \
PERSONAMEM_PROVIDER_API_KEY=... \
PERSONAMEM_PROVIDER_BASE_URL=... \
PERSONAMEM_MODEL_SNAPSHOT_STATUS=operator-confirmed-latest \
PERSONAMEM_MODE=representative \
mise exec -- go test -tags=personamemeval ./cmd/stellad \
  -run '^TestPersonaMemBenchmark$' -count=1 -v -timeout 0
```

Use an empty Stella home and run directory for an independent comparison. A
matching interrupted run resumes from its validated checkpoint.

`PERSONAMEM_MODEL_SNAPSHOT_STATUS` is optional. Without it, the manifest records
the mutable model alias as `unverified-alias`; setting it is an operator claim,
not an independently verified immutable model revision.

## Artifact Boundary

Of the generated run artifacts, only the human-readable report is committed.
Downloaded data, benchmark homes, checkpoints, endpoint snapshots, per-question
answers, score JSON, and logs stay under ignored
`dist/benchmarks/personamem/` paths. No credentials belong in the report or
repository.
