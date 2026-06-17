-- Disable the enforcement of foreign-keys constraints
PRAGMA foreign_keys = off;
-- Drop "auth_oidc_code" table
DROP TABLE `auth_oidc_code`;
-- Drop "auth_oidc_access_token" table
DROP TABLE `auth_oidc_access_token`;
-- Enable back the enforcement of foreign-keys constraints
PRAGMA foreign_keys = on;
