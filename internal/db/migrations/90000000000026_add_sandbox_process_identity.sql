-- +goose Up
CREATE TABLE agent_session_sandbox_process (
    session_id TEXT NOT NULL,
    generation BIGINT NOT NULL,
    pid BIGINT NOT NULL CHECK (pid > 0),
    start_time BIGINT NOT NULL CHECK (start_time > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (session_id, generation, pid, start_time),
    FOREIGN KEY (session_id) REFERENCES agent_session_sandbox(session_id) ON DELETE CASCADE
);
CREATE INDEX idx_agent_session_sandbox_process_generation
    ON agent_session_sandbox_process(session_id, generation);

-- +goose Down
DROP TABLE agent_session_sandbox_process;
