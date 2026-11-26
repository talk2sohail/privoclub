# Outbound requests (egress)

Privo fetches addresses that others give it: the servers admins add, their
sign-in metadata, and AI clients' metadata documents. Any of them could point
inside your network, at a cloud metadata endpoint, the database or the
Coolify host. This page says how Privo stops that (server-side request
forgery, SSRF), how to let it reach an in-house server, and how to run it
behind a reverse proxy.

The design is
[ADR 0008](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0008-egress-and-ssrf.md).
The outbound client has been there from the first release;
`PRIVO_TRUSTED_PROXIES` arrived in 0.8.0.

## How requests are checked

Every outbound request goes through one client: connections to MCP servers,
their OAuth sign-in and token requests, GitHub sign-in, and AI clients'
metadata documents.

- It checks the address each name resolves to at the moment it connects. That
  also defeats DNS rebinding: a name that pointed somewhere public when the
  server was added, and somewhere private later, is still caught.
- Redirects are checked hop by hop: never to another host, never from
  `https` to `http`, and at most three (one for sign-in requests).
- It ignores `HTTP_PROXY` and `HTTPS_PROXY`: a proxy would hide the real
  destination from the check.
- It gives up after 10 seconds to connect or to finish TLS, and after 60
  seconds waiting for a reply's headers. The limits on servers' replies are
  the `PRIVO_UPSTREAM_*` settings in the
  [configuration reference](../installation/configuration.md).

When a server is added, its address must be `http` or `https`, with no user
name or password, and every address its name resolves to must pass. Otherwise
the admin API answers 422 with the reason; here, for a server at a private
address:

```json
{"error": "egress policy refused https://mcp.internal.example/mcp: egress: 10.0.0.5 refused: private network"}
```

## Addresses refused

| Range | What it is |
| --- | --- |
| `127.0.0.0/8`, `::1` | Loopback: Privo's own host |
| `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`, `fc00::/7` | Private networks |
| `169.254.0.0/16`, `fe80::/10` | Link-local, including cloud metadata at `169.254.169.254` |
| `100.64.0.0/10` | Carrier-grade NAT |
| `0.0.0.0/8`, `::` | "This network" and unspecified |
| `224.0.0.0/4`, `ff00::/8` | Multicast |
| `240.0.0.0/4`, `192.0.0.0/24`, `198.18.0.0/15`, `100::/64` | Reserved, protocol assignments, benchmarking, discard |
| `192.0.2.0/24`, `198.51.100.0/24`, `203.0.113.0/24`, `2001:db8::/32` | Documentation |
| `64:ff9b::/96`, `64:ff9b:1::/48`, `2002::/16`, `2001::/32` | NAT64, 6to4 and Teredo, which can carry a private IPv4 address inside IPv6 |

An IPv4 address written as IPv6, such as `::ffff:10.0.0.5`, is checked as the
IPv4 address.

## Allowing an internal server

On a self-hosted install, an MCP server on your own network needs its range
allowed. Set `PRIVO_EGRESS_ALLOW_CIDRS` to a comma-separated list of CIDRs,
the narrowest that work, and restart Privo. For example, one in-house server
at a private address:

```sh
PRIVO_EGRESS_ALLOW_CIDRS=10.20.0.15/32
```

A GitHub Enterprise Server at a private address needs its range here too.

!!! warning
    An allowed range is checked before the list above, so it opens whatever
    it covers, even loopback or the cloud metadata range. Every admin who adds
    a server can then reach what is in it, and so can a server that names an
    address there in its sign-in metadata. Allow single addresses (`/32`)
    where you can, and never `127.0.0.0/8` or `169.254.0.0/16`.

AI clients' metadata documents never use the allowed ranges: a client that
has not signed in yet chooses that address. Privo fetches them from public
addresses only, within five seconds, and follows no redirect.

## Behind a reverse proxy

Privo counts some limits per client address: today, AI clients that register
themselves. Behind a reverse proxy, such as Traefik on Coolify, every request
arrives from the proxy, so every client would share one count.

Set `PRIVO_TRUSTED_PROXIES` to your proxies' ranges. Privo then reads
`X-Forwarded-For` from the right. Each proxy adds the address it received the
request from, so the first address that is not a trusted proxy is the
client's. Anything to its left was written by the client and is never used.
Without the setting, Privo uses the connection's own address and ignores the
header.

On Coolify, list the Docker networks of Coolify's Traefik, for example the
private ranges Docker uses (`docker network inspect` shows yours):

```sh
PRIVO_TRUSTED_PROXIES=10.0.0.0/8,172.16.0.0/12
```

With a CDN such as Cloudflare in front of the proxy, add the CDN's published
ranges too, or every request seems to come from the CDN. IPv6 clients are
counted per `/64`, since one subscriber usually has a whole `/64`.

!!! warning
    Never list an address that a client can reach Privo from directly. A
    client there could write any `X-Forwarded-For` it likes, and choose its
    own address.

## Not built yet

- Blocked attempts are not audit events yet. A blocked address is refused
  when a server is added; a name that resolves to one later fails to connect.
- The isolated stdio runner's outbound proxy (ADR 0007). The development-only
  [local runner](../advanced/stdio-runner.md) runs processes whose own network
  traffic Privo does not check at all.
