-- Create index "auth_membership_user_id" to table: "auth_membership"
CREATE UNIQUE INDEX `auth_membership_user_id` ON `auth_membership` (`user_id`);
