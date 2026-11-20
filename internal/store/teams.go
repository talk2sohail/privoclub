package store

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"slices"
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
		// Keep the rows of teams that stay, so their toolsets stay (ADR 0026).
		var had []string
		rows, err := tx.QueryContext(ctx, `SELECT team FROM server_teams WHERE org_id = ? AND server_id = ?`, org, serverID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var t string
			if err := rows.Scan(&t); err != nil {
				_ = rows.Close()
				return err
			}
			had = append(had, t)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for _, t := range had {
			if slices.Contains(teams, t) {
				continue
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM server_teams WHERE org_id = ? AND server_id = ? AND team = ?`, org, serverID, t); err != nil {
				return err
			}
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
	// Tools holds, for a server whose every reaching team is narrowed, the
	// tool names the caller sees (ADR 0026). A server not in it shows all.
	Tools map[string]map[string]bool
	Teams []string
	// Roles are the member's current roles, read with their access (ADR
	// 0021); nil for a caller who is no member or a removed one.
	Roles []string
}

// Reaches reports whether the caller may use the server.
func (a Access) Reaches(serverID string) bool { return a.Servers[serverID] }

// SeesTool reports whether the caller sees a tool of a server they reach.
func (a Access) SeesTool(serverID, name string) bool {
	if !a.Reaches(serverID) {
		return false
	}
	set, narrowed := a.Tools[serverID]
	return !narrowed || set[name]
}

// AccessFor returns what a caller reaches. userID is the caller's member, or
// "" for a token whose person is no member: such a caller is in "everyone"
// only. A disabled member, or a user id that is not a member, reaches
// nothing.
func (s *Store) AccessFor(ctx context.Context, org OrgID, userID string) (Access, error) {
	a := Access{Servers: map[string]bool{}}
	if userID != "" {
		u, err := scanUser(org, s.r.QueryRowContext(ctx, `SELECT `+userCols+` FROM users u WHERE u.org_id = ? AND u.id = ?`, org, userID))
		if errors.Is(err, sql.ErrNoRows) || (err == nil && u.DisabledAt != nil) {
			return a, nil
		}
		if err != nil {
			return a, err
		}
		a.Roles = u.Roles
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
	// A server shows all its tools when any reaching team is not narrowed;
	// otherwise the union of the reaching teams' lists (ADR 0026).
	const reaching = `org_id = ? AND (team = 'everyone' OR team IN (SELECT team FROM team_members WHERE org_id = ? AND user_id = ?))`
	rows, err := s.r.QueryContext(ctx, `SELECT server_id, MIN(narrowed) FROM server_teams WHERE `+reaching+` GROUP BY server_id`, org, org, userID)
	if err != nil {
		return Access{}, err
	}
	for rows.Next() {
		var id string
		var narrowed bool
		if err := rows.Scan(&id, &narrowed); err != nil {
			_ = rows.Close()
			return Access{}, err
		}
		a.Servers[id] = true
		if narrowed {
			if a.Tools == nil {
				a.Tools = map[string]map[string]bool{}
			}
			a.Tools[id] = map[string]bool{}
		}
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return Access{}, err
	}
	if len(a.Tools) > 0 {
		trows, err := s.r.QueryContext(ctx, `SELECT server_id, name FROM server_team_tools WHERE `+reaching, org, org, userID)
		if err != nil {
			return Access{}, err
		}
		defer func() { _ = trows.Close() }()
		for trows.Next() {
			var id, name string
			if err := trows.Scan(&id, &name); err != nil {
				return Access{}, err
			}
			if set, ok := a.Tools[id]; ok {
				set[name] = true
			}
		}
		if err := trows.Err(); err != nil {
			return Access{}, err
		}
	}
	sort.Strings(a.Teams)
	return a, nil
}

// MaxTeamTools bounds one team's list of tools on a server.
const MaxTeamTools = 1000

// SetTeamTools sets which tools of a server a team sees (ADR 0026): nil for
// all of them, otherwise exactly the names given (none when empty). The team
// must be on the server (ErrNotFound otherwise). It writes ev with the change.
func (s *Store) SetTeamTools(ctx context.Context, org OrgID, serverID, team string, tools []string, ev AuditEvent) error {
	if len(tools) > MaxTeamTools {
		return errors.New("store: too many tools")
	}
	ev.Phase = "admin"
	return s.tx(ctx, func(tx *sql.Tx) error {
		narrowed := 0
		if tools != nil {
			narrowed = 1
		}
		res, err := tx.ExecContext(ctx, `UPDATE server_teams SET narrowed = ? WHERE org_id = ? AND server_id = ? AND team = ?`, narrowed, org, serverID, team)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return ErrNotFound
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM server_team_tools WHERE org_id = ? AND server_id = ? AND team = ?`, org, serverID, team); err != nil {
			return err
		}
		for _, name := range tools {
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO server_team_tools (org_id, server_id, team, name) VALUES (?, ?, ?, ?)`,
				org, serverID, team, name); err != nil {
				return err
			}
		}
		return insertAudit(ctx, tx, org, []AuditEvent{ev})
	})
}

// TeamTools returns the narrowed toolsets of an org: server id, then team,
// then the sorted tool names (an empty, non-nil list means none). Teams that
// see all of a server's tools are not in it.
func (s *Store) TeamTools(ctx context.Context, org OrgID) (map[string]map[string][]string, error) {
	out := map[string]map[string][]string{}
	rows, err := s.r.QueryContext(ctx, `SELECT server_id, team FROM server_teams WHERE org_id = ? AND narrowed = 1`, org)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, team string
		if err := rows.Scan(&id, &team); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if out[id] == nil {
			out[id] = map[string][]string{}
		}
		out[id][team] = []string{}
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	trows, err := s.r.QueryContext(ctx, `SELECT server_id, team, name FROM server_team_tools WHERE org_id = ? ORDER BY server_id, team, name`, org)
	if err != nil {
		return nil, err
	}
	defer func() { _ = trows.Close() }()
	for trows.Next() {
		var id, team, name string
		if err := trows.Scan(&id, &team, &name); err != nil {
			return nil, err
		}
		if l, ok := out[id][team]; ok {
			out[id][team] = append(l, name)
		}
	}
	return out, trows.Err()
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
