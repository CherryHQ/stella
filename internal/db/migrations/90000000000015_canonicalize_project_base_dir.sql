-- +goose Up
SET LOCAL lock_timeout = '10s';

-- Keep every historical value intact until the Home-aware startup migration can
-- resolve it against the durable owner root. PostgreSQL cannot safely decide
-- whether an arbitrary physical prefix or symlink aliases the configured Home.
ALTER TABLE project
  ADD CONSTRAINT project_base_dir_canonical_check CHECK (
    base_dir = '.' OR (
      base_dir <> '' AND
      left(base_dir, 1) <> '/' AND
      left(base_dir, 1) <> '$' AND
      right(base_dir, 1) <> '/' AND
      strpos(base_dir, '//') = 0 AND
      strpos(base_dir, E'\\') = 0 AND
      strpos(base_dir, ':') = 0 AND
      base_dir !~ E'(^|/)\\.{1,2}(/|$)' AND
      base_dir !~ '[[:cntrl:]]'
    )
  ) NOT VALID;

-- +goose Down
SET LOCAL lock_timeout = '10s';

-- The physical prefix is intentionally not reconstructed: it was deployment-
-- specific data and canonical project coordinates are the durable format.
ALTER TABLE project DROP CONSTRAINT project_base_dir_canonical_check;
