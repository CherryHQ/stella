-- +goose Up
SET LOCAL lock_timeout = '10s';

UPDATE agent
SET enabled_builtin_skills = '{"version": 1, "disabled": []}'::jsonb
WHERE jsonb_typeof(enabled_builtin_skills) = 'array'
   OR enabled_builtin_skills = 'null'::jsonb;

-- Keep this database constraint identical to the strict runtime decoder: exact
-- fields, integer version 1, canonical refs, bytewise sort order, and no
-- duplicates. Historical arrays were consumed above; malformed v1 objects are
-- not guessed at and make the migration fail closed during validation.
-- +goose StatementBegin
CREATE FUNCTION agent_skill_policy_is_canonical(policy JSONB) RETURNS BOOLEAN
LANGUAGE plpgsql IMMUTABLE STRICT PARALLEL SAFE AS $$
DECLARE
  entry JSONB;
  ref TEXT;
  previous_ref TEXT;
BEGIN
  IF jsonb_typeof(policy) <> 'object'
     OR NOT (policy ?& ARRAY['version', 'disabled'])
     OR policy - 'version' - 'disabled' <> '{}'::jsonb
     OR jsonb_typeof(policy -> 'version') <> 'number'
     OR policy ->> 'version' <> '1'
     OR jsonb_typeof(policy -> 'disabled') <> 'array' THEN
    RETURN false;
  END IF;

  FOR entry IN
    SELECT value
    FROM jsonb_array_elements(policy -> 'disabled') WITH ORDINALITY AS entries(value, ordinal)
    ORDER BY ordinal
  LOOP
    IF jsonb_typeof(entry) <> 'string' THEN
      RETURN false;
    END IF;
    ref := entry #>> '{}';
    IF ref !~ '^(builtin|system|system_agent):[a-z0-9]+(-[a-z0-9]+)*$'
       OR length(split_part(ref, ':', 2)) > 64
       OR (previous_ref IS NOT NULL AND previous_ref COLLATE "C" >= ref COLLATE "C") THEN
      RETURN false;
    END IF;
    previous_ref := ref;
  END LOOP;
  RETURN true;
END;
$$;
-- +goose StatementEnd

ALTER TABLE agent
  ALTER COLUMN enabled_builtin_skills SET DEFAULT '{"version": 1, "disabled": []}'::jsonb,
  ADD CONSTRAINT agent_skill_policy_canonical_check
  CHECK (agent_skill_policy_is_canonical(enabled_builtin_skills)) NOT VALID;

ALTER TABLE agent VALIDATE CONSTRAINT agent_skill_policy_canonical_check;

-- +goose Down
SET LOCAL lock_timeout = '10s';

ALTER TABLE agent
  DROP CONSTRAINT agent_skill_policy_canonical_check,
  ALTER COLUMN enabled_builtin_skills SET DEFAULT '[]'::jsonb;

DROP FUNCTION agent_skill_policy_is_canonical(JSONB);
