-- +goose Up
-- Expected affected rows are only user-role rows in legacy internal Session
-- histories: one history per delegate, scheduler, or task run, normally orders
-- of magnitude fewer rows than human chat. Keep each UPDATE scoped to one kind
-- so unrelated ctx_message rows are never rewritten.
UPDATE ctx_message AS message
SET actor_type = 'agent'
FROM ctx_conversation AS conversation
WHERE message.conversation_id = conversation.id
  AND message.role = 'user'
  AND message.actor_id IS NULL
  AND conversation.kind = 'delegate';

UPDATE ctx_message AS message
SET actor_type = 'system'
FROM ctx_conversation AS conversation
WHERE message.conversation_id = conversation.id
  AND message.role = 'user'
  AND message.actor_id IS NULL
  AND conversation.kind = 'scheduler';

UPDATE ctx_message AS message
SET actor_type = 'system'
FROM ctx_conversation AS conversation
WHERE message.conversation_id = conversation.id
  AND message.role = 'user'
  AND message.actor_id IS NULL
  AND conversation.kind = 'task';

-- +goose Down
UPDATE ctx_message AS message
SET actor_type = 'human'
FROM ctx_conversation AS conversation
WHERE message.conversation_id = conversation.id
  AND message.role = 'user'
  AND message.actor_id IS NULL
  AND message.actor_type = 'agent'
  AND conversation.kind = 'delegate';

UPDATE ctx_message AS message
SET actor_type = 'human'
FROM ctx_conversation AS conversation
WHERE message.conversation_id = conversation.id
  AND message.role = 'user'
  AND message.actor_id IS NULL
  AND message.actor_type = 'system'
  AND conversation.kind = 'scheduler';

UPDATE ctx_message AS message
SET actor_type = 'human'
FROM ctx_conversation AS conversation
WHERE message.conversation_id = conversation.id
  AND message.role = 'user'
  AND message.actor_id IS NULL
  AND message.actor_type = 'system'
  AND conversation.kind = 'task';
