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
};

export type Pin = {
  kind: string;
  name: string;
  hash: string;
  status: string;
  definition: { description?: string; title?: string } | null;
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
  issuer: string;
  connected: boolean;
  scopes?: string;
  connected_at?: string;
  last_used_at?: string;
};
