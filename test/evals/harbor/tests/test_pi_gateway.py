import json

from harbor.models.agent.context import AgentContext

from stella_harbor.pi_gateway import PiGateway


def _agent(tmp_path):
    return PiGateway(logs_dir=tmp_path, model_name="gateway/gpt-5.6-luna")


def _event(*, input_tokens=11, output_tokens=7, cache_read=3, cost=0.25):
    return {
        "type": "message_end",
        "message": {
            "role": "assistant",
            "content": [{"type": "text", "text": "你好"}],
            "usage": {
                "input": input_tokens,
                "output": output_tokens,
                "cacheRead": cache_read,
                "cacheWrite": 2,
                "cost": {"total": cost},
            },
        },
    }


def test_pi_output_usage_parses_normal_utf8(tmp_path):
    (tmp_path / "pi.txt").write_text(json.dumps(_event(), ensure_ascii=False) + "\n")
    context = AgentContext()

    _agent(tmp_path).populate_context_post_run(context)

    assert context.n_input_tokens == 14
    assert context.n_output_tokens == 7
    assert context.n_cache_tokens == 3
    assert context.cost_usd == 0.25
    assert context.metadata is None


def test_pi_output_recovers_truncated_utf8_and_records_it(tmp_path):
    complete = json.dumps(_event(input_tokens=5), ensure_ascii=False).encode("utf-8")
    truncated = b'{"type":"message_end","message":{"role":"assistant","content":"' + "中".encode("utf-8")[:-1]
    (tmp_path / "pi.txt").write_bytes(complete + b"\n" + truncated)
    context = AgentContext()

    _agent(tmp_path).populate_context_post_run(context)

    assert context.n_input_tokens == 8
    assert context.n_output_tokens == 7
    assert context.metadata == {
        "pi_output_decode": {
            "utf8_decode_errors": 1,
            "truncated_utf8": True,
            "output_bytes": len(complete) + 1 + len(truncated),
        }
    }


def test_pi_output_usage_handles_empty_output(tmp_path):
    (tmp_path / "pi.txt").write_bytes(b"")
    context = AgentContext()

    _agent(tmp_path).populate_context_post_run(context)

    assert context.n_input_tokens == 0
    assert context.n_output_tokens == 0
    assert context.n_cache_tokens == 0
    assert context.cost_usd is None
    assert context.metadata is None
