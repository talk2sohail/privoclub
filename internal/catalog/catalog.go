// Package catalog lists hosted MCP servers an admin can add in one step
// (ADR 0020). It is data compiled into the binary, never fetched: an entry
// only fills in the add-server form, and adding still runs every check of the
// admin API (egress, discovery, registration, review of every tool).
//
// Each entry was checked on CheckedOn with Privo's own discovery code
// (upstreamauth.Discover and ChooseRegistration): the URL answers, OAuth
// servers publish protected resource and authorization server metadata whose
// issuer matches, and Registration says whether Privo registers itself
// (client ID metadata document or dynamic registration) or the provider needs
// an OAuth app of the admin's own. Servers that failed that check (Box: issuer
// mismatch; Atlassian: no protected resource metadata) are left out.
package catalog

import "slices"

// Profiles: the starting points for a team.
const (
	ProfileDevelopers = "developers"
	ProfileDesigners  = "designers"
	ProfileProduct    = "product"
	ProfileSales      = "sales"
	ProfileEveryone   = "everyone"
)

// Sign-in kinds.
const (
	AuthNone  = "none"  // no sign-in: shared by everyone who reaches it
	AuthOAuth = "oauth" // each person connects their own account (ADR 0017)
)

// Registration says how Privo gets an OAuth client at the provider.
const (
	RegistrationAutomatic = "automatic" // client ID metadata document or dynamic registration
	RegistrationOwnApp    = "own_app"   // the admin creates an OAuth app and gives its client id
)

// Profile is a kind of team and what it usually needs.
type Profile struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// Entry is one hosted MCP server.
type Entry struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	Alias        string   `json:"alias"` // suggested server name in Privo
	URL          string   `json:"url"`
	Transport    string   `json:"transport"`
	Auth         string   `json:"auth"`
	Registration string   `json:"registration,omitempty"`
	AppURL       string   `json:"app_url,omitempty"`  // where to create the OAuth app (own_app)
	AppHelp      string   `json:"app_help,omitempty"` // what to do there
	Scopes       []string `json:"scopes,omitempty"`   // a narrower set than the provider offers, when it offers too much
	// Issuer is the authorization server the entry's people sign in at. An
	// own_app entry names it, so the client secret goes only there and never
	// to one the MCP server names (F07 R-05).
	Issuer    string   `json:"issuer,omitempty"`
	Profiles  []string `json:"profiles"`
	CheckedOn string   `json:"checked_on"`
}

// Profiles returns the team profiles in display order.
func Profiles() []Profile {
	return []Profile{
		{ProfileDevelopers, "Developers", "Code, issues, errors and deployments."},
		{ProfileDesigners, "Designers", "Design files, whiteboards and sites."},
		{ProfileProduct, "Product managers", "Docs, roadmaps, tasks and analytics."},
		{ProfileSales, "Sales and support", "Customers, conversations and the CRM."},
		{ProfileEveryone, "Everyone", "Useful to the whole company."},
	}
}

const checked = "2026-11-03"

var entries = []Entry{
	// Developers
	{ID: "github", Name: "GitHub", Description: "Repositories, issues, pull requests and Actions.", Alias: "github",
		URL: "https://api.githubcopilot.com/mcp/", Transport: "http", Auth: AuthOAuth, Registration: RegistrationOwnApp,
		AppURL: "https://github.com/settings/applications/new", AppHelp: "Create an OAuth App (not a GitHub App), set its callback URL to the one shown, and paste its client id and secret.",
		Scopes: []string{"repo", "read:org", "read:user"}, Issuer: "https://github.com/login/oauth", Profiles: []string{ProfileDevelopers}, CheckedOn: checked},
	{ID: "linear", Name: "Linear", Description: "Issues, projects and cycles.", Alias: "linear",
		URL: "https://mcp.linear.app/mcp", Transport: "http", Auth: AuthOAuth, Registration: RegistrationAutomatic,
		Profiles: []string{ProfileDevelopers, ProfileProduct}, CheckedOn: checked},
	{ID: "sentry", Name: "Sentry", Description: "Errors, issues and releases.", Alias: "sentry",
		URL: "https://mcp.sentry.dev/mcp", Transport: "http", Auth: AuthOAuth, Registration: RegistrationAutomatic,
		Profiles: []string{ProfileDevelopers}, CheckedOn: checked},
	{ID: "vercel", Name: "Vercel", Description: "Projects, deployments and logs.", Alias: "vercel",
		URL: "https://mcp.vercel.com", Transport: "http", Auth: AuthOAuth, Registration: RegistrationAutomatic,
		Profiles: []string{ProfileDevelopers}, CheckedOn: checked},
	{ID: "supabase", Name: "Supabase", Description: "Databases, edge functions and storage.", Alias: "supabase",
		URL: "https://mcp.supabase.com/mcp", Transport: "http", Auth: AuthOAuth, Registration: RegistrationAutomatic,
		Profiles: []string{ProfileDevelopers}, CheckedOn: checked},
	{ID: "neon", Name: "Neon", Description: "Serverless Postgres projects and branches.", Alias: "neon",
		URL: "https://mcp.neon.tech/mcp", Transport: "http", Auth: AuthOAuth, Registration: RegistrationAutomatic,
		Profiles: []string{ProfileDevelopers}, CheckedOn: checked},
	{ID: "cloudflare-observability", Name: "Cloudflare Observability", Description: "Workers logs and analytics.", Alias: "cf-observe",
		URL: "https://observability.mcp.cloudflare.com/mcp", Transport: "http", Auth: AuthOAuth, Registration: RegistrationAutomatic,
		Profiles: []string{ProfileDevelopers}, CheckedOn: checked},
	{ID: "stripe", Name: "Stripe", Description: "Payments, customers and the Stripe docs.", Alias: "stripe",
		URL: "https://mcp.stripe.com", Transport: "http", Auth: AuthOAuth, Registration: RegistrationAutomatic,
		Profiles: []string{ProfileDevelopers}, CheckedOn: checked},
	{ID: "context7", Name: "Context7", Description: "Current library and framework documentation. No sign-in.", Alias: "context7",
		URL: "https://mcp.context7.com/mcp", Transport: "http", Auth: AuthNone, Profiles: []string{ProfileDevelopers}, CheckedOn: checked},
	{ID: "deepwiki", Name: "DeepWiki", Description: "Questions about public GitHub repositories. No sign-in.", Alias: "deepwiki",
		URL: "https://mcp.deepwiki.com/mcp", Transport: "http", Auth: AuthNone, Profiles: []string{ProfileDevelopers}, CheckedOn: checked},
	{ID: "cloudflare-docs", Name: "Cloudflare Docs", Description: "Search the Cloudflare documentation. No sign-in.", Alias: "cf-docs",
		URL: "https://docs.mcp.cloudflare.com/mcp", Transport: "http", Auth: AuthNone, Profiles: []string{ProfileDevelopers}, CheckedOn: checked},

	// Designers
	{ID: "figma", Name: "Figma", Description: "Design files, components and variables.", Alias: "figma",
		URL: "https://mcp.figma.com/mcp", Transport: "http", Auth: AuthOAuth, Registration: RegistrationAutomatic,
		Profiles: []string{ProfileDesigners, ProfileDevelopers}, CheckedOn: checked},
	{ID: "canva", Name: "Canva", Description: "Designs, brand templates and assets.", Alias: "canva",
		URL: "https://mcp.canva.com/mcp", Transport: "http", Auth: AuthOAuth, Registration: RegistrationAutomatic,
		Profiles: []string{ProfileDesigners}, CheckedOn: checked},
	{ID: "webflow", Name: "Webflow", Description: "Sites, pages and the CMS.", Alias: "webflow",
		URL: "https://mcp.webflow.com/mcp", Transport: "http", Auth: AuthOAuth, Registration: RegistrationAutomatic,
		Profiles: []string{ProfileDesigners}, CheckedOn: checked},
	{ID: "miro", Name: "Miro", Description: "Boards and whiteboards.", Alias: "miro",
		URL: "https://mcp.miro.com/", Transport: "http", Auth: AuthOAuth, Registration: RegistrationAutomatic,
		Profiles: []string{ProfileDesigners, ProfileProduct}, CheckedOn: checked},

	// Product
	{ID: "notion", Name: "Notion", Description: "Pages, databases and search across the workspace.", Alias: "notion",
		URL: "https://mcp.notion.com/mcp", Transport: "http", Auth: AuthOAuth, Registration: RegistrationAutomatic,
		Profiles: []string{ProfileProduct, ProfileEveryone}, CheckedOn: checked},
	{ID: "asana", Name: "Asana", Description: "Tasks, projects and goals.", Alias: "asana",
		URL: "https://mcp.asana.com/v2/mcp", Transport: "http", Auth: AuthOAuth, Registration: RegistrationOwnApp,
		AppURL: "https://app.asana.com/0/my-apps", AppHelp: "Create an app, add the callback URL shown as a redirect URL, and paste its client id and secret.",
		Profiles: []string{ProfileProduct}, Issuer: "https://app.asana.com", CheckedOn: checked},
	{ID: "monday", Name: "monday.com", Description: "Boards, items and updates.", Alias: "monday",
		URL: "https://mcp.monday.com/mcp", Transport: "http", Auth: AuthOAuth, Registration: RegistrationAutomatic,
		Profiles: []string{ProfileProduct}, CheckedOn: checked},
	{ID: "clickup", Name: "ClickUp", Description: "Tasks, docs and spaces.", Alias: "clickup",
		URL: "https://mcp.clickup.com/mcp", Transport: "http", Auth: AuthOAuth, Registration: RegistrationAutomatic,
		Profiles: []string{ProfileProduct}, CheckedOn: checked},
	{ID: "amplitude", Name: "Amplitude", Description: "Product analytics: charts, cohorts and experiments.", Alias: "amplitude",
		URL: "https://mcp.amplitude.com/mcp", Transport: "http", Auth: AuthOAuth, Registration: RegistrationAutomatic,
		Profiles: []string{ProfileProduct}, CheckedOn: checked},
	{ID: "posthog", Name: "PostHog", Description: "Product analytics, feature flags and session replays.", Alias: "posthog",
		URL: "https://mcp.posthog.com/mcp", Transport: "http", Auth: AuthOAuth, Registration: RegistrationAutomatic,
		Profiles: []string{ProfileProduct, ProfileDevelopers}, CheckedOn: checked},

	// Sales and support
	{ID: "hubspot", Name: "HubSpot", Description: "Contacts, companies and deals.", Alias: "hubspot",
		URL: "https://mcp.hubspot.com/", Transport: "http", Auth: AuthOAuth, Registration: RegistrationOwnApp,
		AppHelp:  "Create an app in your HubSpot developer account with the callback URL shown as its redirect URL, and paste its client id and secret.",
		Profiles: []string{ProfileSales}, Issuer: "https://mcp.hubspot.com", CheckedOn: checked},
	{ID: "intercom", Name: "Intercom", Description: "Conversations, contacts and help articles.", Alias: "intercom",
		URL: "https://mcp.intercom.com/mcp", Transport: "http", Auth: AuthOAuth, Registration: RegistrationAutomatic,
		Profiles: []string{ProfileSales, ProfileProduct}, CheckedOn: checked},

	// Everyone
	{ID: "slack", Name: "Slack", Description: "Search and post in channels and messages.", Alias: "slack",
		URL: "https://mcp.slack.com/mcp", Transport: "http", Auth: AuthOAuth, Registration: RegistrationOwnApp,
		AppURL: "https://api.slack.com/apps", AppHelp: "Create a Slack app, add the callback URL shown as a redirect URL under OAuth & Permissions, and paste its client id and secret.",
		Profiles: []string{ProfileEveryone, ProfileSales}, Issuer: "https://mcp.slack.com", CheckedOn: checked},
}

// Entries returns every entry, sorted by name.
func Entries() []Entry {
	out := slices.Clone(entries)
	for i := range out {
		out[i].Scopes, out[i].Profiles = slices.Clone(out[i].Scopes), slices.Clone(out[i].Profiles)
	}
	slices.SortFunc(out, func(a, b Entry) int {
		if a.Name < b.Name {
			return -1
		}
		if a.Name > b.Name {
			return 1
		}
		return 0
	})
	return out
}

// ByID returns one entry.
func ByID(id string) (Entry, bool) {
	for _, e := range entries {
		if e.ID == id {
			e.Scopes, e.Profiles = slices.Clone(e.Scopes), slices.Clone(e.Profiles)
			return e, true
		}
	}
	return Entry{}, false
}
