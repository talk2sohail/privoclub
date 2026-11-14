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
  auth?: string; // "" | oauth | key
  registration?: string;
  connect_url?: string;
  key_header?: string;
  teams?: string[];
};

export type Pin = {
  kind: string;
  name: string;
  hash: string;
  status: string;
  definition: { description?: string; title?: string; [key: string]: unknown } | null;
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
};

export type Member = {
  person: string;
  name: string;
  role: string;
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
  /** The token belongs to a member, who made it signed in (ADR 0021). */
  bound?: boolean;
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
  user?: string; // the member the event is about or the call ran as (ADR 0021)
  client?: string; // the AI client, when it signed in with OAuth (ADR 0022)
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
};

export type MyConnection = {
  alias: string;
  kind?: string; // oauth | key
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

// An AI client that signed in with OAuth and acts as a member (ADR 0022).
export type ConnectedClient = {
  id: string;
  client: string;
  client_id: string;
  kind: string; // known | approved | document | registered
  person?: string;
  name?: string;
  allowed_at: string;
  last_used_at: string;
  expires_at: string;
};

// A client that may ask a member for access.
export type OAuthClient = {
  client_id: string;
  name: string;
  kind: string; // known | approved | registered
  redirect_uris?: string[];
  created_at?: string;
  authorized_at?: string;
  grants: number;
};
