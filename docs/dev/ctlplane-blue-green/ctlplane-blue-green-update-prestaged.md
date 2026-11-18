# RHOSO Control Plane Blue/Green Update with Rollback (pre-staged variant, follow-up)

> **SUPER DRAFT, not reviewed.** These are collected ideas for a possible follow-up, not a proposal. Parts of it are **out of date** relative to the [simple variant](ctlplane-blue-green-update.md), which got an independent review and several corrections: the floating dnsmasq/public IPs instead of a DNS VIP override, gateway fencing via `nicMappings: {}`, the EDPM OVN client reset, stopping all OpenStack services on A, and InstanceHA disabled before the backup. Where the two docs disagree, the simple variant wins.

> **Start with the [simple variant](ctlplane-blue-green-update.md).** It freezes A at backup time and needs no new data-path features. This doc covers the follow-up idea: keep B restored and updated **outside** the maintenance window, so the window only holds the final data sync (variant P, 4.2).
>
> **Main risk:** during prepare (days), B runs a full ctlplane with **stale prod data and prod credentials** while prod keeps changing. Any autonomous action on B is taken on wrong state: storage cleanup, share-server cleanup, gateway takeover, InstanceHA fencing (5.3). That can destroy production workloads. Only consider this variant once the **standby mode** (7.1), network/credential isolation and the fence acceptance test exist and are proven. Use it when the simple variant's window is too long for an environment.

Status: super draft (unreviewed ideas)
Scope: control plane (ctlplane) update and rollback across two OCP clusters, without reconfiguring EDPM nodes.
Builds on: [OpenStack Backup and Restore User Guide](https://github.com/openstack-k8s-operators/dev-docs/blob/main/backup-restore/user-guide.md) (referred to below as "B/R guide", step numbers refer to it).

## 1. Idea

- Two OCP clusters, **A** (active) and **B** (standby), with a stable endpoint layer in front.
- Back up A, restore it to B (different IPs), update B to the new version, and test it with dedicated **test EDPM nodes**.
- Cutover: switch the endpoint layer to B. EDPM config does not change. At most a few EDPM services need a restart if they don't reconnect cleanly.
- Rollback: switch back to A.

This turns a forward-only in-place update into a reversible traffic switch. The same mechanism also covers OCP upgrades, cluster rebuilds and hardware refreshes.

## 2. Prerequisites and assumptions

| # | Prerequisite | Why |
|---|---|---|
| P1 | **Pre-provisioned EDPM nodes only** (`preProvisioned: true`) | B only takes over the existing nodes; there is no provisioning |
| P2 | **OADP restore per the B/R guide**, with extra **Velero resource modifier** rules for cluster-specific values (LB IPs etc.) | Reuses the validated restore path, including the staged `infrastructure-only` deployment and OVN DB backup/restore (B/R guide backup step 3, restore steps 8 and 10) |
| P3 | **Each OCP cluster has its own MetalLB and NAD IP pools** (disjoint, on the same L2 networks) | A and B run at the same time with no address conflicts. NADs and MetalLB IPs are cluster-local, never restored as-is |
| P4 | **Reverse-direction connections (ctlplane → EDPM) are tested and allowed in the EDPM firewall** for B's source CIDRs, pre-staged from A | Ansible SSH (`edpm_sshd_allowed_ranges`), novncproxy → VNC, telemetry scraping. The pre-staging is a one-time EDPM config change |
| P5 | **Custom public FQDNs** for all public endpoints (not `*.apps.<cluster>` route hosts) | Otherwise Routes restored on B get B's apps domain and the keystone catalog changes. Builds on the custom public hostname/cert work (RHOSRFE-553) |
| P6 | Same namespace, and **B installed at the same operator version N as A** | B/R guide limitation: operator versions must match. B is updated after the restore, so the real update path gets exercised |
| P7 | A fully updated (no partial update state) | B/R guide limitation |
| P8 | OVN version skew (fully updated N+1 ctlplane with N ovn-controller on prod EDPM) **confirmed with the networking team** | See 5.6. Open item, and a blocker until confirmed |

## 3. Key finding: EDPM only needs one stable IP, the DNS server

Everything EDPM nodes connect to is addressed by **hostname**, resolved by the ctlplane **dnsmasq** (DNSData records):

| EDPM → ctlplane | Address used on EDPM |
|---|---|
| Keystone catalog internal endpoints (nova-compute, neutron agents, ceilometer, metadata) | `*-internal.openstack.svc` hostnames |
| RabbitMQ transport_url | `rabbitmq*.openstack.svc` hostnames |
| OVN SB/NB (ovn-controller, ovn-metadata-agent, ovn-bgp-agent) | `ovsdbserver-sb.openstack.svc` (`ovncontroller-config` → `ovn-remote`, from `OVNDBCluster.Status.DBAddress`). It resolves to **per-pod NAD IPs** via DNSData (`ovn-operator/internal/ovndbcluster/dnsdata.go`) |
| **DNS server itself** | **IP literal**: `ctlplane_dns_nameservers` / NodeSet `Status.DNSClusterAddresses` (dnsmasq LB IP) |

Consequences:
- If the **dnsmasq address** prod EDPM uses is stable and served by whichever cluster is active, then B's dnsmasq answers with **B's own IPs** (B's MetalLB VIPs and B's OVN pod NAD IPs), and EDPM connects **directly to B**. No LB in the RPC/OVSDB data path: no extra hop, no SNAT, no long-lived-connection proxying, and no per-raft-member VIPs for OVN.
- Credentials and trust are already identical after restore: CA secrets are restored (leaf certs are reissued from the same CA), user secrets are restored (service passwords, fernet keys), and RabbitMQ user secrets are restored (infra-operator labels them `restore: "true"`, `rabbitmquser_controller.go:361`).
- Test EDPM nodes on B use B's dnsmasq directly. They get the same hostnames and certs, so they exercise exactly what prod EDPM will see after cutover.

### 3.1 Endpoint layer

| Traffic | Mechanism |
|---|---|
| Public API (users, external clients) | External LB (L4 passthrough, backends = A's or B's public MetalLB IPs) **or** an external DNS record switch for the custom FQDNs (P5) |
| EDPM DNS (UDP/TCP 53) | Stable **DNS VIP**: on the external LB (backends = A's or B's dnsmasq) or a dedicated floating IP. Prod EDPM `ctlplane_dns_nameservers` points to it (one-time pre-stage, like P4) |
| Everything else EDPM → ctlplane | Direct to the active cluster's IPs, via DNS |
| Tenant-facing IP literals (designate bind9/unbound, octavia health-manager IPs in amphora config) | Service-specific, see 5.7 |

Operator gap: on B, NodeSet `Status.DNSClusterAddresses` is computed from **B's** dnsmasq service IP. The next dataplane deployment from B would rewrite prod EDPM `resolv.conf` to B's IP, which breaks the stable-IP model and with it any later rollback. We need an **advertised DNS address** override (DNSMasq / NodeSet) so rendered inventories always use the DNS VIP (see 7.2).

The external LB still has value for the **public** side and for the DNS VIP. It just isn't needed in the internal RPC path.

## 4. How it works: phases

Two variants. Both use the same restore, fencing and handover. They differ in what happens inside the maintenance window.

### 4.1 Variant S (simple): freeze A at backup time
Freeze A, back up, restore to B, update B, test, switch. No final sync, no new operator features for the data path. The **freeze window covers the whole restore + update + test**, typically hours. Useful for the first PoC and for small environments.

### 4.2 Variant P (pre-staged, recommended): only the final data sync in the window

```
               A (active, N)                          B (standby)
Pre-stage      EDPM: DNS VIP, firewall/SSH ranges for B (P4), via normal A deployment
               A NetConfig: exclude the IP range reserved for B's test EDPM nodes
-- no maintenance ------------------------------------------------------------------------------
Prepare        serving; CONFIG FREEZE on A           restore at N from a live backup (fenced),
               (no ctlplane CR / NodeSet / user-       hand over test NodeSet → update ctlplane
               secret changes from here on)            to N+1 (+ OVN on test nodes) → test
                                                      B now has its final config: secrets, certs,
                                                      NADs, LB IPs, operators and services at N+1
-- maintenance window --------------------------------------------------------------------------
Freeze         API read-only, drain in-flight tasks,
               final Galera dumps + OVN DB backup,
               config freeze check, copy keystone secret
Final sync                                            services stopped (stay fenced); DROP the service DBs,
                                                      import the Galera dumps (4.3); restore the
                                                      OVN DBs; apply A's keystone keys; re-run db sync
                                                      + cell mapping job, restart nova; start services;
                                                      restore prod NodeSets, no status (5.4)
Switch         stop A rabbit/OVN DBs (resets conns),  DNS VIP (+ public LB) → B
               fence A                                EDPM reconnects, validate, public API still closed
                                                      ── rollback here: VIP → A, unfence A, zero data loss ──
Commit                                                open public API   ◄── point of no return
-- end of maintenance --------------------------------------------------------------------------
Later          A becomes next standby                 EDPM update = first deployment from B (section 6)
```

Freeze window ≈ drain + dump/import (or replication catch-up, 7.3) + db sync + service restart + reconnect. No restore of CRs, no ctlplane update and no image pulls happen in the window. The prepare phase also doubles as the rehearsal: if tests fail, A was never touched.

### 4.3 What the final sync must cover
**Assumptions:** prepare → switch is short (about a week), with a **config freeze on A**: no ctlplane CR/NodeSet/secret changes, no CA rotation, no EDPM nodes added, no EDPM deployments on A. Anything that violates this means redoing prepare. Under the freeze, the final sync is data only:

| State | Handling at final sync |
|---|---|
| Galera data (all DBs, all cells) | Final dumps from A, imported into B (data only, B/R guide mechanics), **or** replication catch-up (4.5) |
| OVN NB/SB | Final `ovsdb-client backup`, restored on B (B/R guide restore step 8). The N snapshot is loaded into an N+1 ovsdb-server, so the schema conversion needs confirming with networking (5.6) |
| Prod NodeSets | Restored (without status) at final sync, right before the switch (5.4) |

**Schema state at import (applies to the dump/import and to the replica seed in 4.5).** B's DBs are at schema N+1 after prepare, and the final data is at N:
- The GaleraBackup dumps are `mysqldump --single-transaction --databases` (all schemas except `mysql`, mariadb-operator `templates/galerabackup/bin/backup_galera`). `restore_galera --content data` only means "not grants". The dump still includes `CREATE DATABASE IF NOT EXISTS`, `DROP TABLE IF EXISTS` + `CREATE TABLE` per table, and the migration version tables (`alembic_version`/`migrate_version`).
- Importing over B's N+1 DBs recreates every table in the dump at N and resets the version tables to N. **Tables that only exist in N+1 are not in the dump and stay behind** with rehearsal data. The N→N+1 db sync then fails ("table already exists") or runs on top of stale leftovers.
- So **`DROP DATABASE` every service DB on B before the import** (services stopped). The import then gives a clean schema N, and the N+1 db sync runs the normal N→N+1 migration, the same path as an in-place update. B's own users keep their grants, because database-level grants survive `DROP DATABASE`.
- The fresh B/R restore doesn't hit this, because the DBs are empty there (the services haven't run db sync yet under `infrastructure-only`).
- Check after db sync: the migration heads/versions on B equal those of the rehearsal (clean N+1). Run each service's status check (e.g. `nova-status upgrade check`).

**Nothing to sync for users and credentials:**
- **DB users**: B's operators create their own MariaDBAccounts, with usernames generated per cluster (`generateUniqueUsername`, mariadb-operator `mariadbdatabase_funcs.go`) and new passwords. The dumps are data only, so B keeps its own users.
- **RabbitMQ users**: restored **from A** during prepare (RabbitMQUser secrets are restore-labelled, infra-operator `rabbitmquser_controller.go:361`), and unchanged under the freeze. They **must** stay identical to A's, because prod EDPM authenticates with them.
- CA and user secrets: unchanged under the freeze.

**Exceptions:**
- **`nova_api.cell_mappings`.** The imported nova_api data carries **A's** cell DB usernames/passwords, which don't exist in B's Galera. Restarting nova (B/R guide restore step 9) works in a fresh restore because the nova-operator runs the cell mapping job *after* the import there. In variant P, B is already fully reconciled. The job only runs when its input hash changes (`nova_controller.go`, hash kept in `Nova.Status.RegisteredCells`), and the import changes nothing. A restart alone would just re-read A's credentials. So: **re-trigger the cell mapping job** (same mechanism as the db-sync trigger in 4.4, or a manual `nova-manage cell_v2 update_cell` with B's credentials), **then** restart nova as in step 9. The cell transport URLs need no change, since the RabbitMQ credentials are identical.
- **Keystone fernet and credential keys** (operator-owned `keystone` secret, not restored by default). Mark the secret for restore with the BackupConfig annotation override: `oc annotate secret keystone backup.openstack.org/restore=true`. Annotations bypass the ownerRef filter in `labelResourceItems`.
  - **Fernet keys**: whether they're worth restoring depends on the backup's age. With a backup from two weeks ago, the keys have rotated many times (`DefaultFernetRotationDays = 1`) and the tokens have expired, so restoring them gains nothing. For the short window between freeze and switch it's worth it: tokens issued by A stay valid on B. In variant P, B rotates on its own during prepare, so **copy A's current `keystone` secret at final sync**.
  - **Credential keys**: independent of the backup's age. Keystone encrypts stored EC2/S3 and TOTP credentials with them, so with B's keys those credentials can't be decrypted. They share the secret with the fernet keys, so in practice **always restore the secret**. Old fernet keys are harmless: the operator rotates them based on the secret's rotation timestamp annotation, which the restore keeps. The same applies to the regular B/R guide, and it's worth documenting there.
  - Verify: keystone-operator adopts the restored secret (ownerRefs stripped by modifier rule 1) and continues rotating.

### 4.4 Operator gaps specific to variant P
- **Stop services on a running, updated ctlplane without deleting it.** Re-applying the `infrastructure-only` deployment-stage annotation on a running ctlplane is the obvious seam, but it was built for initial deployment and restore. Verify whether it scales services down, or extend it.
- **Re-run all update-time data jobs after a data re-seed (generic contract).** Service operators trigger db sync, cell mapping and similar jobs from config/image hashes, not from DB content. Importing N data into an N+1 deployment changes neither, so nothing runs. We need one explicit, service-agnostic trigger (e.g. a ctlplane annotation that invalidates the hashes of every data job), and a **contract for all operators**: every action an update performs on persistent data (db sync, expand/migrate/contract, cell mapping, online data migrations, OVN DB schema conversion) is a **re-runnable, idempotent job** covered by that trigger. Logic that only fires on the version transition (controller code or a one-shot job keyed on the old→new version change) breaks the contract: in variant P it already ran in prepare on rehearsal data and won't run on the final data. **Audit all operators against this contract.** Mechanism: 4.4.1.
- **Copy A's keystone secret** (credential + fernet keys) at final sync (4.3).
- **Config freeze check**: at freeze, diff A's restore-labelled secrets and CRs against the prepare backup, and abort if anything changed.
- Optional: replication instead of dump/import (7.3).

#### 4.4.1 Making data jobs re-run after an import
**Today:** lib-common `job.DoJob` hashes the job's pod template spec (`util.ObjectHash(expectedJob.Spec.Template.Spec)`) and re-runs only if that hash differs from the one stored in the service CR. That's `Status.Hash[DbSyncHash]` in keystone, glance, nova conductor, etc.; for the nova cell mapping job it's `Nova.Status.RegisteredCells`. An import changes no template, so **nothing re-runs**. GaleraRestore only creates a pod, and the import is a manual `oc exec restore_galera`, so mariadb-operator doesn't know an import happened.

**Proposal: the DB announces that its content was replaced.** This is data-driven and automatic, needs no per-update handling, and also fixes the regular B/R flow:
1. **mariadb-operator**: GaleraRestore performs the import itself as a tracked Job (spec: backup timestamp, `content`, optional DB list). It no longer leaves an idle pod for a manual exec. On success it bumps a **restore generation** on the affected `MariaDBDatabase` status (e.g. `status.restoreGeneration`, or the UID of the GaleraRestore). Promoting a replica (4.5) bumps it the same way.
2. **lib-common**: a helper that puts the restore generation of the job's `MariaDBDatabase`(s) into the job's pod template (env var or annotation). The template hash changes, so `DoJob` re-runs the job.
3. **Every service operator** (the contract): use the helper for every data job: db sync, expand/migrate/contract, online data migrations, nova cell mapping (with the **nova_api** DB's generation, so the mapping gets rewritten with B's credentials). Operators already read their `MariaDBDatabase` for the DB host, so they only need to watch its status.
4. **Ordering**: data jobs complete (the existing per-service DBSync conditions, aggregated in the ctlplane Ready condition) before the services are unfenced or started (7.1). Then restart nova as in B/R guide restore step 9.

OVN has no job: the restored N snapshot is converted by ovsdb-server at pod start (B/R guide restore step 8 deletes the pods). **Verify** that the ovn-operator start scripts convert an older-schema file for clustered (raft) DBs.

**Interim / PoC (no code changes):** after the import, clear the stored hashes so the operators re-run the jobs. Patch the service CR statuses (`Status.Hash[DbSyncHash]` per service, `Nova.Status.RegisteredCells`). This is manual, depends on per-service key names, and can race with reconciles, so it's fine for a lab and not for a product.

### 4.5 Option: one-way Galera replication A → B instead of dump/import
With replication, the final sync needs no Galera dump/import at all: B's Galera clusters run as async replicas of A's, and at freeze they only have to **catch up**. The CR restore from prepare stays as it is. Replication replaces only the data part.

**Catch: B's prepare DBs are already migrated to N+1.** A's binlog events are for schema N and can't be applied to them. So replication starts **after** the rehearsal tests, on freshly seeded DBs at N:

```
Prepare      restore (dump-based) → update B to N+1 → test           (as 4.2)
Replica      services on B stopped (fenced); DROP DATABASE all service DBs (4.3);
mode         seed B's Galera from A at N (GaleraBackup dump with GTID position, or mariabackup);
             start async replication per Galera cluster (main + each cell)
Freeze       A frozen → wait until every B replica has applied A's final GTID position
Final sync   stop replication, promote (read_only off) → db sync N→N+1 + cell mapping job
             → restart nova → start services                         (rest as 4.2)
```

Benefits:
- **Shorter window**: catch-up takes seconds instead of dump + transfer + import, which grows with DB size.
- **A consistent cut for free across all Galera clusters**: once A is really frozen, every replica converges to A's final state. No coordinated multi-cluster backup is needed (5.1). The proof is per cluster: B's `gtid_slave_pos` = A's `gtid_binlog_pos`.
- Replica mode can start days before the window, and stays independent of the window.

Requirements / design points (mariadb-operator feature, e.g. `Galera.spec.replication`):
- **A: binlog + GTID** enabled on all Galera nodes (`log_bin`, `log_slave_updates`, `wsrep_gtid_mode`, consistent `wsrep_gtid_domain_id`). With GTIDs consistent across Galera nodes, the replica can fail over to another A node. Enabling this on A means a rolling Galera restart. Do it **before** prepare; it's a one-time prerequisite, not a per-update step.
- **B: one Galera node acts as the replica**, and Galera propagates the changes to the other B nodes (standard "Galera cluster as async replica" pattern). One channel per Galera cluster.
- **Filter out the `mysql` schema** (`replicate_wild_ignore_table=mysql.%`). B keeps its own MariaDBAccounts (4.3). Replicating A's users and grants would overwrite them.
- **Connectivity and TLS**: B → A Galera, cross-cluster on internalapi (A's Galera exposed via a LoadBalancer service or the external LB). B already trusts A's CA, since the CA is restored. Use a dedicated replication user on A with the `REPLICATION SLAVE` privilege only.
- **Read-only replica**: `read_only=ON` on B while in replica mode (services are stopped anyway).
- **Health**: the status exposes replication state, lag and the last error. **Never skip errors.** A broken replica means re-seeding.
- **Promote** = stop the replica, clear the replica config, `read_only=OFF`. No reverse replication: B is at N+1 after db sync. Rollback before commit is still just "switch back to A", which is untouched.
- Seeding reuses the B/R mechanics: GaleraBackup dumps with their GTID position, then `CHANGE MASTER TO ... MASTER_USE_GTID=slave_pos`.

The OVN NB/SB stay snapshot-based. They're small and fast to back up. ovsdb-server active-backup replication would face the same N vs. N+1 schema question and isn't worth it.

### 4.6 Variant P works for any update, including upstream bumps
Variant P doesn't depend on what an update contains. Both variants, and the in-place update, perform the same data conversion: N data run through the N+1 data jobs. Every supported update path has to make that work anyway. P only moves everything else out of the window (CR restore, ctlplane update, image pulls), so **P's window is always ≤ S's window**. The data-job duration (e.g. a large migration from an upstream bump, 5.9) is in both windows.

P is generic as long as the contract in 4.4 holds: all update-time data actions are re-runnable jobs behind one trigger. Then P needs no per-update or per-service handling. Variant S remains the **fallback**: it needs none of the 4.4 features, so it can be used before they exist, or for an update whose operators violate the contract.

## 5. Critical points

### 5.1 The backup is stale at cutover
A keeps taking writes while B is being prepared. In variant P (4.2), B keeps its config, certs and N+1 deployment, and only the data from the rehearsal is replaced by the final sync (4.3). In both variants the cutover needs a write freeze on A. That means an outage of the ctlplane **API** for the freeze window. Workloads keep running: ovn-controller keeps its installed flows while the SB is disconnected, and VMs aren't affected.

Drain before the freeze: online data migrations from the previous update complete (5.9), no instances with `task_state != NULL`, no volumes in `*ing` states, no LBs in `PENDING_*`, no running migrations. Unfinished RPCs are lost (B/R guide: RabbitMQ is recreated fresh).

All Galera clusters (nova_api, cells, service DBs) and the OVN NB need **one consistent cut**. That takes a real freeze, not "low traffic". This ties in with the read-only API quiesce and GaleraBackup coordination work.

### 5.2 Rollback has a point of no return
After B takes writes, rollback to A loses them and leaves orphans: VMs created on B exist in libvirt but not in A's DB, and VMs deleted on B are still ACTIVE in A. So stage the switch: internal/EDPM first with the public API closed, validate, then open the public API as the commit point. EDPM updates happen only after commit: once EDPM is at N+1 it can't go back to an N ctlplane.

### 5.3 B must not affect prod (worst case)

**Threat model.** B runs a full ctlplane with **prod data and prod credentials**, but **without the prod EDPM nodes attached**. From B's point of view, every prod compute, gateway chassis, amphora and storage consumer is dead or out of sync. In variant P (4.2) this lasts for the whole prepare phase, which can be days. After the switch, A is in exactly the same position. Every service that reacts on its own to "everything is down", or that touches anything shared, can damage prod. These are the shared surfaces:

| # | Shared with prod | Reached from B by |
|---|---|---|
| S1 | **Provider/external L2 networks and the BGP fabric** | B's OCP nodes (ctlplane ovn-controller with `nicMappings`), test EDPM nodes |
| S2 | **Storage backends** (Ceph pools, NetApp/arrays, backup targets) | cinder, glance, manila, nova (via test computes), cinder-backup |
| S3 | **Management planes** (BMCs, Octavia lb-mgmt, external DNS backends) | InstanceHA, ironic, octavia worker, designate |
| S4 | **Prod public endpoints** (custom FQDNs resolve to A via LB/public DNS) | Any B service or stored URL using the public interface |
| S5 | **External sinks** (billing/notification consumers, metrics, logging) | ceilometer, notification publishers, usage audits |
| S6 | **Prod EDPM nodes** (reverse paths allowed by P4) | ansible, telemetry scraping, InstanceHA |

**Per-service risk (starting list; every operator needs an audit):**

| Sev | Service | Mechanism (B with prod data, no prod EDPM) | Surface |
|---|---|---|---|
| **Critical** | **OVN gateways (B's ctlplane ovn-controller, test networker nodes)** | Ctlplane `ovn-controller` with `nicMappings` always registers as a gateway chassis (the `enable-chassis-as-gateway` spec field is deprecated and ignored; the script defaults to gateway on, `ovn-operator/templates/ovncontroller/bin/functions`). B's NB/SB contain **all prod routers**. B's chassis become gateway candidates, BFD sees the prod gateways as down, and a B chassis becomes **active for prod router gateways**. It then GARPs prod router/FIP addresses and SNATs on the shared provider network: **north-south traffic hijack**. With BGP, ovn-bgp-agent on a B/test node advertises prod FIP/tenant prefixes: **fabric route hijack**. (Exact neutron rescheduling behaviour to verify in the lab; fence regardless) | S1 |
| **Critical** | **InstanceHA** | Sees the prod computes as down and **fences them via their BMCs** (`fencingSecret`, still valid for pre-provisioned nodes). Prod hypervisors get powered off | S3, S6 |
| **Critical** | cinder-volume / cinder-backup | Startup `init_host` processing of volumes/backups in transitional states (e.g. resuming **deletes**). The RBD deferred-deletion trash purge periodic. All of it on **prod pools** with the backup-time state | S2 |
| **Critical** | Tests touching restored resources | Attaching a restored (prod) volume to a test VM maps a prod RBD image that's **in use by a prod VM**: data corruption. Deleting a restored image or volume deletes prod data. Allocating a FIP in B can hand out an IP prod assigned after the backup: duplicate IP on the provider network | S1, S2 |
| **Critical** | manila-share | `ensure_shares`/driver resync re-applies **backup-time export/access rules** (revoking access granted since). Automatic share-server cleanup deletes servers that B considers unused (the ones created after the backup are unknown to B) | S2 |
| **Critical** | ironic-conductor (tenant baremetal, if deployed) | Power-state sync forces the DB power state onto real machines. Deploy timeouts lead to cleaning, which **wipes disks** of nodes in use | S3 |
| **High** | aodh evaluator/notifier, heat-engine | Alarm actions are **URLs stored in the DB**, typically prod **public** endpoints, so B triggers autoscaling or webhooks **on A**. B evaluates real metrics if B's telemetry scrapes prod EDPM (P4) | S4, S6 |
| **High** | Any B service using the **public** catalog interface | Public FQDNs resolve to **A**, so B acts on prod APIs with prod credentials | S4 |
| **High** | octavia worker / health-manager | No heartbeats, so it starts a failover. Mostly B-internal (B's nova/neutron), **but** if lb-mgmt is reachable from B, the worker can push config to **prod amphorae** (restored CA = valid client certs) | S3 |
| **High** | designate worker/producer | Periodic sync pushes backup-time zones to **external/shared backends** (DNS records revert). Low risk if the backends are B's own bind9 pods | S3 |
| **High** | ceilometer central, cinder volume-usage-audit, other notifications | Polls B's APIs (restored data = all prod resources) and publishes to **external billing/metrics**: **duplicate billing**, false monitoring | S5 |
| Medium | watcher applier/decision-engine | Acts on B's own nova (DB only), but generates noise and actions. Disable | — |
| Medium | dataplane | Any deployment touching prod NodeSets (section 6) | S6 |
| Low | nova, neutron server, placement, keystone, swift | Stale state stays B-internal: nova has no automatic evacuation, B's NB/SB are separate DBs, and swift rings use `.svc` hostnames (`swift-operator/internal/swiftring/funcs.go:85`) so they stay inside B. A keystone LDAP backend is read-only | — |

**Fencing: defense in depth.** No single layer is enough. Software fencing fails on a missed service or a manual mistake, and network fencing fails on a missed path.

1. **Network isolation (independent of OpenStack config, strongest):**
   - B's OCP nodes and the test EDPM nodes are **not** attached to the prod provider/external L2 segments. Use isolated switch segments/VLANs behind the same physnet names, and no BGP peering with the prod fabric (or peering into an isolated VRF).
   - Block B egress to: Ceph mons/OSDs and array management (except a test pool/SVM), BMC networks, Octavia lb-mgmt, external DNS backends, external billing/notification sinks.
   - **Secondary networks (NADs/macvlan) bypass OVN-Kubernetes NetworkPolicy/EgressFirewall.** Use `MultiNetworkPolicy` or switch/firewall ACLs.
   - **Split DNS in B:** public FQDNs resolve to **B's** public IPs inside B's cluster (or egress to A's public VIPs is blocked). B must never reach A's APIs.
2. **Credential isolation (resource modifiers on restore):**
   - Replace the shared-backend secrets in B with test credentials: a cephx user with caps on test pools only, a test SVM, an empty InstanceHA `fencingSecret`, no external sink credentials.
   - Swap in the prod credentials in the final sync (4.3). It's a secret-only change, validated in the closed-public-API stage before commit.
3. **Service fencing (standby mode, 7.1):**
   - Replicas 0 for the Critical/High services above.
   - InstanceHA disabled.
   - `nicMappings: {}` on B's ctlplane ovn-controller (openstack-operator then deletes the OVNController). Test networker nodes on isolated provider segments.
4. **API access discipline:**
   - B's public API is reachable only by testers.
   - Tests run in a **dedicated test project**, created on A before the prepare backup or fresh on B, as a non-admin user, and never touch restored resources.
   - The test project also isolates FIP allocation (a separate test external network/subnet range that prod doesn't use).

**Fence acceptance test, before any OpenStack service starts on B (and on A after switch):**
- From B pods (cluster network **and** every NAD) and from the test nodes: Ceph prod pools, BMCs, lb-mgmt, A's public VIPs and the external sinks are unreachable.
- Prod public FQDNs resolve to B inside B.
- A packet capture on the prod provider segments shows no ARP/GARP/ND from B's nodes or the test nodes. No BGP sessions from B into the prod fabric.
- The standby condition is True and lists every fenced service. InstanceHA is disabled and its fencing secret is empty.

Automate it in the cutover playbook (7.5). The same test runs **against A after the switch**, because A becomes the "prod data without EDPM" cluster at that point.

**Trade-off.** Network and credential isolation means B's storage/provider connectivity at switch differs from what was tested in prepare. Keep that delta to **firewall rules and secrets only**, never OpenStack config, and validate it in the closed-public-API stage (5.2) before commit.

### 5.4 B/R guide deltas: steps 14/15 contradict "don't touch EDPM"
- **NodeSet status isn't restored**, and `OpenStackDataPlaneDeployment` CRs aren't labeled for backup. Only NodeSets (order 60) and Services (order 40) are. After restore, B has no record of what's deployed on prod EDPM. **B doesn't need that record** (see handling below).
- B/R guide **step 15** resyncs that with a configuration deployment (ansible) against all NodeSets. In blue/green we must not do that: on B after the update, it would render N+1 config and images onto prod EDPM. That's an EDPM update that makes rollback impossible.
- What reads NodeSet status before the EDPM update:
  - The **OVN dataplane gate** (`DataplaneNodesetsOVNControllerImagesMatch`, `internal/openstack/dataplane.go:41`) iterates all NodeSets, but skips those with an empty `Status.ContainerImages["OvnControllerImage"]`. Status-less prod NodeSets don't block B's update. With status restored at N, they would.
  - The **final dataplane stage** (`DataplaneNodesetsDeployed`) waits until every NodeSet is Ready at the target version. It stays in progress until the post-commit EDPM update, with or without status.
  - The **greenfield check** only needs B's ctlplane to be deployed.
  - The EDPM update takes its images from `OpenStackVersion`.

Proposed handling:
1. **Restore NodeSets without status.** No `restoreStatus`, no Deployments.
2. In **prepare** (days), restore only the **test** NodeSet (handed over from A, 6.4). **Prod NodeSets are restored only at final sync, right before the switch.** This keeps them off B while it runs with stale data, so a mistaken deployment from B can't reach prod EDPM during prepare. B needs no status, so this late restore doesn't affect the update.
3. **Skip step 15.** The next EDPM deployment from B is the post-commit EDPM update (section 6). Once restored, prod NodeSets look **"not deployed"** on B, which invites exactly that resync. The runbook must forbid it, and the standby mode enforces it later (7.1).
4. EDPM keeps working with A's leaf certs (same CA) until the first deployment from B installs the reissued ones. Check that no leaf cert expires inside the switch → commit → EDPM update window.

Other B/R guide steps:
- **Step 9 (nova restart)**: required as-is. Stale cell DB connections show up exactly as "new instances hang in BUILD".
- **Step 10 (neutron ↔ OVN sync)**: run in `log` mode only. With the OVN NB taken at the same freeze point as the neutron DB there should be no drift. Any drift means **stop and investigate**, not `repair` on a prod switchover. Don't regenerate the OVN DBs: new tunnel keys reprogram every chassis and disrupt traffic.
- **Step 11 (octavia → OVN sync)**: as-is, if octavia is deployed.
- **Step 0 modifiers**: add the blue/green rules (5.5).

### 5.5 Resource modifiers for B (P2, P3)
Extend the B/R guide `openstack-restore-resource-modifiers` ConfigMap with B-specific rules:
- `OpenStackControlPlane`: replace each `metallb.universe.tf/loadBalancerIPs` override with B's IP. JSON patch per service path, generated from an A→B IP map. Better long term: reference pools (`metallb.universe.tf/address-pool`) instead of fixed IPs, so no rewrite is needed (7.2).
- **NADs**: exclude from the restore (B has its own pools, P3), using `backup.openstack.org/restore: "false"` via the BackupConfig annotation override, or `excludedResources`. B's NADs must exist before restore step 4.
- `NetConfig` / `Reservation` / `IPSet`: restore as-is, because prod EDPM IPs must stay reserved. Test EDPM nodes on B allocate from a range that **A's NetConfig excludes** (pre-stage). Otherwise A could later hand the same IP to a new prod node.
- DNSMasq / NodeSet: set the advertised DNS address to the DNS VIP (needs 7.2).
- InstanceHA: keep `disabled: "True"` (already rule 3) and re-enable manually after commit.

Generate the modifiers from a small A→B mapping file. Don't hand-maintain JSON patches per field.

### 5.6 OVN version skew: open with networking
The minor update is staged: `MinorUpdateOVNControlplane` → `MinorUpdateOVNDataplane` → infra → `MinorUpdateControlplane` → `MinorUpdateDataplane`. In B, the OVN dataplane stage is satisfied by the test NodeSets only (5.4). At cutover, prod ovn-controllers on N face a fully updated N+1 ctlplane, a combination the standard update never produces.

Questions for networking:
- Is "N+1 northd/neutron + N ovn-controller" supported per release pair, and for how long?
- If not: cut over after `MinorUpdateOVNControlplane` (the target-stage gate exists), update prod EDPM OVN, then finish B's update. This works but keeps rollback only for the OVN stage.

The same question applies to the nova RPC skew (N+1 conductor / N compute). That one is expected to be fine, since every update runs it.

### 5.7 Remaining IP literals and reverse paths
- **Designate**: bind9/unbound IPs are tenant-visible (NS records, subnet `dns_nameservers`). They need floating IPs or LB VIPs, plus the known MetalLB return-routing caveats.
- **Octavia**: health-manager IPs are baked into the amphora config. Amphorae send heartbeats to A's IPs. Options: same HM IPs on B, or an amphora config update after cutover.
- **Reverse paths (P4)**: ansible SSH, novncproxy → VNC, scraping. Must be tested, and covered by the EDPM nftables/SSH ranges for B's source CIDRs.

### 5.8 Long-lived connections
RabbitMQ and OVSDB sessions stay on A until they break. The switch is therefore: flip the DNS VIP, then stop A's RabbitMQ and OVN DB pods. Clients reconnect, re-resolve, and land on B. Verify:
- No DNS caching on EDPM beyond the dnsmasq TTL.
- oslo.messaging and OVS re-resolve hostnames on reconnect.
- How long agents take to report up.

Keep a "restart-only" dataplane service ready as the fallback for agents that don't recover. It restarts containers without config changes.

### 5.9 Any minor update may carry a new upstream release (RHOSO 19)
In RHOSO 19, a single service can move to a new upstream release inside a minor update. The design treats this as the **general case**, with no per-update or per-service handling: every update may contain major migrations, RPC version changes and config option changes. That's where blue/green adds the most value, since an in-place update of that kind can't be rolled back. The generic consequences:

- **DB migrations become major migrations** for that service: new tables and columns, data migrations, and possibly long-running steps on large tables. The N→N+1 db sync runs **inside the freeze window** (4.2/4.5), and replication doesn't shorten it. **Measure it in prepare.** Prepare runs on a full-size restore of prod data, which is the realistic benchmark. Plan the window around it.
- **Expand/contract** (keystone, neutron, glance): all ctlplane services switch at once, with no rolling mix of old and new API/server pods, so the contract steps can run at final sync. Confirm per service that the EDPM agents still at N (neutron agents, nova-compute) don't touch the DB directly. They use RPC, so this should hold.
- **Online data migrations** (nova, cinder): they run after the db sync, online, and can run after the switch. They **must be complete before the next update.** Nova's db sync refuses to proceed with pending migrations. So the precheck for the **next** blue/green cycle has to include "online data migrations complete on A" before the prepare backup.
- **RPC / service version skew with EDPM:** after the switch, prod EDPM keeps running the **old upstream release** of that service until the post-commit EDPM update (section 6):
  - nova: N+1 conductor/API with N-1 nova-compute is supported upstream (`[upgrade_levels] compute=auto`). But N+1 control services **refuse to start** if the compute service records in the DB are older than the supported minimum. A jump of more than one upstream release is only allowed between SLURP releases, so per minor update check which upstream releases are involved. B starts with the restored compute records from A, so a too-old version fails at service start on B. That's an early signal, already in prepare.
  - neutron: N-1 agents (ovn-metadata-agent, sriov/dhcp agents) with an N+1 server is supported upstream. Neutron also interacts with the OVN version (5.6).
  - Other services: same question per service. The answer goes into the release notes of each RHOSO 19 minor update that carries an upstream bump: "supported skew between ctlplane and EDPM, and for how long".
- **Keep the skew period short.** Supported doesn't mean it should last. Schedule the EDPM update soon after commit.
- **None of this is blue/green-specific.** The skew rules, migration duration and config deprecations apply to the in-place update too. Blue/green only makes them testable in prepare and reversible until commit. What blue/green adds is the re-run contract (4.4) and making the skew statement **machine-checkable**: release metadata per update, verified by the cutover playbook's precheck instead of a manual read of the release notes.
- **Config:** a new upstream release can deprecate or remove options used in `customServiceConfig`. B fails or warns in prepare, so this gets caught before the window. The EDPM-side config for that service is only applied by the EDPM update.
- **Rehearse the skew:** after B's ctlplane update, keep the handed-over **test EDPM nodes at N for a while** (only OVN updated, as the gate requires) and run tests. That's exactly the state prod is in between switch and EDPM update. Then update the test nodes, which rehearses the EDPM update including that service's compute-side changes.

## 6. EDPM handover

The B/R guide finishes with a **configuration resync** (step 15): an `OpenStackDataPlaneDeployment` across all NodeSets that runs their ansible services against the nodes. That's harmless when you restore into the same role at the same version.

Blue/green **hands the pre-provisioned nodes over**: they keep running exactly as A configured them, and B takes ownership without any ansible run until the update. That changes the dataplane part of the restore.

### 6.1 Rule: no EDPM deployment from B except the update
- **No post-restore deployment.** B/R guide step 15 is dropped.
- The **first** `OpenStackDataPlaneDeployment` from B against prod NodeSets is the **EDPM update**: the OVN dataplane update, then the `update` service. It runs **after commit** (5.2).
- Until then, EDPM nodes run the config A deployed. That works because hostnames, CA, credentials and DNS VIP are identical (section 3).
- Enforce this in fenced/`internal` mode (7.1): reject deployments against handed-over NodeSets until the commit.

### 6.2 What B needs to own the nodes without redeploying
| Item | Handling |
|---|---|
| NodeSet spec | Restored (order 60): test NodeSet in prepare, prod NodeSets at final sync right before the switch (5.4) |
| NodeSet status (deployed images, `DeployedVersion`, `DeployedConfigHash`) | **Not restored, and not needed** (5.4). It gets populated by the post-commit EDPM update |
| Deployment history (`OpenStackDataPlaneDeployment`) | Not restored, so old Deployments can't re-trigger |
| IPAM (`IPSet`, `Reservation`) and DNSData for node hostnames | Restored or regenerated by B's NodeSet controller. B's dnsmasq must serve prod node hostnames before switch |
| Ansible SSH key, nova migration SSH key, libvirt secret | User secrets, restored |
| EDPM leaf certs (ovn, libvirt, nova, neutron-metadata) | Reissued on B from the same CA. Nodes keep A's certs (still valid) until the first deployment from B installs the new ones |
| Rendered config secrets (`nova-cell*-compute-config`, neutron/ovn agent configs) | Regenerated by B. Expected to differ only in hashes (same hostnames and creds). Verify there's no semantic drift **before** the update, see 6.3 |
| DNS address in rendered inventory | Must be the DNS VIP, not B's dnsmasq IP (7.2) |

### 6.3 Consequence: the update deployment carries handover drift
The first deployment from B delivers two things at once:
- the version change (N → N+1)
- the handover drift: reissued leaf certs, regenerated config secrets, possibly different ordering or defaults rendered by B

That's more than a normal minor update. Mitigations:
- **Diff before the update.** Render B's per-node config at N (after restore, before B's ctlplane update) and compare it with what A deployed. The diff should contain only cert material and hashes. Possible tooling: an ansible `--check --diff` run, or comparing the rendered secrets of A and B.
- **The update deployment must include cert installation.** Either the update service list includes `install-certs`, or a full deployment follows the update right away. Otherwise B's reissued certs are only installed later, and A's certs must not expire in the meantime.

### 6.4 Test EDPM nodes go through the handover too
To rehearse the prod path, the test NodeSet should **live on A at N**, be part of the backup, and be **handed over to B like prod**:
- restored without status
- no redeploy
- reconnect after switch
- then updated from B

Test nodes created fresh on B only exercise the update, not the handover.

Without status, the OVN gate skips the test NodeSet too, so B's update would pass the OVN dataplane stage without updating OVN on the test nodes. To keep the standard order:
1. Set the target-stage annotation on `OpenStackVersion` so the update pauses after `MinorUpdateOVNControlplane`.
2. Run the OVN deployment on the test NodeSet.
3. Remove the annotation and let the update continue.

Afterwards keep the test nodes at N for the other services for a while. That's the state prod is in after the switch.

### 6.5 Fencing A after switch
Exactly one cluster may act on prod EDPM at any time. After switch, A gets fenced the same way B was before: side-effecting services off, InstanceHA off, no dataplane deployments, and dnsmasq not behind the DNS VIP. Keep A intact until the commit point plus soak time. It is the rollback target. A rollback is the handover in reverse. A's NodeSet status is still valid, because B never deployed to the nodes before commit.

## 6a. B/R guide mapping

| B/R guide step | Blue/green |
|---|---|
| Backup 1–5 (versions, Galera dumps, OVN DB backup, OADP PVC + resources) | As-is, at freeze. OVN DB backup (step 3) is **mandatory**, not optional |
| Restore step 0 (resource modifiers) | Extend with B rules (5.5) |
| Restore 1–5 (PVCs, secrets/CMs, infra CRs, ctlplane infra-only) | As-is, **NADs excluded** (B's own) |
| Restore 6 (GaleraBackup, IPSet, DataPlaneService) | As-is |
| Restore 7–8 (Galera + OVN DB restore) | As-is |
| Restore 9 (resume + nova restart) | As-is, but **stay fenced** (7.1) |
| Restore 10 (neutron ↔ OVN sync) | `log` mode only. Drift means stop |
| Restore 11 (octavia sync) | As-is |
| — (new) | **B ctlplane update to N+1** (test NodeSets only) |
| Restore 14 (dataplane) | Without status, no Deployments. Test NodeSet in prepare; prod NodeSets at final sync, right before the switch |
| Restore 15 (EDPM deployment) | **Drop.** The first deployment is the post-commit EDPM update (6.1) |
| Restore 16 (InstanceHA) | **Only after commit** |
| — (new) | Switch, validate, commit/rollback (section 4) |

## 7. Proposed operator features

### 7.1 Fenced/standby mode (must-have)
One switch, for example `OpenStackControlPlane.spec.standby: true|internal|false`, translated by the operators into:
- Replicas set to 0, or backend disabled, for the Critical/High services in 5.3, and `nicMappings: {}` on the ctlplane ovn-controller.
- A webhook that rejects `OpenStackDataPlaneDeployment` for prod NodeSets (label-based) while fenced.
- InstanceHA forced to disabled.
- A status condition that makes the fenced state visible.
- `internal`: EDPM-facing services up, public API closed, as the pre-commit stage.

The `infrastructure-only` deployment-stage annotation is the existing seam to extend.

### 7.2 Endpoint identity decoupled from cluster IPs (must-have)
- **Advertised DNS address** for DNSMasq/NodeSet, so rendered EDPM inventories use the DNS VIP rather than the local dnsmasq LB IP (section 3).
- Pool-based instead of fixed `loadBalancerIPs` in the ctlplane CR. This removes most modifier rules.

### 7.3 Shorter freeze window
- **Must-have for variant P (4.4):** stop services on a running ctlplane, re-run db sync and the cell mapping job after a data re-seed, copy the keystone keys, and check the config freeze.
- Optional: mariadb-operator Galera replication (4.5). The freeze then becomes catch-up instead of dump + import, with a consistent cut across all Galera clusters. Most valuable for large DBs; measure dump/import time in the prepare phase first.

### 7.4 Dataplane status visibility (nice-to-have)
Not needed for correctness: the update logic works with status-less NodeSets (5.4). The only gap is visibility. Handed-over NodeSets show "not deployed", which can mislead admins or automation into running a resync. Options: restore NodeSet status for display only (it must not block the OVN gate), or mark handed-over NodeSets with a condition or annotation, e.g. "adopted, deployed by previous cluster at version N".

### 7.5 Cutover orchestration
An Ansible playbook next to the B/R playbooks (or a ci-framework role extending `cifmw_backup_restore`) covering: precheck/drain, freeze, backup, restore with modifiers, staged prod NodeSet restore, switch, validate, commit/rollback. Only make it a CR once the steps are stable.

## 8. Not solved
- Dataplane rollback. EDPM updates stay forward-only; the test NodeSets act as canaries.
- Zero API downtime. The freeze window remains.
- Rollback after commit without data loss.
- Cost: a second ctlplane cluster (ping-pong A↔B, but permanent capacity).

## 9. Test items / open questions
1. OVN (and nova) version skew, release-pair statement (P8, 5.6). **Blocker.**
2. B's update with status-less NodeSets: the OVN gate skips them, the dataplane stage waits, and the post-commit EDPM update brings them to Ready at N+1 (5.4).
3. EDPM re-resolution and reconnect behaviour after the DNS VIP switch and A RabbitMQ/OVN shutdown. Agent recovery time (5.8).
4. Reverse-path firewalling from B (P4).
5. Side-effecting periodics audit per operator. 5.3 is a starting list.
6. Octavia HM IPs and designate IPs (5.7).
7. Freeze mechanism: read-only API mode vs. closing the public LB. Agent-reported state (heartbeats, RP inventory, chassis) may be lost because it gets re-reported; user intent must not be.
8. Leaf cert lifetimes vs. the cutover → EDPM update window (5.4).
9. Does the minor-update `update` service re-render all service configs and install certs, or does the handover drift need a full deployment (6.3)?
10. Tooling to diff the config B renders at N against what A deployed (6.3).
11. Per RHOSO 19 minor update with an upstream bump: the supported ctlplane ↔ EDPM skew per service, how long it may last, and the db sync duration on prod-size data (5.9).

## 10. Next steps
1. Lab PoC: two clusters, external LB for public endpoints + DNS VIP, B/R guide restore with the extra modifiers, test NodeSet on B, minor update, switch, rollback. Measure the freeze window and EDPM reconnect behaviour.
2. Validate status-less NodeSet restore, the late prod NodeSet restore, and skipping step 15 (5.4).
2b. **Per-operator re-run contract audit** (4.4): find update-time data actions that are tied to the version transition and can't be re-triggered after a reseed.
2a. **Per-operator side-effect audit** (5.3): startup and periodic actions on shared surfaces S1–S6, verified in the lab (gateway rescheduling, cinder/manila init_host, octavia, designate). This audit becomes the spec for the standby mode and the fence acceptance test.
3. Networking sync on OVN skew (5.6).
4. API design for 7.1, 7.2 and 7.4.
