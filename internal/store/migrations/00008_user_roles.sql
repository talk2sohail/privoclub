-- +goose Up
-- ADR 0021: a person holds a set of roles. Valid sets are {owner}, {admin},
-- or a non-empty subset of {member, approver, auditor}; the store checks
-- them (NormalizeRoles), the schema only stores them.
CREATE TABLE user_roles (
    org_id     TEXT NOT NULL REFERENCES orgs(id),
    user_id    TEXT NOT NULL,
    role       TEXT NOT NULL,
    source     TEXT NOT NULL DEFAULT 'manual', -- manual; later idp:<group> (F20 SSO)
    granted_by TEXT NOT NULL DEFAULT '',       -- actor id of whoever granted it
    granted_at TEXT NOT NULL,
    PRIMARY KEY (org_id, user_id, role),
    FOREIGN KEY (org_id, user_id) REFERENCES users (org_id, id)
);
INSERT INTO user_roles (org_id, user_id, role, source, granted_by, granted_at)
    SELECT org_id, id, role, 'manual', 'migration', created_at FROM users;

CREATE TABLE invite_roles (
    org_id    TEXT NOT NULL REFERENCES orgs(id),
    invite_id TEXT NOT NULL,
    role      TEXT NOT NULL,
    PRIMARY KEY (org_id, invite_id, role),
    FOREIGN KEY (org_id, invite_id) REFERENCES invites (org_id, id) ON DELETE CASCADE
);
INSERT INTO invite_roles (org_id, invite_id, role) SELECT org_id, id, role FROM invites;

-- Roles belong to the person, not to the gateway token: a member's own
-- gateway token only uses the gateway, and role: rules read the person's
-- current roles. Automation tokens (no user) keep their role.
UPDATE api_tokens SET role = 'member' WHERE kind = 'gateway' AND user_id <> '';

ALTER TABLE users DROP COLUMN role;
ALTER TABLE invites DROP COLUMN role;

-- +goose Down
-- Lossy: each person keeps the strongest of their roles.
ALTER TABLE users ADD COLUMN role TEXT NOT NULL DEFAULT 'member';
UPDATE users SET role = COALESCE((
    SELECT r.role FROM user_roles r WHERE r.org_id = users.org_id AND r.user_id = users.id
    ORDER BY CASE r.role WHEN 'owner' THEN 0 WHEN 'admin' THEN 1 WHEN 'approver' THEN 2 WHEN 'auditor' THEN 3 ELSE 4 END
    LIMIT 1), 'member');
ALTER TABLE invites ADD COLUMN role TEXT NOT NULL DEFAULT 'member';
UPDATE invites SET role = COALESCE((
    SELECT r.role FROM invite_roles r WHERE r.org_id = invites.org_id AND r.invite_id = invites.id
    ORDER BY CASE r.role WHEN 'admin' THEN 1 WHEN 'approver' THEN 2 WHEN 'auditor' THEN 3 ELSE 4 END
    LIMIT 1), 'member');
DROP TABLE invite_roles;
DROP TABLE user_roles;
