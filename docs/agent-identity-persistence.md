# Secure Agent identity persistence

## Scope and defaults

Agent identity persistence is optional and disabled by default. It preserves only the controller binding, Agent ID, and per-Agent authentication token across restarts on the same trusted host and intact disk. It never saves an enrollment token, full registration response, labels, or object-storage credentials. The identity token is stored in a private plaintext file; this is not encryption, a secret manager, or a boundary against code running as the Agent's OS user.

All Agent configuration is a startup snapshot. Configuration-file edits, including identity and transport changes, require a process restart. The Agent does not rewrite its configuration file after enrollment.

Enable deliberately in a private configuration file:

```toml
[grpc]
serverAddr = "controller.example.test:9443"
token = ""
registrationToken = "" # Supply a valid enrollment token for the first start only.

[grpc.tls]
enabled = true
caFile = "/etc/arcentra/controller-ca.pem"
serverName = "controller.example.test"

[agent]
id = ""
workspaceDir = "/var/lib/arcentra/workspace"

[agent.identityPersistence]
enabled = true
directory = "/var/lib/arcentra/agent-identity"
```

The directory has no implicit default. It must be absolute, separate from the build workspace in both directions, and exclusive to this Agent. Supported Unix ownership and local locking semantics are required. The directory must be owned by the current effective user with mode 0700; files must be regular, single-link, same-owner files with mode 0600. Existing unsafe files, links, permissions, unknown contents, or ambiguous state are rejected rather than repaired. New directories/files are created with private modes. Keep identity data on trusted durable local storage, outside repository checkout, artifact, and log locations. Do not put a shared identity directory on a distributed filesystem whose locking or sync semantics differ from the host filesystem.

Verified TLS and an explicit trusted CA file are mandatory when persistence is enabled. The controller binding includes the actual client's endpoint, effective TLS server name, and the exact CA bytes loaded at startup. There is no insecure fallback. Endpoint, server-name, or CA-file-content changes intentionally require operator reconciliation, even if a new CA file would trust the same server. Ordinary non-persistent TLS may still use system roots.

## Identity selection

1. Partial explicit configuration, such as only `agent.id` or only `grpc.token`, is rejected before enrollment. Supply both or neither.
2. A complete explicit pair takes precedence over the enrollment token. When a ready identity store exists, its controller binding and Agent ID must match; the explicit token is used for that run without replacing the stored token.
3. A complete explicit pair with an empty store is used for that run only. It is not silently imported or written to the store.
4. Without an explicit pair, a valid ready store restores the original pair before checking for an existing terminal-report journal. The enrollment token can be removed from configuration after successful first enrollment.
5. Without either identity source, a fresh safe workspace and an empty store permit one dynamic enrollment attempt. An existing terminal-report outbox blocks new enrollment.

Pending, corrupt, ambiguous, mismatched-controller, or unsafe stores always fail closed, including when explicit credentials are supplied. Invalid, revoked, or disabled per-Agent credentials never fall back to a registration token and never create a replacement Agent. This stage does not provide a credential-import or automatic token-rotation operation.

## Durability and crash handling

Before the one dynamic registration RPC, the Agent durably writes a token-free pending marker. On a successful, structurally valid response, it durably writes the ready identity before changing runtime identity or starting publishers and workers. The identity directory stays exclusively locked until registration, workers, and cleanup have stopped; a late response cannot write after the lock is released.

Lost or failed RPC responses, malformed responses, and uncertain writes without a safely committed ready identity leave pending or ambiguous state. The next process refuses automatic enrollment. A token-free guard survives until the ready record and directory have been synced. If a crash occurs during the final guard removal, reopening either refuses the remaining guard or restores the already-durable original ready identity; it never treats the store as empty or starts another enrollment. This can require operator recovery even if the controller never committed enrollment: preventing duplicate or replacement identities takes priority over automatic availability.

Do not remove pending files, delete the identity directory, or point at a new directory just to bypass the check. Stop the Agent and preserve both the identity directory and original workspace. Reconcile the possibly-created Agent with the controller using administrative records. Recover the exact original ID and valid per-Agent credential through an authorized operational process; protect credentials from tickets and logs. If the original identity cannot be recovered, separately reconcile unfinished work and terminal reports before provisioning a replacement. There is no automatic recovery/import command in this stage.

Identity persistence does not make command execution exactly once, resume interrupted jobs, transfer terminal receipts to a replacement Agent, or survive disk loss. Existing terminal-report and artifact identity checks remain in force.

## Authentication and readiness

`/health` returns 503 until registration, required identity durability, the first authenticated heartbeat, and configured worker/publisher startup succeed. Publishers and workers are created only after that initial heartbeat. Periodic heartbeat scheduling also starts only after initial authentication. Subsequent heartbeat failures make readiness false until authentication succeeds again; shutdown and cleanup keep it false.

A registration awaiting operator approval can durably save its returned identity, but does not start heartbeat, initialize supplied object-storage clients, publish events, or start workers. After approval, restart the Agent: it authenticates with the saved pair and retrieves current configuration from the controller. Approval denial, token revocation, or disabled status must be resolved by an operator rather than reenrollment.

Registration, heartbeat, and shutdown diagnostics retain safe status codes and fixed descriptions rather than untrusted RPC messages that may echo credentials.

## Controller authorization freshness

Per-Agent authentication reads the current enabled record and HMAC secret from the primary database without the former identity/secret caches. Missing, malformed, or unavailable auto-approval policy leaves a newly enrolled Agent pending approval; the existing migration's explicit auto-approve setting retains its normal meaning. Pending registration never returns object-storage credentials. Disabled/deleted identities or changed HMAC credentials are denied without enrollment fallback.

This software no longer reads or writes the old Redis HMAC-secret cache. It does not purge existing cache data or rotate real credentials during startup. Any cleanup of previously stored cache entries belongs to an explicitly authorized operational change.

## Deployment probes

`/health` remains a readiness alias; use `/ready` for readiness and `/live` for liveness. Pending enrollment or authentication loss must not cause a liveness restart loop. Public HTTP shutdown/debug endpoints are unavailable; use OS signals. See `deployment-readiness.md` for direct controller TLS, immutable identity/CA boundaries and explicit migration prerequisites.
