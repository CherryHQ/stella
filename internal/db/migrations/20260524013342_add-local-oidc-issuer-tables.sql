-- Create "auth_oidc_codes" table
CREATE TABLE `auth_oidc_codes` (
  `id` text NOT NULL,
  `code_hash` text NOT NULL,
  `user_id` text NOT NULL,
  `org_id` text NOT NULL DEFAULT '',
  `client_id` text NOT NULL,
  `redirect_uri` text NOT NULL,
  `scopes` text NOT NULL DEFAULT '[]',
  `nonce` text NOT NULL DEFAULT '',
  `pkce_challenge` text NOT NULL DEFAULT '',
  `pkce_method` text NOT NULL DEFAULT 'S256',
  `expires_at` text NOT NULL,
  `consumed_at` text NULL,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`user_id`) REFERENCES `auth_user` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "auth_oidc_codes_code_hash" to table: "auth_oidc_codes"
CREATE UNIQUE INDEX `auth_oidc_codes_code_hash` ON `auth_oidc_codes` (`code_hash`);
-- Create index "idx_auth_oidc_codes_code_hash" to table: "auth_oidc_codes"
CREATE INDEX `idx_auth_oidc_codes_code_hash` ON `auth_oidc_codes` (`code_hash`);
-- Create index "idx_auth_oidc_codes_user_id" to table: "auth_oidc_codes"
CREATE INDEX `idx_auth_oidc_codes_user_id` ON `auth_oidc_codes` (`user_id`);
-- Create "auth_oidc_access_tokens" table
CREATE TABLE `auth_oidc_access_tokens` (
  `id` text NOT NULL,
  `token_hash` text NOT NULL,
  `user_id` text NOT NULL,
  `org_id` text NOT NULL DEFAULT '',
  `client_id` text NOT NULL,
  `scopes` text NOT NULL DEFAULT '[]',
  `expires_at` text NOT NULL,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`user_id`) REFERENCES `auth_user` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "auth_oidc_access_tokens_token_hash" to table: "auth_oidc_access_tokens"
CREATE UNIQUE INDEX `auth_oidc_access_tokens_token_hash` ON `auth_oidc_access_tokens` (`token_hash`);
-- Create index "idx_auth_oidc_access_tokens_token_hash" to table: "auth_oidc_access_tokens"
CREATE INDEX `idx_auth_oidc_access_tokens_token_hash` ON `auth_oidc_access_tokens` (`token_hash`);
-- Create index "idx_auth_oidc_access_tokens_user_id" to table: "auth_oidc_access_tokens"
CREATE INDEX `idx_auth_oidc_access_tokens_user_id` ON `auth_oidc_access_tokens` (`user_id`);
