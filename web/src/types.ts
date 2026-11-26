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
  team_tools?: Record<string, string[]>; // narrowed teams' tools (ADR 0026); admins only
  team_prompts?: Record<string, string[]>;
  team_resources?: Record<string, string[]>;
  team_templates?: Record<string, string[]>;
  // For editing (ADR 0029), admins only: never a value or a secret.
  header_names?: string[];
  client_id?: string;
  has_client_secret?: boolean;
};

export type Pin = {
  kind: string;
  name: string;
  hash: string;
  status: string;
  definition: { description?: string; title?: string; [key: string]: unknown } | null;
  findings?: Finding[]; // signs of hidden instructions (F11 scan); a hint, never a decision
  serving?: boolean; // an accepted definition the server presents now; absent while unknown
  previous?: { hash: string; definition: Pin["definition"] }; // what a pending one would replace
};

export type Finding = { path: string; rule: string; severity: "warn" | "note"; message: string; excerpt?: string };

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
  conditions?: Condition[]; // on the call's arguments (ADR 0027)
  grant_of?: string; // the approval a grant was made from
};

export type Condition = { arg: string; op: string; value?: unknown };

export type Member = {
  person: string;
  name: string;
  roles: string[]; // ADR 0025
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
  decided_by?: string;
  decided_at?: string | null;
  own?: boolean; // the viewer's own request
  decide_blocked?: string; // why the viewer cannot decide it
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
  issuer?: string; // the authorization server an own_app entry's secret goes to
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

/** Durations in milliseconds, or sizes in bytes (ADR 0024). */
export type Stats = { p50: number; p95: number; p99: number; min: number; max: number; avg: number };

export type StatGroup = {
  key: string;
  calls: number;
  ok: number;
  tool_errors: number;
  errors: number;
  refused: number;
  error_rate: number;
  total_ms: Stats;
  decide_ms: Stats;
  audit_ms: Stats;
  connect_ms: Stats;
  upstream_ms: Stats;
  args_bytes: Stats;
  result_bytes: Stats;
};

export type StatSummary = {
  window: string;
  by: string;
  total: StatGroup;
  groups: StatGroup[];
  labels?: Record<string, string>;
  /** More calls ran in the window than the time sample holds; counts still cover all. */
  sampled: boolean;
  sample_from?: string;
  recording: boolean;
};

export type StatBucket = { start: string; calls: number; errors: number; tool_errors: number; refused: number; p50_ms: number; p95_ms: number };

export type StatSeries = { bucket: string; buckets: StatBucket[]; sampled: boolean; sample_from?: string };

export type TelemetryStorage = {
  recording: boolean;
  retention_days: number;
  calls: number;
  oldest_call: string | null;
  logs: number;
  oldest_log: string | null;
  dropped: number;
};

export type LogEntry = {
  id: string;
  time: string;
  level: string;
  category: string;
  server?: string;
  member?: string;
  message: string;
  repeats: number;
};

/** A person's own request that waits for a decision (F10). */
export type MyApproval = { id: string; server: string; tool: string; requester: string; reason: string; created_at: string; expires_at: string };

// A person's connection to a server, as an admin sees it (F07 A). Never a token.
export type AnyConnection = {
  server: string;
  person: string;
  kind: "oauth" | "key";
  scopes: string;
  catalog: boolean;
  connected_at: string;
  last_used_at: string;
  refreshed_at: string;
  unused: boolean;
};
