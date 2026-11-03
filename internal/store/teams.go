package store

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"sort"
	"time"
)

// Teams decide which servers a person reaches (ADR 0020).

// TeamEveryone is the built-in team every active member is in. It has no row.
const TeamEveryone = "everyone"

var teamSlugRe = regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`)

// ValidTeamSlug reports whether s can name a team that admins create.
// "everyone" is reserved.
func ValidTeamSlug(s string) bool { return teamSlugRe.MatchString(s) && s != TeamEveryone }

// ErrUnknownTeam is returned when a team named in a request does not exist.
var ErrUnknownTeam = errors.New("store: unknown team")

// Team is a group of people and the servers they reach.
type Team struct {
	Slug        string
	Name        string
	Description string
	Profile     string
	CreatedAt   time.Time
	Members     []TeamMember // filled by ListTeams
	Servers     []string     // server aliases, filled by ListTeams
}

// TeamMember is one person in a team.
type TeamMember struct {
	UserID string
	Person string
	Name   string
}

// CreateTeam adds a team and writes ev in the same transaction.
func (s *Store) CreateTeam(ctx context.Context, org OrgID, t Team, ev AuditEvent) error {
	if !ValidTeamSlug(t.Slug) {
		return errors.New("store: invalid team slug")
	}
	ev.Phase = "admin"
	return s.tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO teams (org_id, slug, name, description, profile, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
			org, t.Slug, t.Name, t.Description, t.Profile, ts(t.CreatedAt))
		if err != nil {
			return mapErr(err)
		}
		return insertAudit(ctx, tx, org, []AuditEvent{ev})
	})
}

// UpdateTeam changes a team's name and description.
func (s *Store) UpdateTeam(ctx context.Context, org OrgID, slug, name, description string, ev AuditEvent) error {
	ev.Phase = "admin"
	return s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE teams SET name = ?, description = ? WHERE org_id = ? AND slug = ?`, name, description, org, slug)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return ErrNotFound
		}
		return insertAudit(ctx, tx, org, []AuditEvent{ev})
	})
}

// DeleteTeam removes a team, its members, the access it gave and the rules
// that name it.
func (s *Store) DeleteTeam(ctx context.Context, org OrgID, slug string, ev AuditEvent) error {
	ev.Phase = "admin"
	return s.tx(ctx, func(tx *sql.Tx) error {
		for _, q := range []string{
			`DELETE FROM team_members WHERE org_id = ? AND team = ?`,
			`DELETE FROM server_teams WHERE org_id = ? AND team = ?`,
		} {
			if _, err := tx.ExecContext(ctx, q, org, slug); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM policies WHERE org_id = ? AND subject = ?`, org, "team:"+slug); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM teams WHERE org_id = ? AND slug = ?`, org, slug)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return ErrNotFound
		}
		return insertAudit(ctx, tx, org, []AuditEvent{ev})
	})
}

// TeamBySlug returns one team without its members and servers.
func (s *Store) TeamBySlug(ctx context.Context, org OrgID, slug string) (Team, error) {
	var t Team
	var created string
	err := s.r.QueryRowContext(ctx, `SELECT slug, name, description, profile, created_at FROM teams WHERE org_id = ? AND slug = ?`, org, slug).
		Scan(&t.Slug, &t.Name, &t.Description, &t.Profile, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrNotFound
	}
	t.CreatedAt = parseTS(created)
	return t, err
}

// ListTeams lists an org's teams by name, each with its active members and
// the aliases of the servers it reaches.
func (s *Store) ListTeams(ctx context.Context, org OrgID) ([]Team, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT slug, name, description, profile, created_at FROM teams WHERE org_id = ? ORDER BY name, slug`, org)
	if err != nil {
		return nil, err
	}
	var out []Team
	idx := map[string]int{}
	for rows.Next() {
		var t Team
		var created string
		if err := rows.Scan(&t.Slug, &t.Name, &t.Description, &t.Profile, &created); err != nil {
			_ = rows.Close()
			return nil, err
		}
		t.CreatedAt = parseTS(created)
		idx[t.Slug] = len(out)
		out = append(out, t)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	mrows, err := s.r.QueryContext(ctx, `
		SELECT m.team, u.id, u.person, u.display_name FROM team_members m
		JOIN users u ON u.org_id = m.org_id AND u.id = m.user_id
		WHERE m.org_id = ? AND u.disabled_at IS NULL ORDER BY u.person`, org)
	if err != nil {
		return nil, err
	}
	for mrows.Next() {
		var team string
		var m TeamMember
		if err := mrows.Scan(&team, &m.UserID, &m.Person, &m.Name); err != nil {
			_ = mrows.Close()
			return nil, err
		}
		if i, ok := idx[team]; ok {
			out[i].Members = append(out[i].Members, m)
		}
	}
	_ = mrows.Close()
	if err := mrows.Err(); err != nil {
		return nil, err
	}
	srows, err := s.r.QueryContext(ctx, `
		SELECT st.team, sv.alias FROM server_teams st
		JOIN servers sv ON sv.org_id = st.org_id AND sv.id = st.server_id
		WHERE st.org_id = ? ORDER BY sv.alias`, org)
	if err != nil {
		return nil, err
	}
	defer func() { _ = srows.Close() }()
	for srows.Next() {
		var team, alias string
		if err := srows.Scan(&team, &alias); err != nil {
			return nil, err
		}
		if i, ok := idx[team]; ok {
			out[i].Servers = append(out[i].Servers, alias)
		}
	}
	return out, srows.Err()
}

// AddTeamMember puts an active user in a team. Adding someone already in it
// is a no-op, still audited.
func (s *Store) AddTeamMember(ctx context.Context, org OrgID, slug, userID string, now time.Time, ev AuditEvent) error {
	ev.Phase = "admin"
	return s.tx(ctx, func(tx *sql.Tx) error {
		var one int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM teams WHERE org_id = ? AND slug = ?`, org, slug).Scan(&one); errors.Is(err, sql.ErrNoRows) {
			return ErrUnknownTeam
		} else if err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM users WHERE org_id = ? AND id = ? AND disabled_at IS NULL`, org, userID).Scan(&one); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO team_members (org_id, team, user_id, added_at) VALUES (?, ?, ?, ?)`,
			org, slug, userID, ts(now)); err != nil {
			return err
		}
		return insertAudit(ctx, tx, org, []AuditEvent{ev})
	})
}

// RemoveTeamMember takes a user out of a team. It is ErrNotFound when they
// were not in it.
func (s *Store) RemoveTeamMember(ctx context.Context, org OrgID, slug, userID string, ev AuditEvent) error {
	ev.Phase = "admin"
	return s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM team_members WHERE org_id = ? AND team = ? AND user_id = ?`, org, slug, userID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return ErrNotFound
		}
		return insertAudit(ctx, tx, org, []AuditEvent{ev})
	})
}

// checkTeams fails with ErrUnknownTeam unless every name is "everyone" or an
// existing team of org.
func checkTeams(ctx context.Context, tx *sql.Tx, org OrgID, teams []string) error {
	for _, t := range teams {
		if t == TeamEveryone {
			continue
		}
		var one int
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM teams WHERE org_id = ? AND slug = ?`, org, t).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrUnknownTeam
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func insertServerTeams(ctx context.Context, tx *sql.Tx, org OrgID, serverID string, teams []string) error {
	for _, t := range teams {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO server_teams (org_id, server_id, team) VALUES (?, ?, ?)`, org, serverID, t); err != nil {
			return err
		}
	}
	return nil
}

// SetServerTeams replaces the teams that reach a server. An empty list makes
// the server reachable by nobody.
func (s *Store) SetServerTeams(ctx context.Context, org OrgID, serverID string, teams []string, ev AuditEvent) error {
	ev.Phase = "admin"
	return s.tx(ctx, func(tx *sql.Tx) error {
		var one int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM servers WHERE org_id = ? AND id = ?`, org, serverID).Scan(&one); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		if err := checkTeams(ctx, tx, org, teams); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM server_teams WHERE org_id = ? AND server_id = ?`, org, serverID); err != nil {
			return err
		}
		if err := insertServerTeams(ctx, tx, org, serverID, teams); err != nil {
			return err
		}
		return insertAudit(ctx, tx, org, []AuditEvent{ev})
	})
}

// ServerTeams returns the teams of every server of org, by server id, each
// list sorted.
func (s *Store) ServerTeams(ctx context.Context, org OrgID) (map[string][]string, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT server_id, team FROM server_teams WHERE org_id = ? ORDER BY server_id, team`, org)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string][]string{}
	for rows.Next() {
		var id, team string
		if err := rows.Scan(&id, &team); err != nil {
			return nil, err
		}
		out[id] = append(out[id], team)
	}
	return out, rows.Err()
}

// Access is what one caller reaches: the ids of the servers and the teams
// they are in ("everyone" included when they are in it).
type Access struct {
	Servers map[string]bool
	Teams   []string
}

// Reaches reports whether the caller may use the server.
func (a Access) Reaches(serverID string) bool { return a.Servers[serverID] }

// AccessFor returns what a caller reaches. userID is the caller's member, or
// "" for a token whose person is no member: such a caller is in "everyone"
// only. A disabled member, or a user id that is not a member, reaches
// nothing.
func (s *Store) AccessFor(ctx context.Context, org OrgID, userID string) (Access, error) {
	a := Access{Servers: map[string]bool{}}
	if userID != "" {
		var disabled sql.NullString
		err := s.r.QueryRowContext(ctx, `SELECT disabled_at FROM users WHERE org_id = ? AND id = ?`, org, userID).Scan(&disabled)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && disabled.Valid) {
			return a, nil
		}
		if err != nil {
			return a, err
		}
	}
	a.Teams = []string{TeamEveryone}
	if userID != "" {
		rows, err := s.r.QueryContext(ctx, `SELECT team FROM team_members WHERE org_id = ? AND user_id = ? ORDER BY team`, org, userID)
		if err != nil {
			return Access{}, err
		}
		for rows.Next() {
			var t string
			if err := rows.Scan(&t); err != nil {
				_ = rows.Close()
				return Access{}, err
			}
			a.Teams = append(a.Teams, t)
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return Access{}, err
		}
	}
	rows, err := s.r.QueryContext(ctx, `
		SELECT DISTINCT server_id FROM server_teams
		WHERE org_id = ? AND (team = 'everyone' OR team IN (SELECT team FROM team_members WHERE org_id = ? AND user_id = ?))`, org, org, userID)
	if err != nil {
		return Access{}, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return Access{}, err
		}
		a.Servers[id] = true
	}
	sort.Strings(a.Teams)
	return a, rows.Err()
}

// AddServerTeam gives one more team access to a server.
func (s *Store) AddServerTeam(ctx context.Context, org OrgID, serverID, team string, ev AuditEvent) error {
	ev.Phase = "admin"
	return s.tx(ctx, func(tx *sql.Tx) error {
		var one int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM servers WHERE org_id = ? AND id = ?`, org, serverID).Scan(&one); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		if err := checkTeams(ctx, tx, org, []string{team}); err != nil {
			return err
		}
		if err := insertServerTeams(ctx, tx, org, serverID, []string{team}); err != nil {
			return err
		}
		return insertAudit(ctx, tx, org, []AuditEvent{ev})
	})
}

// RemoveServerTeam takes a team's access to a server away. It is ErrNotFound
// when the team had none.
func (s *Store) RemoveServerTeam(ctx context.Context, org OrgID, serverID, team string, ev AuditEvent) error {
	ev.Phase = "admin"
	return s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM server_teams WHERE org_id = ? AND server_id = ? AND team = ?`, org, serverID, team)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return ErrNotFound
		}
		return insertAudit(ctx, tx, org, []AuditEvent{ev})
	})
}
