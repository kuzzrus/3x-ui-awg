# Node agent (`x-ui-agent`)

Design for an optional third kind of node: a thin, DB-less agent that only runs
Xray-core for a master panel. Nothing here is implemented yet; each phase in
[Phasing](#phasing) lands as its own PR and this file is updated as they do.

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
  `nodeEligibleProtocols` already excludes them; agents run Xray-native inbounds only.
- Per-client IP limits on agent nodes.
- Pull mode (agent dials the master) and agent self-update. Both are addable later.

## Decisions

| Question                    | Decision                                                                                                                                                                                 |
| --------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Where does the agent live   | Separate binary `x-ui-agent` (`cmd/x-ui-agent`), same Go module and release tarball. No DB, no web UI, no CGO.                                                                           |
| How the master reaches it   | New `Node.Kind` (`panel` default, `agent`). A third `runtime.Runtime` implementation, `AgentRuntime`. Every caller already goes through the interface.                                   |
| Wire protocol               | Its own small HTTPS protocol (`/v1/*`), not the panel's `panel/api/*` dialect.                                                                                                           |
| Who renders the Xray config | The master. The agent receives a finished config and applies it.                                                                                                                         |
| How changes propagate       | Reuse the stock `ConfigDirty` mechanism: a mutation marks the node dirty, the sync job renders and pushes the whole config. The agent hot-applies when it can and restarts when it must. |
| Traffic                     | Agent reports cumulative counters; the master diffs them against persisted per-node baselines (`node_client_traffics`), as for stock nodes.                                              |
| Installer                   | A separate `install-agent.sh`, not an `--agent` branch in `install.sh`.                                                                                                                  |
| Push or pull                | Push (master dials the agent) in v1.                                                                                                                                                     |

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
   -> runtime.Runtime                                    TLS server (SNI-gated)
        AgentRuntime  --mark node dirty-->                auth (bearer)
 NodeTrafficSyncJob / heartbeat                           /v1/config  apply
   -> render node config      --HTTPS PUT /v1/config-->   /v1/status
   <- /v1/status, /v1/stats   <--------------------        /v1/stats
                                                          xray-core (child process)
                                                            hot-diff via gRPC, or restart
                                                          last-good config on disk
```

Reused as-is from the master: `internal/xray` (`Process`, `XrayAPI`, `ComputeHotDiff`),
`Node` columns for status (`Status`, `LatencyMs`, `CpuPct`, `MemPct`, `XrayVersion`,
`XrayState`, `XrayError`, `Guid`, ...), `NodeService.UpdateHeartbeat`, the node views
and websocket broadcast, `nodetoken` encryption for the stored secret.

## Wire protocol v1

All requests carry `Authorization: Bearer <secret>` and are JSON. Responses are JSON
with plain HTTP status codes (no `{success,msg,obj}` envelope).

| Endpoint           | Purpose                                                                                                                                                                                                                                |
| ------------------ | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `PUT /v1/config`   | Body: `{revision, config, restartOnUserRemoval}`. Validates with `xray run -test`, then applies (no-op, hot, or restart). Answers `{applied: noop\|hot\|restart, xrayState}`; on a bad config answers 422 and keeps the last good one. |
| `GET /v1/status`   | Agent version, hostname, GUID, config revision, Xray version/state/error, CPU, memory, uptime, interface throughput. This is the heartbeat.                                                                                            |
| `GET /v1/stats`    | Cumulative counters since Xray started: per inbound tag and per user email (up/down), online emails, `xrayStartedAt` so the master detects resets exactly instead of guessing from a drop.                                             |
| `POST /v1/restart` | Restart Xray with the last good config.                                                                                                                                                                                                |

Any other path, wrong method, or failed auth answers the same bare 404 so a probe
cannot tell the agent from nothing. Later phases add endpoints (sidecars, geo update,
logs, self-update) under the same prefix.

### Config apply

1. Write the candidate next to the live config and run the core's own config test.
2. Compare with the running config via `ComputeHotDiff`: an empty or API-applicable
   diff is applied through gRPC; anything else restarts the core.
3. If the core fails to come up after a restart, restore the previous config and
   restart again; report the failure in the response and in `GET /v1/status`.
4. The agent forces log paths into its own directory regardless of what the config
   says, and always injects the stats and API sections it depends on.

On boot the agent loads the last good config and starts Xray without waiting for the
master, so a reboot with the master down does not take the node offline.

## Pairing and transport security

The master creates the bundle when the admin adds an agent node. It contains the
node address and port, a random 256-bit secret, a TLS key pair and the master's
expectations for it. The admin runs one command on the node:

```
curl -fsSL <raw install-agent.sh> | bash -s -- --bundle <base64url bundle>
```

- **Pinned certificate, no trust on first use.** The master generates the agent's
  self-signed certificate and key, stores only the SHA-256 fingerprint
  (`Node.PinnedCertSha256`, mode `pin`, the pin machinery that already exists), and
  puts the key and certificate in the bundle. The private key is not retained.
- **SNI gate.** The agent only completes a TLS handshake for one server name,
  derived from the secret (`HKDF-SHA256(secret, info="x-ui-agent-sni-v1")`, rendered
  as an ordinary-looking hostname). Any other SNI is aborted before a certificate is
  produced, so a scanner sees a closed port. The comparison is constant time.
- **Bearer secret** stored encrypted on the master with the existing `nodetoken`
  machinery (`Node.ApiToken`), compared in constant time on the agent.
- The bundle is a credential: the UI shows it once, the installer accepts it from a
  file or stdin as well as the command line, and re-pairing mints a new secret.
- The agent trusts the master completely, like SSH: it applies whatever config it is
  sent. A compromised master means compromised agents, exactly as with stock nodes
  and their API tokens.

## Master-side integration

New code lives in new files. The stock files get small hooks guarded by `Kind`:

| Hook                                                          | Change                                                                                                                                                          |
| ------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `database/model` + `db.go`                                    | `Node.Kind` column (default `panel`) with a migration.                                                                                                          |
| `runtime.Manager.RuntimeFor`                                  | `kind == agent` returns `AgentRuntime`.                                                                                                                         |
| `AgentRuntime`                                                | Mutating methods mark the node dirty (`MarkNodeDirty`) and kick the sync; per-client methods are the same call.                                                 |
| `NodeService.Probe`                                           | `kind == agent` heartbeats through `/v1/status` and fills the same `HeartbeatPatch`.                                                                            |
| `NodeHeartbeatJob.probeOne`                                   | Skip the descendants refresh for agents.                                                                                                                        |
| `NodeTrafficSyncJob.syncOne` / `maybePushGlobals`             | Agents go to an agent sync (render, push if dirty, pull stats) and skip the stock snapshot merge and global-traffic push.                                       |
| `NodeService.UpdatePanels`, `GetWebCertFiles`, `node_tree.go` | Not applicable to agents; skip or reject.                                                                                                                       |
| `XrayService`                                                 | Extract the per-inbound rendering loop of `GetXrayConfig` into a helper both the local and the per-node renderer call, so inbound rendering fixes land in both. |
| `xray` hot apply                                              | Move `tryHotApply` and its `*Reconciling` helpers into `internal/xray` so the agent shares them.                                                                |

The master template (routing, outbounds, DNS, policy) is the same for every agent in
v1, minus the local-only injections (sidecar bridges, panel egress, node egresses).
Outbounds that point at a loopback sidecar port (Tor, WARP) resolve to the agent's own
loopback once sidecars exist on the node, so the shared template keeps working; until
then such a rule fails on an agent that lacks the sidecar. Per-node routing
(`nodeTags` on a rule) is its own phase.

## Traffic accounting

The agent keeps no history. Each poll the master reads cumulative counters, subtracts
the persisted baseline for that node and email, accumulates the delta into the central
totals through the same path local traffic uses, and stores the new baseline. A counter
that went down together with a new `xrayStartedAt` is a reset, so the new counter is the
delta since the restart; a drop with the same `xrayStartedAt` is ignored. The most that
a restart can lose is the traffic since the last poll (about five seconds). While the
master is down the counters keep growing in the agent's Xray, so nothing is lost.

Depletion and expiry are enforced by the master: a disabled client disappears from the
next rendered config. The stock global-traffic push to nodes is not used for agents.

## Failure behavior

- Master unreachable: the agent keeps serving its last good config.
- Agent unreachable: the node goes `offline` through the normal heartbeat; mutations
  keep the node dirty and the next successful tick pushes the final state.
- Config rejected by the core: 422 from `PUT /v1/config`, last good config kept, the
  node stays dirty and the error is shown on the node.
- Master restart: the rendered config is deterministic, so a push of an unchanged config
  is a no-op on the agent.

## Phasing

1. **Protocol package** (`internal/agentproto`): request/response types, bundle
   encoding, SNI derivation, token helpers. Pure Go, inert.
2. **Agent library and binary** (`internal/agent`, `cmd/x-ui-agent`): TLS server with
   the SNI gate and auth, `/v1/*` handlers, Xray runner (validate, hot/restart,
   rollback, supervision), stats and status. Release build of the binary.
3. **Installer** (`install-agent.sh`, systemd unit) and operator docs.
4. **Master core**: `Node.Kind` and migration, `AgentRuntime`, per-node rendering,
   agent sync (heartbeat, push, stats), accounting.
5. **UI**: agent kind in the add-node flow, bundle display, live connection check,
   node list.
6. **Live verification** on real hosts, then fixes.
7. Later, each on its own: outbound sidecars on the node (Tor, WARP, Psiphon) and
   AdGuard Home as the node's decoy, `nodeTags` routing, geo file updates,
   agent self-update, pull mode.

## Open questions

- Whether the stock node-tree views should show agents under their own heading.
- How geo data (`geosite`/`geoip` plus the fork's extra `.dat` files) is kept current
  on an agent before the geo update endpoint exists; v1 ships what the release bundles.
- Whether the agent should also expose `GET /v1/logs` in v1 or leave it for the sidecar
  phase.
