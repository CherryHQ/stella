import json

import pytest

from stella_harbor import otel


def test_fetch_uses_tempo_proxy_and_keeps_child_spans(tmp_path, monkeypatch):
    job = tmp_path / "job"
    result = job / "run" / "task__one" / "agent" / "stella" / "result.json"
    result.parent.mkdir(parents=True)
    result.write_text(json.dumps({"session_id": "session-1"}))
    urls = []

    class Response:
        def __init__(self, payload):
            self.payload = payload

        def __enter__(self):
            return self

        def __exit__(self, *_):
            return False

        def read(self):
            return json.dumps(self.payload).encode()

    def fake_urlopen(url, timeout):
        urls.append(url)
        if "/api/search?" in url:
            return Response({"traces": [{"traceID": "trace-1"}]})
        return Response({"batches": [{"scopeSpans": [{"spans": [
            {"name": "agent.loop", "startTimeUnixNano": "0", "endTimeUnixNano": "10000000",
             "attributes": [{"key": "gen_ai.conversation.id", "value": {"stringValue": "session-1"}}]},
            {"name": "child", "startTimeUnixNano": "0", "endTimeUnixNano": "2000000", "attributes": []},
        ]}]}]})

    monkeypatch.setattr(otel, "urlopen", fake_urlopen)
    sessions, spans = otel.fetch(job, "http://127.0.0.1:3000")

    assert sessions == {"session-1": "task__one"}
    assert {span["name"] for span in spans} == {"agent.loop", "child"}
    assert any("/api/datasources/proxy/uid/tempo/api/search?" in url for url in urls)
    assert any("span.gen_ai.conversation.id" in url for url in urls)
    rendered = otel.render(job, sessions, spans)
    assert "2 spans" in rendered
    assert "trial span assertion: PASS session=session-1" in rendered


def test_render_fails_when_every_trial_has_no_matching_span():
    with pytest.raises(RuntimeError, match="zero spans for every trial session"):
        otel.render(None, {"missing": "task__missing"}, [{"name": "agent.loop", "duration_ms": 1, "attributes": {}}])


def test_render_accepts_one_retained_trial_trace_and_reports_missing_sessions(tmp_path):
    output = otel.render(tmp_path, {"found": "task__found", "missing": "task__missing"}, [
        {"name": "agent.loop", "duration_ms": 1, "attributes": {}, "trial_session_id": "found"},
    ])
    assert "1/2 trial session(s)" in output
    assert "trial sessions with no retained Tempo trace: 1" in output


def test_summary_groups_dynamic_turn_and_database_span_names():
    stats, _, _ = otel.summarize({"s": "trial"}, [
        {"name": "turn 1", "duration_ms": 10, "attributes": {}, "trial_session_id": "s"},
        {"name": "turn 2", "duration_ms": 20, "attributes": {}, "trial_session_id": "s"},
        {"name": "GetAgent (SELECT agent)", "duration_ms": 2,
         "attributes": {"db.system": "postgresql"}, "trial_session_id": "s"},
    ])
    assert [(row["name"], row["count"]) for row in stats] == [("turn", 2), ("db.query", 1)]


def test_summary_reports_retry_attributes_without_guessing_turns():
    stats, retries, _ = otel.summarize({"s": "trial"}, [
        {"name": "gen_ai.chat", "duration_ms": 10, "attributes": {"gen_ai.conversation.id": "s"}},
        {"name": "gen_ai.chat", "duration_ms": 20, "attributes": {"stella.retry_count": 2}},
    ])
    assert retries == 2
    assert stats == [{"name": "gen_ai.chat", "count": 2, "total_ms": 30, "mean_ms": 15, "p95_ms": 20, "max_ms": 20}]
