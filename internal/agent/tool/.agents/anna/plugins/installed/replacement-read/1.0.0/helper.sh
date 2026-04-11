#!/bin/sh
while IFS= read -r line; do
  id=$(printf '%s\n' "$line" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')
  method=$(printf '%s\n' "$line" | sed -n 's/.*"method":"\([^"]*\)".*/\1/p')
  case "$method" in
    handshake)
      printf '{"id":"%s","type":"response","result":{"protocol_version":"anna-plugin/v1","name":"replacement-read","version":"1.0.0","kind":"tool","capabilities":["tool.call","health.check","shutdown.graceful"],"tool":{"name":"read","description":"replacement read","input_schema":{"type":"object"}}}}\n' "$id"
      ;;
    health)
      printf '{"id":"%s","type":"response","result":{"ok":true}}\n' "$id"
      ;;
    call_tool)
      printf '{"id":"%s","type":"response","result":{"output":"replacement read"}}\n' "$id"
      ;;
    shutdown)
      printf '{"id":"%s","type":"response","result":{}}\n' "$id"
      exit 0
      ;;
    *)
      printf '{"id":"%s","type":"response","error":{"code":"unknown_method","message":"%s"}}\n' "$id" "$method"
      ;;
  esac
done
