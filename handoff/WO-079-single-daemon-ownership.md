# WO-079 — One local daemon owns SQLite and the swarm across browsers/profiles

| | |
|---|---|
| **Addressee** | Architect / Sr Dev |
| **Status** | **Implemented 2026-08-11 — automated Linux tests + Windows cross-build pass; Windows live QA pending** |
| **Date** | 2026-08-11 |
| **Source** | Architecture review, 2026-08-11 |

## Problem

The native host opens SQLite and starts libp2p for the lifetime of its stdio
connection. Keel registers that host with Chrome-family browsers and Firefox.
Several browser profiles or browsers can therefore start independent processes
against one database, each with its own swarm identity, cache, timers and
contribution policy.

SQLite WAL avoids some write corruption; it does not make several independently
configured network owners one coherent daemon.

## Selected topology

Use one long-lived owner process per OS user. Keep one installable binary if
that simplifies packaging, but give it two explicit runtime modes:

- **Native-host proxy:** one short-lived process per browser native-messaging
  connection. It owns stdin/stdout framing only, authenticates to local IPC,
  and copies envelopes bidirectionally. It never opens SQLite or starts libp2p.
- **Daemon owner:** owns the Store, SwarmRuntime, contribution supervisor,
  background timers and all peer networking. It accepts concurrent proxy
  sessions and survives any one browser or proxy disconnect.

Use a Unix-domain socket on Linux/macOS and a named pipe on Windows. The endpoint
is current-user only (`0600` socket inside a private parent directory, or a pipe
ACL restricted to the current SID); do not use TCP, including loopback. Add a
random installation secret stored with the same current-user protection and
require it in the first proxy/owner frame. OS ACL and secret are defense in
depth; either failure rejects the client.

Owner election is exclusive endpoint ownership:

1. Proxy attempts to connect.
2. If absent, it starts an owner candidate and retries with bounded backoff.
3. Exactly one candidate wins exclusive socket/first-pipe-instance creation;
   losers connect to the winner and exit owner mode.
4. A Unix socket is stale only after connect fails and the contender has won an
   exclusive election guard; only then may it unlink the exact endpoint.

Do not use SQLite, `busy_timeout`, or an ordinary stale lock file as the owner
authority.

## Session and lifecycle contract

- The owner multiplexes each proxy as an independent bridge session over shared
  Store/SwarmRuntime instances. Correlation ids are scoped to one session; an id
  from browser A can never resolve browser B's pending request.
- Store mutations are serialized by the existing Store boundary. Contribution
  state is global and a successful change is broadcast as status to all clients.
- Client EOF cancels only that session's requests. It does not stop the owner,
  database, live relay, pre-walk or provider duties.
- Connect-or-spawn is the recovery path. A later packaging order may register
  user-session startup, but correct networking cannot depend on a browser tab.
- `uninstall` and an explicit owner shutdown command stop cleanly after clients
  drain. A proxy must never kill an unknown/incompatible owner.
- Proxy/owner starts with required `owner_ipc:1` negotiation (WO-081). An upgrade
  uses a version-negotiated controlled owner restart; incompatible versions show
  an update-required result instead of creating a second owner.

## Required implementation slices

1. Extract the current bridge request loop so it can run against one IPC client
   without package-global Store/Swarm ownership.
2. Add owner listener/election/authentication and concurrent session management.
3. Change registered native-host mode to proxy-only and preserve native framing
   and browser manifests.
4. Move scheduled network work and WO-077's runtime supervisor into the owner.
5. Update install/uninstall and `daemon/README.md` for endpoint/secret lifecycle,
   clean shutdown and diagnostics.

## Do not

- Do not rely on SQLite's busy timeout as process coordination.
- Do not leave each browser/profile to become its own swarm node.
- Do not add a remotely reachable local API.

## Acceptance

- [x] Two supported browsers using the same user data directory result in one
      effective SQLite/swarm owner.
- [x] Contribution-policy changes apply once and are broadcast to every client.
- [x] Owner crash/restart and Unix stale-owner recovery are tested.
- [x] Ten simultaneous contenders elect exactly one owner; ten concurrent sessions complete a
      correlated request and no response crosses sessions.
- [x] A wrong secret, broad Unix credential permissions and incompatible
      `owner_ipc` revision are rejected; Unix socket modes and Windows
      current-SID DACL construction are enforced. The proxy never opens Store
      or swarm resources.
- [x] Closing every browser leaves the owner running;
      explicit shutdown drains and closes them once.
- [x] Native-messaging framing and the no-browser-persistence rule remain
      unchanged.

## Challenge

Keep transport and election behind small OS-specific adapters so Unix socket
behavior is not assumed on Windows. Firefox remains an ordinary proxy client;
it receives no separate ownership path.

## Engineer completion notes — 2026-08-11

- Default browser launch now runs `runProxy`; only `keel-host owner run` opens
  Store and starts the swarm. Connect-or-spawn starts the detached owner and
  losing candidates exit before touching either resource.
- Unix uses an inode-checked socket cleanup plus a lifetime kernel election
  guard. Windows uses `FILE_FLAG_FIRST_PIPE_INSTANCE` and a protected DACL for
  the current SID. No TCP listener exists.
- The proxy/owner first frame authenticates the 256-bit install secret and
  requires `owner_ipc:1`. Application bridge frames are then forwarded without
  decoding or rewriting.
- `owner status`, `owner stop`, install credential creation, uninstall shutdown
  and credential removal are wired. Browser EOF closes one session only.
- Tests: concurrent credential creation, permission rejection, handshake auth/
  revision failure, ten-contender election, stale Unix recovery, ten-session
  correlation isolation, shared Store visibility, client-EOF survival and
  authenticated shutdown. The full `go test -race ./...` suite passes; Windows
  `go test -c` cross-compiles.
- WO-077's node-replacement supervisor now runs inside the owner. After a
  terminal contribution transition, the owner emits one `CONTRIBUTION_STATUS`
  event with an owner-event id to every authenticated session; the correlated
  requester result remains separate. A two-session integration test proves the
  new effective policy reaches both browsers without crossing request ids.
