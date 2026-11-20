// The admin API's answers, as the app reads them (internal/control).

export type ServerView = {
  id: string;
  alias: string;
  transport: string;
  enabled: boolean;
  status: string;
  error?: string;
  target: string;
  items: Record<string, number>;
  pending_review: number;
  auth?: string;
  registration?: string;
  connect_url?: string;
  teams?: string[];
};

export type Finding = { path: string; rule: string; severity: "warn" | "note"; message: string; excerpt?: string };

export type Pin = {
  kind: string;
  name: string;
  hash: string;
  status: string;
  // The whole definition as the server sent it: upstream text, shown only as text.
  definition: Record<string, unknown> | null;
  findings?: Finding[]; // signs of hidden instructions (F11); a hint, never a decision
  previous?: { hash: string; definition: Record<string, unknown> | null }; // the accepted version it would replace
};

export type ServerConnection = {
  person: string;
  scopes: string;
  catalog: boolean;
  connected_at: string;
  last_used_at: string;
};

export type Policy = {
  id: string;
  server: string;
  tool: string;
  subject: string;
  action: string;
  note: string;
  created_by: string;
  expires_at: string | null;
  subject_person?: string; // the member of a "user:" rule (a grant from an approval)
};

export type Member = {
  person: string;
  name: string;
  roles: string[]; // ADR 0021
  github_login: string;
  created_at: string;
  removed: boolean;
};

export type Token = {
  id: string;
  token?: string;
  name: string;
  role: string;
  kind: string;
  person: string;
  // A member's own token, bound to them (F07 R-01); every other token acts
  // as no member, whatever person it names.
  personal?: boolean;
  created_at?: string;
  expires_at: string;
};

export type AuditEvent = {
  time: string;
  actor: string;
  server: string;
  item: string;
  action: string;
  decision: string;
  outcome: string;
  approved_by: string;
  account?: string; // whose upstream account the call used: "<person> (user <id>)"
};

export type Approval = {
  id: string;
  status: string;
  requester: string;
  requester_person: string;
  server: string;
  tool: string;
  reason: string;
  created_at: string;
  expires_at: string;
  arguments?: unknown;
  arguments_error?: string;
  decided_by?: string;
  decided_at?: string | null;
  own?: boolean; // the viewer's own request
  decide_blocked?: string; // why the viewer cannot decide it
};

export type MyConnection = {
  alias: string;
  issuer: string;
  connected: boolean;
  scopes?: string;
  connected_at?: string;
  last_used_at?: string;
};

export type Team = {
  slug: string;
  name: string;
  description: string;
  profile: string;
  built_in?: boolean;
  members: { person: string; name: string }[];
  servers: string[];
};

export type Profile = { id: string; name: string; description: string };

export type CatalogEntry = {
  id: string;
  name: string;
  description: string;
  alias: string;
  url: string;
  transport: string;
  auth: "none" | "oauth";
  registration?: "automatic" | "own_app";
  app_url?: string;
  app_help?: string;
  scopes?: string[];
  profiles: string[];
  checked_on: string;
  installed?: string;
};

export type Catalog = {
  profiles: Profile[];
  entries: CatalogEntry[];
  callback_url: string;
  oauth_available: boolean;
};

export type MyServer = {
  alias: string;
  name: string;
  description?: string;
  status: string;
  tools: number;
  auth?: string;
  connected?: boolean;
  connect_url?: string;
};

export type MyTools = {
  teams: { slug: string; name: string }[];
  servers: MyServer[];
};

/** A person's own request that waits for a decision (F10). */
export type MyApproval = { id: string; server: string; tool: string; requester: string; reason: string; created_at: string; expires_at: string };
