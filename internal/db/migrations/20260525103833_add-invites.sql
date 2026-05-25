-- Create "auth_invite" table
CREATE TABLE `auth_invite` (
  `id` text NOT NULL,
  `token_hash` text NOT NULL,
  `org_id` text NOT NULL,
  `email` text NULL,
  `role` text NOT NULL DEFAULT 'user',
  `status` text NOT NULL DEFAULT 'pending',
  `max_uses` integer NOT NULL DEFAULT 1,
  `use_count` integer NOT NULL DEFAULT 0,
  `invited_by` text NOT NULL,
  `accepted_by` text NULL,
  `expires_at` text NOT NULL,
  `created_at` text NOT NULL DEFAULT (datetime('now')),
  `updated_at` text NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (`id`),
  CONSTRAINT `0` FOREIGN KEY (`accepted_by`) REFERENCES `auth_user` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION,
  CONSTRAINT `1` FOREIGN KEY (`invited_by`) REFERENCES `auth_user` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION,
  CONSTRAINT `2` FOREIGN KEY (`org_id`) REFERENCES `auth_organization` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CHECK (status IN ('pending','accepted','revoked'))
);
-- Create index "auth_invite_token_hash" to table: "auth_invite"
CREATE UNIQUE INDEX `auth_invite_token_hash` ON `auth_invite` (`token_hash`);
-- Create index "idx_auth_invite_org" to table: "auth_invite"
CREATE INDEX `idx_auth_invite_org` ON `auth_invite` (`org_id`);
