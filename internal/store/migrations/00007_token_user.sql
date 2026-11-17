-- +goose Up
-- F07 review R-01 (High): a gateway token found its owner's upstream grants,
-- teams and policy by the free-text person it carries, and an owner or admin
-- could issue a token naming any member. A token is now bound to a user by
-- id; only a person's own token (POST /api/v1/me/tokens, signed in) carries
-- one. Tokens without a user reach no one's account and are in "everyone"
-- only.
ALTER TABLE api_tokens ADD COLUMN user_id TEXT NOT NULL DEFAULT '';
CREATE INDEX api_tokens_org_user ON api_tokens (org_id, user_id);

-- Bind the existing tokens that a person made for themselves while signed in,
-- and nothing else. Such a token has a token.create event with the person's
-- own user as actor, the same role and name, written within seconds of the
-- token. Tokens made by an administrator for someone, or by the CLI, stay
-- unbound: their holder makes a new token of their own.
UPDATE api_tokens SET user_id = COALESCE((
    SELECT u.id FROM users u
    WHERE u.org_id = api_tokens.org_id AND u.person = api_tokens.person
      AND EXISTS (
        SELECT 1 FROM audit_events e
        WHERE e.org_id = api_tokens.org_id AND e.phase = 'admin'
          AND e.actor_id = 'user:' || u.id
          AND e.action = 'token.create:gateway:' || api_tokens.role || ':' || api_tokens.name
          AND abs(julianday(e.ts) - julianday(api_tokens.created_at)) * 86400 < 5)
), '')
WHERE kind = 'gateway' AND person <> '';

-- Whose upstream account a call used: "<person> (user <id>)" for a server
-- where each person uses their own account, empty otherwise.
ALTER TABLE audit_events ADD COLUMN account TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE audit_events DROP COLUMN account;
DROP INDEX api_tokens_org_user;
ALTER TABLE api_tokens DROP COLUMN user_id;
