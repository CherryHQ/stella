CREATE TABLE auth_user_roles (
    user_id INTEGER NOT NULL REFERENCES auth_users(id) ON DELETE CASCADE,
    role_id TEXT NOT NULL REFERENCES auth_roles(id) ON DELETE CASCADE,
    PRIMARY KEY(user_id, role_id)
);
