-- +goose Up
-- Each row is one provider request, not one user turn. A turn can include
-- compaction, retries, and tool-loop follow-ups, each with distinct usage.
CREATE TABLE agent_llm_call (
    id                     UUID PRIMARY KEY DEFAULT uuidv7(),
    session_id             TEXT NOT NULL REFERENCES ctx_conversation(session_id) ON DELETE CASCADE,
    agent_id               TEXT NOT NULL REFERENCES agent(id) ON DELETE CASCADE,
    provider               TEXT NOT NULL,
    model                  TEXT NOT NULL,
    usage_reported         BOOLEAN NOT NULL DEFAULT false,
    input_tokens           BIGINT,
    output_tokens          BIGINT,
    cache_read_tokens      BIGINT,
    cache_write_tokens     BIGINT,
    cost_usd               NUMERIC(20, 12),
    duration_ms            BIGINT NOT NULL CHECK (duration_ms >= 0),
    time_to_first_token_ms BIGINT,
    stop_reason            TEXT NOT NULL DEFAULT '',
    error                  TEXT NOT NULL DEFAULT '',
    occurred_at            TIMESTAMPTZ NOT NULL,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (
        (usage_reported AND input_tokens IS NOT NULL AND output_tokens IS NOT NULL
         AND cache_read_tokens IS NOT NULL AND cache_write_tokens IS NOT NULL)
        OR
        (NOT usage_reported AND input_tokens IS NULL AND output_tokens IS NULL
         AND cache_read_tokens IS NULL AND cache_write_tokens IS NULL AND cost_usd IS NULL)
    ),
    CHECK (input_tokens IS NULL OR input_tokens >= 0),
    CHECK (output_tokens IS NULL OR output_tokens >= 0),
    CHECK (cache_read_tokens IS NULL OR cache_read_tokens >= 0),
    CHECK (cache_write_tokens IS NULL OR cache_write_tokens >= 0),
    CHECK (cost_usd IS NULL OR cost_usd >= 0),
    CHECK (time_to_first_token_ms IS NULL OR time_to_first_token_ms >= 0)
);

CREATE INDEX idx_agent_llm_call_session_id ON agent_llm_call (session_id);

-- +goose Down
DROP TABLE agent_llm_call;
