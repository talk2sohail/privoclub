# 0007. Stdio runner isolation and package supply chain

- Status: Accepted, 2026-10-28
- Date: 2026-10-28

## Context

Many MCP servers ship as `npx` or `uvx` packages that run as local processes.
Running them is running third-party code next to customer credentials. One
shared container with `npx -y` at call time lets a malicious package reach the
database, read other servers' secrets, or exhaust memory for everyone.

## Decision

### Process model

- One process per (org, server, credential owner); one per (org, server) only
  for servers with shared credentials.
- Each process: its own Unix user, cgroup v2 limits on memory, CPU and process
  count, read-only root file system with a private writable scratch directory,
  seccomp profile, no Docker socket, no host mounts.
- Idle processes are reaped; a per-org and global process cap applies.
- Secrets reach a process only through its own environment at spawn; processes
  cannot read each other's `/proc`.

### Network

- The runner sits on its own network whose only route is to the gateway; it
  cannot reach Postgres, the admin site or the host.
- Outbound traffic goes through an allow-listing egress proxy (0008); each
  server declares the hosts it needs, recorded with its pin.

### Gateway to runner

- A versioned **runner protocol**: spawn (signed spec: package, version,
  arguments, environment, limits), attach stdio stream, kill, health.
- Mutual TLS between gateway and runner, with keys generated at install. The
  runner only spawns specs signed by the gateway.
- This protocol is the extension point for future runtimes (Kubernetes pods,
  Firecracker or gVisor sandboxes) without changing the gateway.

### Supply chain

- No unpinned packages: registration requires `package@x.y.z` plus an integrity
  hash. `npx -y <pkg>` without a version is refused.
- Packages are installed once at registration into a cached layer, with
  install scripts disabled where the package allows, never fetched at call time.
- A version change is a new pin and needs review (0001 stage 4).

### Privo Cloud

No arbitrary stdio servers in v1. Cloud offers curated, reviewed servers only,
each org in its own sandboxed runner (gVisor or Firecracker class).

## Consequences

- Per-user processes cost memory (a Node server is roughly 80 to 150 MB);
  limits, reaping and caps keep a single Coolify host predictable.
- Adding a stdio server is code execution, so it needs the `server_admin` role
  and step-up re-authentication (0003, 0011).
