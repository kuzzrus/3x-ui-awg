# Node agent (`x-ui-agent`)

Design for an optional third kind of node: a thin, DB-less agent that only runs
Xray-core for a master panel. The agent, its installer and the master side with its UI
exist (phases 1 to 15 of [Phasing](#phasing)); the later items are not built.

## Problem

Today a node is a complete, independent x-ui install (`model.Node`, driven through
`internal/web/runtime/remote.go`): its own SQLite DB, web UI, admin login and API
token. The master replicates inbounds and clients into it over `panel/api/*` and
pulls traffic back, so two databases must agree, and every node needs a full panel
just to run Xray. The machinery that makes this work (adoption, reconcile, sweeps,
node baselines, trees) is stock upstream code and keeps changing upstream.

An agent node removes the second database: the master owns all state, renders the
node's Xray config itself, and the node only applies it and reports what it saw.

## Goals and non-goals

Goals:

- One command on a fresh VPS pairs it with the master (one-time pairing bundle).
- Master is the single source of truth: inbounds, clients, routing, outbounds.
- The node keeps serving across reboots and master outages from its last good config.
- A bad config can never take the node down: validate first, roll back on failure.
- Stock full-panel nodes keep working unchanged; agents are purely additive.

Non-goals (for v1):

- Adopting an already-installed x-ui as an agent. That stays the stock `remote.go`
  node, forever.
- Sidecar protocols on the node (MTProto, AmneziaWG, TUIC, tproxy, NaiveProxy).
  `nodeEligibleProtocols` already excludes them; agents run Xray-native inbounds only. The
  helper programs that an outbound reaches on the node's loopback (Tor, Psiphon) are another matter:
  see [Helper programs](#helper-programs).
- Per-client IP limits on agent nodes.

## Decisions

| Question                    | Decision                                                                                                                                                                                 |
| --------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Where does the agent live   | Separate binary `x-ui-agent` (`cmd/x-ui-agent`), same Go module and release tarball. No DB, no web UI, no CGO.                                                                           |
| How the master reaches it   | New `Node.Kind` (`panel` default, `agent`). A third `runtime.Runtime` implementation, `AgentRuntime`. Every caller already goes through the interface.                                   |
| Wire protocol               | Its own small HTTPS protocol (`/v1/*`), not the panel's `panel/api/*` dialect.                                                                                                           |
| Who renders the Xray config | The master. The agent receives a finished config and applies it.                                                                                                                         |
| How changes propagate       | Reuse the stock `ConfigDirty` mechanism: a mutation marks the node dirty, the sync job renders and pushes the whole config. The agent hot-applies when it can and restarts when it must. |
| Traffic                     | Agent reports cumulative counters; the master diffs them against what it already accounted of each counter (`agent_counters`).                                                           |
| Installer                   | A separate `install-agent.sh`, not an `--agent` branch in `install.sh`.                                                                                                                  |
| Push or pull                | Push (the master dials the agent) by default, and pull (the agent dials the master) per node: see [Pull mode](#pull-mode).                                                               |

### Why a new protocol and not the panel dialect

An agent could instead mimic the subset of `panel/api/*` that `Remote` calls (about 28
endpoints) and let the existing master code drive it unchanged. Rejected:

- It needs the agent to keep an inbound/client store and reimplement panel semantics
  (tag prefixes, remote ids, form-encoded inbound payloads, client records), which is
  a second database again, only smaller.
- The master could not push routing or outbounds centrally, which is half the point.
- The stock replication code assumes a node with its own state to adopt and sweep;
  an agent has none, so most of that code would run for nothing.

### Why not an `--agent` mode of the `x-ui` binary

It would still carry the DB, UI and auth weight, and every panel-only subsystem would
need a guard. A separate `main` package imports only `internal/xray` and a few
helpers, so none of the panel (and no SQLite/CGO) is linked in.

## Architecture

```
 master panel                                          agent host
 ------------                                          ----------
 Inbound/Client CRUD                                   x-ui-agent
   marks the node dirty in its own transaction           TLS server (SNI-gated)
 node sync job (about every 5 s)                         auth (bearer)
   -> render node config      --HTTPS PUT /v1/config-->  /v1/config  apply
   <- /v1/status, /v1/stats   <--------------------      /v1/status, /v1/stats
                                                         xray-core (child process)
                                                           hot-diff via gRPC, or restart
                                                         last-good config on disk
```

Reused as-is from the master: `internal/xray` (`Process`, `XrayAPI`, `ComputeHotDiff`),
`Node` columns for status (`Status`, `LatencyMs`, `CpuPct`, `MemPct`, `XrayVersion`,
`XrayState`, `XrayError`, `Guid`, ...), `NodeService.UpdateHeartbeat`, the node views
and websocket broadcast, `nodetoken` encryption for the stored secret.

## Wire protocol v1

All requests carry `Authorization: Bearer <secret>`. Bodies and responses are JSON,
except that a config push sends the Xray config itself as the body. Responses use plain
HTTP status codes (no `{success,msg,obj}` envelope).

| Endpoint                     | Purpose                                                                                                                                                                                                                                                                                                                                                                                                                                                            |
| ---------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `PUT /v1/config`             | Body: the rendered Xray config, byte for byte; `restartOnUserRemoval` is a query flag. The revision is a hash of the flag and the body bytes, computed by each side over exactly what was sent, so no JSON re-encoding can make them disagree. Validates with the core's own test (`xray -test -c`), then applies (no-op, hot, or restart). Answers `{revision, applied: noop\|hot\|restart, xrayState}`; on a bad config answers 422 and keeps the last good one. |
| `GET /v1/status`             | Agent version, hostname, GUID, config revision, Xray version/state/error, CPU, memory, uptime, interface throughput. This is the heartbeat.                                                                                                                                                                                                                                                                                                                        |
| `GET /v1/stats`              | Cumulative counters since Xray started: per inbound tag and per user email (up/down), online emails, `xrayStartedAt` so the master detects resets exactly instead of guessing from a drop, `configRevision`, the revision of the config the agent holds, which tells the master whether a push is due, and `sidecarsRevision` and `decoyRevision`, the same for the helper programs and the decoy.                                                                 |
| `POST /v1/restart`           | Restart Xray with the last good config. Answers 409 before any config exists and 500 when the core does not come back; 422 only ever means a refused push.                                                                                                                                                                                                                                                                                                         |
| `GET /v1/geo`                | The `*.dat` files in the agent's asset folder, with their sizes.                                                                                                                                                                                                                                                                                                                                                                                                   |
| `PUT /v1/geo?name=&sha256=`  | Stores one geo file. The body is the file, gzip-encoded when `Content-Encoding: gzip` says so. The agent hashes what it unpacked and refuses (400) a name that is no `*.dat` file name or a body that does not match `sha256`, refuses (413) more than 256 MiB, and swaps the file in by renaming, so the core never reads half of one. It does not restart the core.                                                                                              |
| `POST /v1/update`            | Starts the self-update: downloads the installer from its fixed address, refuses (409) while a run is on or when the agent was not installed by it, and runs it as a transient systemd unit of its own, because the installer stops this very agent. `dev=true` moves to the rolling dev channel. Answers the run as `{runId, state: pending}`.                                                                                                                     |
| `GET /v1/update`             | How the last run ended: `none`, `pending`, `success` or `failed`, with the installer's exit code. The unit writes it when the installer ends, so the new agent can answer it.                                                                                                                                                                                                                                                                                      |
| `PUT /v1/sidecars`           | Body `{enabled: [...]}`: the helper programs the agent runs, which replaces the list it had (an empty one stops them all). Answers at once, before an install has run, with `{revision, enabled, states}`; 400 for a name it does not know. See [Helper programs](#helper-programs).                                                                                                                                                                               |
| `PUT /v1/decoy`              | Body `{mode, template, proxyUrl, domain, cert, email, certFile, keyFile, port}`: the decoy the agent serves, which replaces the one it had (no mode stops it). Answers at once, before the listener is up, with `{revision, spec, state}`; 400 for a decoy it cannot serve. See [Decoy site](#decoy-site).                                                                                                                                                         |
| `GET /v1/logs?count=&level=` | The newest lines of the agent's own log, newest first, which holds what its core printed too. `count` is at most 1000 (100 by default), `level` is `debug`, `info`, `notice`, `warning` or `error` (`err` too, as the panel's own log dialog says it).                                                                                                                                                                                                             |

Any other path, wrong method, or failed auth answers the same bare 404, so a response
never says which part of the request was wrong. It does not hide that an HTTPS server
answers: that is the SNI gate's job. The master only sends valid requests, so for it a 404
means a wrong secret and a handshake failure means a wrong bundle; the agent logs the
precise reason locally, once a minute per reason so a scanner cannot flood the log. Later
phases add endpoints under the same prefix.

### Config apply

1. Apply the agent's own overrides to the body (step 4), write the candidate next to
   the live config and run the core's own config test.
2. Compare with the running config via `ComputeHotDiff`: an empty or API-applicable
   diff is applied through gRPC; anything else restarts the core.
3. If the core fails to come up after a restart, restore the previous config and
   restart again; report the failure in the response and in `GET /v1/status`.
4. The agent confines the access and error log files to its own log folder (keeping the
   file names, creating the folder; empty and `none` stay off), and refuses with a 422 a
   config it could not drive: no `api` inbound on 127.0.0.1, no stats section, no
   Handler or Stats service, no routing rule for the api inbound, or client counters
   switched off for level 0.

On boot the agent opens its port, then loads the last good config and starts Xray without
waiting for the master, so a reboot with the master down does not take the node offline.
The port opens first so a stored config that will not start cannot lock the master out; a
push made meanwhile waits for the start attempt to end.

### Helper programs

An agent can run programs beside its core that an outbound of the config reaches on the
loopback of the server. The node carries the list (`Node.Sidecars`, agents only, replaced by
each save like the tags). The entries are `tor`, a Tor client with its SOCKS port on `127.0.0.1:19050`,
and `psiphon`, a Psiphon client on `127.0.0.1:19060`: the ports the panel's own use. The outbound
that uses one is an ordinary `socks` outbound of the template, limited to the nodes that run the
program with `nodeTags`.

The master compares the `sidecarsRevision` of `GET /v1/stats` with that of the list it wants on
every tick and sends `PUT /v1/sidecars` when they differ, the revision of an empty list being
empty, so an agent that predates the endpoint, which reports none, is left alone while the node
asks for none, and is told it is too old (the 404 of an unknown path) when it asks for one. An
agent that cannot take the list is left alone for five minutes, and one that failed is tried
again after 15 seconds. The reason shows as the last error of the node.

The agent keeps the list in `sidecars.json` of its state folder and brings the programs up
from it when it starts, with the master out of reach. One loop looks at them every 10 seconds:
it installs a program the server lacks (Tor through the package manager of the server, Psiphon by
downloading the one binary the panel pins by its checksum, linux/amd64 only; either can take minutes,
so the request that sets the list does not wait for it), starts one that is
not running, and stops one that left the list. A program that died or would not start is left
alone for 30 seconds, and one that could not be installed for 5 minutes; taking it off the
list and putting it back tries at once. Every program is stopped when the agent stops.
`GET /v1/status` carries the state of each program, `pending`, `installing`, `running` or
`failed`, with the last thing it said or why it failed, which for Tor tells how far it got in
building a circuit and for Psiphon whether its tunnel is up. Psiphon runs with the config this
build bundles, the same one the panel seeds for itself. The Nodes page shows the programs beside
the node's tags.

### Decoy site

An agent can serve a decoy: what anyone who probes the port that the REALITY fallback of its core
points at is shown, in place of an error. It is the decoy half of the panel's own reverse proxy
(`internal/frontproxy`), run without a panel behind it, as a TLS listener on `127.0.0.1` (port 7443 unless
the node says another) that answers every path with a page built into the agent, with a reverse
proxy to a site the node names, or with AdGuard Home. The node carries it as `Node.Decoy` (agents only, replaced by each save,
so a form that names no mode takes it off), and the master checks it on save the way the agent does.

The listener must terminate real TLS, as the fallback hands over bytes still encrypted, so the decoy has a
domain and a certificate: ACME over HTTP-01 on port 80 (`auto`, kept in the state folder, and it fails with the
reason of the CA for a name that does not point at the node), or files that are on the node already
(`manual`). It answers a ClientHello that names another domain with a failed handshake. The fallback
target of a REALITY inbound of the node is set by hand to `127.0.0.1` and the port, as on the panel.
The look of a page is made from the GUID of the agent, so no two nodes serve the same bytes. A site to proxy must be
on a public address: anyone who probes the node can send the decoy anywhere, so an address on a private network, the
loopback or a link-local one is refused when the node is saved and again by the agent, which also refuses to dial one
that a name resolves to. A page that mocks a login does not count the failed logins of a visitor that comes from the
loopback, which behind a fallback is every one of them, so a scanner cannot lock the others out.

The master sends the decoy as it does the helper programs: when `decoyRevision` of `GET /v1/stats` is not
the one of the decoy it wants (empty for none, so an agent from before the endpoint is left alone while its
node asks for none, and is told it is too old once it asks for one). The agent keeps it in `decoy.json` of its
state folder and serves it from boot. A loop looks at it every 10 seconds: a page or a proxy address that
changed is swapped into the running listener, a new port, domain or certificate restarts it, and one that
would not start is left alone for 30 seconds. `GET /v1/status` carries how it stands (`off`, `starting`,
`running` or `failed`) and where the certificate stands (`obtaining`, `obtained` with its expiry, or
`failed` with why).

AdGuard Home as the decoy is not a pretend site: a visitor who tries it finds a working DNS service, as on the
panel. The agent installs the release the panel's own feature does (checked against the checksum the release
publishes) and runs it on the loopback, on two ports that nothing held when it was set up and that its config
keeps: one for its page, which the decoy serves at the root of the domain, and one for its DNS. A fixed port
could belong to another service of the server by then, and the decoy would show that to a visitor. A port that
something has come to hold since is chosen again, with the other, at the next start. It is a
helper program that the decoy asks for and the master does not list, so it appears in no list of the node and
stops when the decoy is another or none. The account of AdGuard Home gets a password that nobody is given,
since nobody is to sign in to a decoy, and its own login page is what a visitor who tries finds. The
listener is not started until AdGuard Home runs and is stopped when it does not, for the same reason, and
while it installs, which takes a few minutes, the state of the decoy is `starting` and says what it waits
for; if AdGuard Home cannot be installed it is `failed`, with the reason.

### Geo files

The core refuses a config that reads a geo file it does not have, and the files of the
template's `geodata` section count: it looks for each one before it downloads anything.
So before a push the sync compares what the rendered config reads with `GET /v1/geo`: the
database of every `geoip:`, `geosite:`, `ext:`, `ext-ip:`, `ext-domain:` and `ext-site:`
token, read with the parser of the panel's own token validator, and every
`geodata.assets[].file`. It sends the files the agent lacks and the master holds. The
upload runs in the background, gzipped, sixteen at a time and within 15 minutes each, so
the tick that waits for every node is not held by a slow link; the config goes out on the
first tick after the files are there, as the core would refuse it before. An upload that
fails is tried again after 15 seconds, then after twice as long each time, up to ten
minutes. The files are also looked at while the agent's core is not running at the
revision the master wants, which is what a core that went down on a missing file looks
like. A file the agent already has is never replaced. Keeping them current is the
template's `geodata` section (Geodata Auto-Update on the dashboard): it is rendered into
every agent's config like the rest of the template, so each core fetches its updates on
the schedule set there. A file the master does not have either, or a name that is no plain
`*.dat` name (the master never writes anything else on a node), is left out with a
warning, the push goes ahead and the agent's refusal shows on the node. An agent from
before the endpoint answers 404 and is pushed as it always was.

## Pairing and transport security

The master creates the bundle when the admin adds an agent node. It contains the
node address and port, a random 256-bit secret and a TLS key pair. The admin runs one
command on the node:

```
bash <(curl -fsSL https://raw.githubusercontent.com/kuzzrus/3x-ui-awg/main/install-agent.sh)
```

The installer asks for the bundle at a silent prompt. The bundle holds the agent's
private key and secret, so it is never a command-line argument (argv is world-readable
through `/proc` and ends up in shell history); unattended installs read it from a file
(`--bundle-file <path>`, or `-` for stdin). The installer stores it root-only (`0600`).

- **Pinned certificate, no trust on first use.** The master generates the agent's
  self-signed certificate and key, stores only the SHA-256 fingerprint in
  `Node.PinnedCertSha256` (the field and hex form stock `pin` nodes use), and puts the
  key and certificate in the bundle. The private key is not retained.
- **SNI gate.** The agent only completes a TLS handshake for one server name,
  derived from the secret (`HKDF-SHA256(secret, info="x-ui-agent sni v1")`, rendered
  as an ordinary-looking hostname). Any other SNI is aborted before a certificate is
  produced, so a scanner gets a handshake failure and never sees the certificate. The
  derived name is visible on the wire (ClientHello), so it is a filter, not a secret:
  the bearer secret is what authenticates. The comparison is constant time.
- **The master sets the SNI itself.** The stock pin client leaves `ServerName` unset, and
  crypto/tls sends no SNI at all when `Node.Address` is an IP, which the gate would
  reject. Agents therefore use their own client config (`agentproto.ClientTLSConfig`:
  derived `ServerName`, TLS 1.3, pinned fingerprint) and never `tlsConfigForNode`.
- **Bearer secret** stored encrypted on the master with the existing `nodetoken`
  machinery (`Node.ApiToken`), compared in constant time on the agent.
- The bundle is a credential: it is shown once and re-pairing mints a new secret.
  `POST /panel/api/nodes/addAgent` takes a name, an address and a port, mints the secret
  and the certificate, stores the secret and the pin, marks the node dirty and returns
  the bundle. It does not probe: the agent is not installed yet, so the node reads
  offline until it is. `POST /panel/api/nodes/repairAgent/:id` mints a new pair for an
  agent that was reinstalled or whose bundle was lost; the old agent is cut off at once.
- An agent node's secret, pin and transport come with its bundle, so the generic node
  update changes only its name, remark, endpoint and enable flag, and rejects a secret.
- The agent trusts the master completely, like SSH: it applies whatever config it is
  sent. A compromised master means compromised agents, exactly as with stock nodes
  and their API tokens.

## Pull mode

Push mode needs a port of the node that the master can reach. In pull mode the agent dials the master, which
sends its requests back over that connection, so the node opens no port for the master. It suits a node behind a
NAT, and a node whose only public port is the 443 of its core, with the [decoy](#decoy-site) behind its REALITY
fallback: the node shows that one port to the world, and the master is reached on its own 443 the way a browser
reaches the panel. A node chooses its way when it is added and changes it only by being paired again
(`Node.Pull`, which a save never sets).

- **Connection.** The agent opens a WebSocket to `wss://<master>/<base path>/agent/connect/<node id>`, naming the
  subprotocol `x-ui-agent.v1` and sending `Authorization: Bearer <secret>`. The address is the one the panel is served
  on, so a master behind a reverse proxy, or one whose proxy is the fallback of a REALITY inbound on 443, is reached as
  it is by a browser. The endpoint asks for no session: the secret is all of its authentication. A request that
  does not carry the secret of an enabled agent node in pull mode, that names another node, or that does not name the
  subprotocol, is answered with the bare 404 that the agent gives.
- **The roles turn.** Over that WebSocket the master speaks HTTP/2 to the agent: the agent serves the `/v1/*` handlers it
  serves in push mode (an `http.Server` that speaks HTTP/2 without TLS, with the same handler), and the master's client
  sends its requests through the connection (an `http.ClientConn`) in place of dialing. Every call of the protocol, its
  revisions and its 404 for what the agent does not know are the same in both modes, and only where the connection comes
  from differs. A message of the WebSocket carries at most 32 KiB, and the two ends join them again.
- **Trust.** No TLS runs inside the connection. The outer one tells the agent who the master is: the chain of the
  master's own certificate, or the pin that the bundle carries for a panel whose certificate is not a public one. The
  secret tells the master who the agent is, and every request still carries it, as in push mode. The agent's
  certificate and the SNI gate have nothing to do here, since nothing listens.
- **Bundle.** A pull bundle is version 2: the address of the master with its base path, the id of the node, the
  secret, and optionally the pin. It holds no certificate and no key. An agent from before pull mode refuses it as a
  version it does not support, and the installer, which always installs the newest agent, prints that the agent opens no
  port in place of asking for one to be opened.
- **One connection per node.** A new connection replaces the old one and closes it, so an agent that dials again after a
  silent loss is not kept out by its own dead connection. The master sends an HTTP/2 PING after 15 s of silence and
  drops the connection when it is not answered in 10 s, and the agent pings every 20 s and drops one that has
  been silent for more than 70 s, which it checks at each ping. After a loss the agent waits 1 s before it dials, then twice as long after each failure up to
  30 s with some jitter, and starts again from 1 s after a connection that held for 30 s. A node that is deleted, disabled
  or paired again loses its connection at once, and its old agent is answered 404.
- **Master side.** `runtime.AgentTunnels` holds the connection of each node. The client of a node in pull mode is the
  same `AgentClient`, with a transport that picks the connection the node holds at each request, so it keeps working across
  reconnects. A node that holds none reads offline, saying that the agent has not connected. The agent runs on as it
  does with the master out of reach: its core, helper programs and decoy keep serving what they were last given.
- **Pairing.** `POST /panel/api/nodes/addAgent` with `pull=true` takes `masterUrl`, an `https` address with the base
  path of the panel as the node reaches it, and optionally `masterPin`. The node keeps a placeholder port (443) and no pin
  of its own. `POST /panel/api/nodes/repairAgent/:id` takes the same fields and `pull`, which changes the way: pairing a
  node for pull needs `masterUrl`, and pairing it for push needs the `port` of the agent. With no fields at all a node
  that the panel dials is paired as it was, while a node whose agent dials the panel needs `masterUrl` again, since the
  panel keeps no address of itself for it. A connection that was let in before the node was paired again, disabled or
  deleted is not registered: the panel reads the secret once more where it registers the connection.

## Master-side integration

New code lives in new files. The stock files get small hooks guarded by `Kind`:

| Hook                                                          | Change                                                                                                                                                          |
| ------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `database/model` + `db.go`                                    | `Node.Kind` (default `panel`), `Node.AgentStartedAt` and the `agent_counters` table, added by the automatic migration.                                          |
| `runtime.Manager.RuntimeFor`                                  | `kind == agent` returns `AgentRuntime`.                                                                                                                         |
| `AgentRuntime`                                                | Does not import `NodeService` (`service` already imports `runtime`). See below.                                                                                 |
| Agent HTTP client (new file in `runtime`)                     | Built from `agentproto.ClientTLSConfig`: derived SNI, TLS 1.3, pinned fingerprint. Shared by `AgentRuntime` and `Probe`; stock `tls_client.go` is untouched.    |
| `NodeService.Probe`                                           | `kind == agent` heartbeats through `/v1/status` and fills the same `HeartbeatPatch`.                                                                            |
| `NodeHeartbeatJob.probeOne`                                   | Skip the descendants refresh for agents.                                                                                                                        |
| `NodeTrafficSyncJob.syncOne` / `maybePushGlobals`             | Agents go to an agent sync (render, push if dirty, pull stats) and skip the stock snapshot merge and global-traffic push.                                       |
| `NodeService.UpdatePanels`, `GetWebCertFiles`, `node_tree.go` | Not applicable to agents; skip or reject.                                                                                                                       |
| `XrayService`                                                 | Extract the per-inbound rendering loop of `GetXrayConfig` into a helper both the local and the per-node renderer call, so inbound rendering fixes land in both. |
| `xray` hot apply                                              | Done: `xray.ApplyHot` (`internal/xray/hot_apply.go`) is shared by the master and the agent.                                                                     |

`AgentRuntime` has nothing to mark dirty itself. The stock mutation paths for
node-attached inbounds and clients (`inbound.go`, `client_*.go`, `inbound_traffic*.go`)
already call `MarkNodeDirtyTx` inside their own transaction, whatever the node kind,
because the reconcile of stock nodes depends on it; the sync job then pushes the whole
rendered config. So the per-inbound and per-user methods return nil (a per-user push has
no meaning when the whole config is re-rendered), `RestartXray` maps to
`POST /v1/restart`, and the traffic resets are master-side baseline operations. A change
therefore reaches an agent on the next sync tick (about five seconds), as for stock nodes.

The sync job renders the node's config only when it has a reason to, and sends it only
when it differs from what the agent holds:

- A dirty node is rendered on the next tick. The render is deterministic (inbounds and
  clients in id order, and no value that is new with every master process), so its
  revision only changes when the config does.
- Every 30 seconds each agent is compared anyway, because the template, the
  subscriptions and the settings do not mark nodes dirty, and because an agent that was
  reinstalled has lost its config. The agent's revision comes from `GET /v1/stats`.
- A config the agent refuses (422) is not sent again until it changes, or for five
  minutes at most; the node stays dirty and the agent's own error shows on the node. Any
  other failed push is retried after 15 seconds, or at once when a mutation asks for one,
  so a failure that persists costs a render every 15 seconds and not every tick. A push
  is given 30 seconds, like the reconcile of a stock node; the agent finishes what it
  started either way.
- The restart a stock node gets when a client is disabled is skipped for agents: the push
  that drops the client carries the restart policy.

The master template (routing, outbounds, DNS, policy) is the same for every agent in
v1, minus the local-only injections (sidecar bridges, panel egress, node egresses).
An outbound that points at the loopback port of a helper program (Tor, Psiphon) works on an agent
that runs the program, and fails its dial on one that does not, so such an outbound is
limited to the nodes that run it with `nodeTags`. A WARP outbound is an Xray outbound and
needs no helper program. An amneziawg outbound of the template, whose master-side bridge
points at a sidecar an agent does not have, becomes a blackhole with the same tag.

A routing rule can be picked per node. An agent node carries tags (`Node.Tags`: up to 16, each up
to 32 characters, matched without regard to case; a node of the other kind carries none). A rule
with a `nodeTags` list, a panel-only key like `enabled` and `comment`, is in the config of the
agent nodes that carry one of the tags and in no other, the panel's own core included; the key is
stripped from every rendered rule. A rule without `nodeTags`, or with an empty list, is for every
core. The template is refused when a `nodeTags` is not a list of tags, a rule that holds one
anyway is left out of every config, and the internal api rule is never left out. A save of a node
replaces its tags like every other field, and no tags clears them.

An outbound is picked the same way: a `nodeTags` list on an entry of the template's `outbounds` puts
it in the config of the agent nodes that carry one of the tags and in no other, the panel's own core
included, and the key is stripped. The first outbound is the default of every core, so it cannot carry
`nodeTags`, or the cores without them would get another default: to send the other traffic of some
nodes through a node-only outbound, give those nodes a rule that matches everything, with their tags.
The panel's own core needs outbounds too, for the panel outbound, the outbound a node is reached
through, the egress of an inbound's bridge and the download of the geo files, so the template is
refused when it limits one of those to agent nodes, or leaves a balancer that one of them uses with
node-only outbounds only, and the pickers of those features leave both out.

What the tags leave dangling is repaired and never refused: an agent is kept in step by the config it
is sent, and a client is removed by being left out of it, so a config that is not rendered keeps
serving what was revoked. Xray 26.9.9 loads a rule that names no outbound and drops its traffic, a
balancer left without outbounds sends its traffic out of the default outbound, and so does a balancer
whose members are all down and that has no fallback. So, in a config that lacks an outbound the tags
took away, the rules that send to it, or to a balancer it emptied, send to a blackhole tagged
`x-ui-missing-outbound`, which does not depend on the version of the core, a fallback to it is that
blackhole too, and the last error of the node says what was changed. The panel's own core is repaired
the same way and logs it. The bridges of the panel are not injected towards a balancer without outbounds.
An outbound chained through a missing one by `sockopt.dialerProxy` fails its dials by itself, so it is
only reported. The template is refused when a rule or an outbound for every core, an empty
`nodeTags` included, sends to, or is chained through, a node-only outbound or a balancer of those
only, since the panel's own core would lack it. A rule that names an outbound the template does not hold at all is left as it was, for
templates in the field are full of them.

## Traffic accounting

The agent keeps no history. Each poll the master reads cumulative counters together with
the `xrayStartedAt` of the core that produced them, and compares them with the persisted
baseline for that node and counter (`agent_counters`: one row per user email and per inbound
tag).

- Same `xrayStartedAt`: the delta is `counter - baseline`, and a counter with no baseline
  yet is new, so it counts whole.
- A different `xrayStartedAt` is a new core whatever the counters say. Counters restart
  from zero, so the whole new counter is the delta. A restart is not always visible as a
  drop: a busy client can pass its old baseline within one poll, so a rule that waits
  for a drop would under-count.
- The same `xrayStartedAt` with a counter below its baseline cannot happen (the agent
  never resets counters) and is treated as a reset too.

Either way the delta goes into the client totals through the same statement local traffic
uses (and into the inbound totals of the node's inbounds), and the baselines are replaced,
all in one transaction, so a crash can neither count a delta twice nor lose it. When a new
core starts, the baselines of the old one are dropped first. A restart loses at most the traffic since the last
poll (about five seconds). While the master is down the counters keep growing in the
agent's Xray, so nothing is lost.

The baselines are the agent's own table and not `node_client_traffics`, whose rows the
stock resets delete: resetting a client's usage on the master zeroes its totals and leaves
what was accounted of the counter alone, so the agent's whole counter does not count again.

Depletion and expiry are enforced by the master: the traffic sync job runs the usual check
after every tick, a client that passed its quota is switched off and its node marked
dirty, and it disappears from the next rendered config. The same goes for the traffic
quota and the expiry of an inbound of an agent node, which has no panel to enforce them
(a panel node still does it itself), and the renewal of a client on a plan that renews
(a client that lives only on agent nodes included): the renewed client is back in the next
push. Neither the stock global-traffic
push to nodes nor a restart on disable is used for agents: the push carries the restart
policy.

## Failure behavior

- Master unreachable: the agent keeps serving its last good config. In pull mode it also dials again, with a growing
  wait, until the master is back.
- Agent unreachable: the node goes `offline` through the normal heartbeat; mutations
  keep the node dirty and the next successful tick pushes the final state.
- Config rejected by the core: 422 from `PUT /v1/config`, last good config kept, the
  node stays dirty and the error is shown on the node.
- Master restart: the rendered config is deterministic and sent byte for byte, so its
  revision is unchanged and the push is a no-op on the agent.

## Install and operate

On the node, as root, on a systemd host, with the pairing bundle the master shows:

```
bash <(curl -fsSL https://raw.githubusercontent.com/kuzzrus/3x-ui-awg/main/install-agent.sh)
```

The installer asks for the bundle, checks it before it touches anything, downloads the
release archive for the node's CPU (verifying its SHA-256), installs the agent and the
Xray core, and starts `x-ui-agent.service`. Options: `--bundle-file`, `--version <tag>`
(`dev-latest` follows the per-commit builds), `--tarball <path>` for a node that cannot
reach GitHub, and `--uninstall` (`--yes` skips the question). While the latest stable
release predates the agent, a run without `--version` installs `dev-latest` instead and
says so; a tag or archive given explicitly is never swapped.

| What                                                      | Where                                              |
| --------------------------------------------------------- | -------------------------------------------------- |
| Agent binary                                              | `/usr/local/x-ui-agent/x-ui-agent`                 |
| Xray core and geo files                                   | `/usr/local/x-ui-agent/bin/`                       |
| Pairing bundle (`0600`), agent identity, last good config | `/etc/x-ui-agent/`                                 |
| Agent log and the core's access and error logs            | `/var/log/x-ui-agent/`, `journalctl -u x-ui-agent` |

- Open the agent's TCP port, printed at the end of the install, in the host firewall. It
  is the only port the master needs.
- The Nodes page shows the log of an agent (`GET /v1/logs`) and restarts its core
  (`POST /v1/restart`) from the actions of its row, so a node that misbehaves can be looked
  at and kicked without a shell on it.
- The master can start the update from the Nodes page, like a panel node's: the agent runs
  the installer again, on the latest release or on the dev channel as the dialog says. It
  needs systemd-run and an agent installed by `install-agent.sh`, answers 409 otherwise, and
  comes back on the new version after a short restart. The master logs how the run ended,
  and `journalctl -u 'x-ui-agent-update-*'` on the node has the installer's own output. An
  agent from before the endpoint is told to be too old, and needs the installer run on it once.
- To update by hand, run the installer again without a bundle: the bundle, the identity and the
  last good config stay, and the agent comes back up on that config.
- A reboot starts the agent and its core from the last good config without the master.
- `x-ui-agent -check-bundle -bundle-file <path>` validates a bundle and prints the
  address it names, or `pull` and the address of the master for a bundle of an agent that dials it.

## Phasing

1. **Protocol package** (`internal/agentproto`): request/response types, bundle
   encoding, SNI derivation, token helpers. Pure Go. Done.
2. **Agent library and binary** (`internal/agent`, `cmd/x-ui-agent`): TLS server with
   the SNI gate and auth, `/v1/*` handlers, Xray runner (validate, hot/restart,
   rollback, supervision), stats and status. Release build of the binary. Done: the
   release archive carries `x-ui-agent` next to `x-ui`.
3. **Installer** (`install-agent.sh`, systemd unit) and operator docs. Done.
4. **Master core**: `Node.Kind` and migration, `AgentRuntime`, per-node rendering,
   agent sync (heartbeat, push, stats), accounting. Done.
5. **UI**: agent kind in the add-node flow, bundle display, node list. Done: the Add
   agent button, the pairing bundle shown once with the install steps, an Agent tag and
   Pair again on the node list. The heartbeat is the live connection check.
6. **Live verification** on a real host. Done with a throwaway master and an installed
   agent: add, install, heartbeat, config push and hot apply, accounting through agent and
   master restarts and resets, quota depletion, pairing again, deletion.
7. **Geo files**. Done: the master sends an agent the geo files its config reads when
   it lacks them, and the template's `geodata` section keeps them current.
8. **Self-update**. Done: the Nodes page updates an agent like a panel node, through
   `POST /v1/update`.
9. **Management**. Done: the Nodes page shows an agent's log (`GET /v1/logs`) and restarts its
   core (`POST /v1/restart`).
10. **Per-node routing**. Done: tags on an agent node, `nodeTags` on a routing rule and on an
    outbound.
11. **Helper programs**. Done: the Tor client of an agent node, set with `PUT /v1/sidecars`.
12. **Psiphon**. Done: the second helper program, beside Tor.
13. **Decoy site**. Done: a decoy page or proxy on an agent node, set with `PUT /v1/decoy`.
14. **AdGuard Home as a decoy**. Done: a mode of the decoy, which the agent installs and runs for it.
15. **Pull mode**. Done: an agent that dials the master on the port the panel's reverse proxy serves (443 behind
    a REALITY fallback), so a node needs no port of its own besides its core's.
16. Later: nodes as members of a subscription balancer (its members are picked inbound by inbound today).

## Open questions

- Whether the stock node-tree views should show agents under their own heading.
