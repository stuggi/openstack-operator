# RHOSO Control Plane Blue/Green Update with Rollback (simple variant)

Status: architecture proposal / discussion draft (rev. after independent review)
Scope: control plane (ctlplane) update with rollback across two OCP clusters, handing over **pre-provisioned** EDPM nodes without reconfiguring them.
Builds on: [OpenStack Backup and Restore User Guide](https://github.com/openstack-k8s-operators/dev-docs/blob/main/backup-restore/user-guide.md) ("B/R guide"; step numbers refer to it).
Follow-up: [pre-staged variant](ctlplane-blue-green-update-prestaged.md). It shortens the maintenance window, but B runs with stale prod data for days. Read that doc for the risks.

> **Review focus:** the most critical part is **section 6, "B must not affect prod"**, especially the **residual risks (6.1)** and how each service is fenced (6.2, 6.3). B holds prod data and prod credentials while prod EDPM is still attached to A. A missed service or a wrong fence can damage production. Input from the service owners on their side effects is the most valuable feedback.

## 1. Idea

- Two OCP clusters: **A** (active, version N) and **B** (standby). B's OCP, operators and networking are **prepared in advance**.
- In the maintenance window:
  1. **Freeze A**: disable InstanceHA, close the public API, fence A's autonomous actors, drain.
  2. Back up A and restore it to B.
  3. Update B to N+1 and test.
  4. Switch the EDPM-facing endpoints to B.
- EDPM nodes are **handed over** without any configuration change. Their control-plane endpoints are hostnames resolved via one DNS IP, and the CA and credentials are identical. The only sanctioned touch before the EDPM update is an OVN client reset (7.2).
- **Rollback** until the commit point: switch back to A, which was frozen and never touched.

### Why freeze A at backup time
- **B's data matches reality.** A is frozen and its autonomous actors are fenced, so the restored DB describes the actual state of volumes, shares, ports, instances and load balancers. In the pre-staged variant, B acts on stale state for days, and that's what makes it destructive.
- **No final sync.** B's data simply *is* prod after the switch. No re-seed, no DB drop/import into an updated ctlplane, no re-run trigger for db sync, no config-freeze week.
- **Works with today's operators** for the data path. The fresh restore runs db sync normally, and the update on B is the regular in-place update path. That holds for any minor update, including upstream release bumps (RHOSO 19).
- Trade-off: the API outage, plus no InstanceHA and no LB failover, covers drain + backup + restore + update + validation (**hours**). Workloads keep running throughout. North-south traffic of centralized routers has a short outage at switch if the gateways run on the ctlplane OCP nodes (4.2).

## 2. Prerequisites

| # | Prerequisite |
|---|---|
| P1 | **Pre-provisioned EDPM nodes only** (`preProvisioned: true`) |
| P2 | OADP backup/restore per the B/R guide, plus extra **Velero resource modifier** and exclusion rules for B (5.2) |
| P3 | **Each OCP cluster has its own MetalLB and NAD IP pools** (disjoint, same L2 networks). **Exception: floating IPs**, the dnsmasq IP (3.1) and, with MetalLB public endpoints, the public VIPs (3.2). They're defined on both clusters and advertised by one at a time |
| P4 | **Reverse-direction connections** (ctlplane → EDPM: ansible SSH, novncproxy → VNC, telemetry scraping, swift-proxy → EDPM swift storage ports if `edpm_swift` is used) tested and allowed in the EDPM firewall for B's source CIDRs. Mitigation for SSH reachability before commit: no deployments against prod NodeSets from B (7) |
| P5 | **Custom public FQDNs** for all public endpoints (otherwise B's routes get B's `*.apps` domain and the keystone catalog changes) |
| P6 | **A closable public endpoint switch** (3.2): freeze must close A's public API immediately and enforceably, and commit opens B's. DNS can't do that. Either floating public VIPs (MetalLB public endpoints, recommended), or an external L4 LB in front of both clusters (Route-based public endpoints) |
| P7 | Same namespace. B's operators installed **at version N** (B/R guide: versions must match), with OLM **`installPlanApproval: Manual`** on A and B so B can't drift before the restore |
| P8 | **B is a cluster-level clone of A** (restored CRs reference these by name): node labels used by Topology/nodeSelectors, StorageClasses, MetalLB pool names, NNCP layout, NMState, cert-manager, COO, CLO/Loki, **image mirror config (IDMS, pull secrets)** (the first EDPM deployment from B renders `registries.conf` from B's cluster), and enough capacity and IP pool size |
| P9 | **External systems that filter by source IP accept B**: NFS exports (cinder/manila/glance), storage array management, HSM (barbican), LDAP, designate secondaries, BMCs |
| P10 | A fully updated, no partial update state. Online data migrations from the previous update complete. No CA renewal due within window + soak |
| P11 | **OVN version skew** confirmed with networking (10.1) |

## 3. Endpoint layer

### 3.1 Control-plane connectivity: hostnames + one floating DNS IP
EDPM reaches the control plane by **hostname**, resolved by the ctlplane **dnsmasq**:
- keystone internal endpoints and metadata (`*-internal.openstack.svc`)
- RabbitMQ (`rabbitmq*.openstack.svc`)
- OVN SB/NB (`ovsdbserver-sb.openstack.svc`, which resolves to per-pod NAD IPs via DNSData)

The DNS server itself is an IP literal (`ctlplane_dns_nameservers`). The operator always sets it from the active dnsmasq service IP (`internal/dataplane/inventory.go`, which overrides NodeSet `ansibleVars`). So:

- **Recommended: reuse A's existing dnsmasq IP as a floating IP.** B's MetalLB has that one IP in a dedicated pool (`autoAssign: false`), and B's dnsmasq service gets the same IP (the resource modifier keeps it unchanged). Only the active cluster **advertises** it: L2Advertisement/BGPAdvertisement for that pool exists on one cluster at a time. EDPM needs **no change**, and later deployments from B render the same IP. No operator feature is needed.
- Alternative: a DNS VIP on an external LB. This needs an **advertised DNS address** override (DNSMasq/NodeSet) that doesn't exist yet. Until then it only works with `ansibleExtraVars` on every deployment from both clusters, plus a `resolv.conf` check on all EDPM nodes before the window.

Everything else then goes **directly to the active cluster's own IPs**, as answered by its dnsmasq. There's no LB in the RPC/OVSDB path.

**Test access during the window:** B's floating IPs aren't advertised until the switch.
- Test nodes (which may be touched) point their `resolv.conf` at an extra, B-only LoadBalancer Service selecting the dnsmasq pods.
- API tests run **inside B**: the `openstackclient` pod or test-operator (tempest) with `OS_INTERFACE=internal` / internal endpoint type, so no public access to B is needed.
- With Route-based public endpoints, testers can also reach B's routes through B's ingress VIP directly (e.g. `--resolve`/hosts entries for the custom FQDNs), restricted by source IP.

### 3.2 Public endpoint switch
The switch must stay up while A or B is down, rebuilt or updated. So it **never runs on A or B**, and a third OCP cluster is too heavy for an L4 switch (only reasonable if one already exists, e.g. an ACM hub, and even then it shares that cluster's lifecycle).

| Public endpoints exposed as | Switch | Notes |
|---|---|---|
| **MetalLB LoadBalancer services** with custom FQDNs (RHOSRFE-553) — **recommended** | **Floating public VIPs**, same approach as dnsmasq: each public VIP sits in a dedicated MetalLB pool on both clusters (`autoAssign: false`), and only one cluster advertises it. Freeze = withdraw A's advertisement. Commit = advertise on B. Rollback = advertise on A | No extra component. Client source IP preserved (no SNAT). All switching reduces to "which cluster advertises the floating pools" |
| **OCP Routes** on the ingress controller (each cluster's ingress VIP is OCP-owned and can't float) | **External L4 LB** in front of both clusters' ingress VIPs, **on the customer's existing LB infrastructure** (F5, NetScaler, A10; HA operated by the network team). Fallback: a HAProxy + keepalived pair on dedicated VMs/hosts outside both clusters. Freeze = disable A's backend. Commit = enable B's | L4 passthrough. The OCP router routes by SNI, and the LB holds no certs. Enable the PROXY protocol on the IngressController if client source IPs matter (keystone audit, rate limits), since an L4 LB usually SNATs |

Below, "**close/open the public endpoint**" means withdrawing/advertising the floating public pool, or disabling/enabling the LB backend.

### 3.3 Other IP literals and trust anchors on EDPM
| Literal | Where | Handling |
|---|---|---|
| **Logging** (EDPM rsyslog → telemetry logging service) | `RsyslogAddress` = the logging Service's `loadBalancerIPs` (telemetry-operator `logging_controller.go`). TLS trusts the OpenShift service CA, which differs on B and isn't restored | **Accepted log gap** from switch until the EDPM update, which re-renders target and CA. Keep the logging IP in the modifier IP list (5.2). Alternative: put it behind a floating IP like DNS (the service-CA trust still breaks) |
| **InstanceHA heartbeat / fence_kdump** | `edpm_instanceha_monitoring_heartbeat_ip` and the kdump target (literal IPs). The heartbeat is InstanceHA's veto against fencing a host Nova reports down | InstanceHA **disabled on A before the backup** and re-enabled only after the EDPM update re-renders these targets (8) |
| **Octavia health-manager / rsyslog IPs** | Per OCP node and per cluster (`octavia-hmport-map`, operator-owned, not restored), rendered into the amphora `controller_ip_port_list` | Amphorae keep sending to A's IPs. B's health manager stays fenced until every amphora is reconfigured (`openstack loadbalancer amphora configure`) after commit (8) |
| **Designate bind9/unbound IPs** | Tenant-visible (NS records, subnet `dns_nameservers`) | Floating IPs like DNS, or accepted change. Open item (10.6) |

### 3.4 Credentials and trust
- **CA secrets** are restored. Leaf certs are reissued from the same CA, and EDPM keeps its existing certs.
- **User secrets** are restored (incl. `metadata_proxy_shared_secret`, InstanceHA HMAC).
- **RabbitMQ user secrets** are restore-labelled, so they're identical to A's, which prod EDPM needs.
- Mark the **keystone secret** (fernet + credential keys) for restore with `oc annotate secret keystone backup.openstack.org/restore=true` (the annotation bypasses the owner check). Credential keys decrypt the stored EC2/TOTP credentials. Fernet keys keep A's tokens valid across the switch. The plain B/R guide doesn't restore this secret either, so the same fix belongs upstream.

## 4. Procedure

```
               A (active, N)                           B (standby)
-- prepared in advance (no maintenance) ---------------------------------------------------------
Prepare        EDPM firewall/SSH for B (P4) via A       OCP clone of A (P8), operators at N (manual
               test NodeSet deployed (mandatory, 7)     approval), OADP + BSL, own NADs/MetalLB pools,
               keystone secret annotated                floating DNS + public pools (not advertised)
                                                        or L4 LB entries, modifiers generated + dry-run
                                                        in lab (5.2), network fence in place (6.3)
-- maintenance window ---------------------------------------------------------------------------
1 InstanceHA   disable on A (record previous state)
2 Freeze       close A's public endpoint (3.2)
3 Fence A      autonomous actors to 0 (6.2)
4 Drain        no task_state != NULL, no *ing volumes,
               no PENDING_* LBs, no migrations
5 Backup       B/R backup steps 1–5 (OVN DB mandatory)
6 Restore                                               B/R restore + B modifiers, fenced (6.2),
                                                        NodeSets without status, no step 15
7 Update                                                operators → N+1 (InstallPlan), then
                                                        OpenStackVersion → N+1 (test NodeSet OVN, 7.3)
8 Pre-switch                                            fence checks (6.3), A-vs-B state diff (4.3)
9 Switch       cut A's EDPM-facing endpoints            advertise floating DNS IP, EDPM OVN reset,
               (4.1)                                    reconnect, minimal unfence (4.1)
10 Validate                                             exit criteria (4.3), public endpoint closed
               ── rollback here: 4.2, zero data loss after test cleanup ──
11 Commit                                               open B's public endpoint ◄── point of no return
-- end of maintenance ---------------------------------------------------------------------------
Post-commit    A = next standby (wipe/rebuild)          unfence autonomous actors, amphora reconfigure
                                                        → octavia HM, EDPM update, InstanceHA last (8)
```

Window ≈ steps 1–10. Measure it in a lab with prod-size data. Include the **OADP Data Mover upload and download of all PVCs** (Glance file backend, Swift, DB dumps), not just the DB size.

### 4.1 Switch sequence (step 9)
1. **Cut A's EDPM-facing endpoints:**
   - Withdraw A's MetalLB advertisements (removes A's DNS IP and every internal LB endpoint in one generic step).
   - Scale A's OVN DBs down. EDPM reaches them via pod NAD IPs, not MetalLB.
   - Remove A's ctlplane `ovn-controller` (`nicMappings: {}`, 6.2).
   - Restart A's RabbitMQ pods, so established AMQP sessions reset. They come back unreachable.
2. **Advertise the floating DNS IP on B** (create the advertisement for that pool).
3. **EDPM OVN client reset** (7.2): `ovn-appctl -t ovn-controller sb-cluster-state-reset`, then restart ovn-metadata-agent (and ovn-bgp-agent if used) on every EDPM node.
4. **Wait for reconnect:** all nova-compute services up, all network agents alive, all chassis connected to B's SB.
5. **Minimal unfence of B**, only what EDPM attachment and validation need:
   - If gateways run on the ctlplane: restore B's `nicMappings`, delete A's stale ctlplane chassis records from B's SB, and verify that **every `cr-lrp` is claimed by a B chassis**. North-south traffic is down from step 1 until this point. Budget it, and lab-test it.
   - Gateways on EDPM networker nodes are handed over unchanged.
   - Autonomous actors stay fenced until commit (8).

### 4.2 Rollback sequence (before commit)
Same steps, **roles swapped**, preceded by cleanup:
1. **Clean up test resources on B** (test VMs, volumes, images, ports, FIPs in the test project) while B's API is still up. Otherwise they stay behind as orphans unknown to A (libvirt domains, RBD images, OVS ports).
2. **Cut B's EDPM-facing endpoints:** withdraw B's advertisements, including the floating DNS IP. Scale down B's OVN DBs, set B's `nicMappings: {}`, restart B's RabbitMQ.
3. On A: re-create the advertisements, scale up the OVN DBs, restore `nicMappings`, and start RabbitMQ (already running).
4. EDPM OVN client reset again. The cluster ID changes back.
5. Wait for reconnect. Verify the `cr-lrp` claims.
6. Unfence A's autonomous actors. Open A's public endpoint (3.2).
7. **InstanceHA on A last**, restoring the state recorded in step 1 of the window.

Rehearse the rollback in the lab and give it a time budget. It runs inside the same window.

### 4.3 Pre-switch check and validation exit criteria
- **A-vs-B state diff (step 8):** compare A's and B's view of instance host/power/task state, volume and attachment status, amphora set, LB provisioning status and router gateway bindings. **Abort on any difference.** A's agent-reported state (heartbeats, resource provider inventory) may differ and self-heals after reconnect.
- **Exit criteria (step 10), all required before commit:**
  - every prod nova-compute service up on B
  - every network agent alive
  - every chassis connected to B's SB
  - every `cr-lrp` claimed by an active chassis (if ctlplane gateways)
  - metadata reachable from a test VM
  - no DNSData entry with an IP from A's pools
  - smoke tests passed in the test project (test VMs pinned to the test NodeSet's aggregate/AZ)

## 5. Restore on B

### 5.1 Data path
The restore follows the B/R guide unchanged for the data:
- Galera dumps (restore step 7)
- OVN DB restore (step 8)
- the nova restart for cell mappings (step 9)
- the neutron ↔ OVN sync in **`log` mode only** (step 10; drift means stop and investigate, never `repair` on a prod switch)
- the octavia sync (step 11)

The DBs are empty at import, and the operators run db sync after the import as usual. The update to N+1 afterwards runs db sync again through the regular update path.

### 5.2 Resource modifier and exclusion rules for B
Generate these from an A→B mapping file, not by hand:
- **LB IPs**: rewrite **every** `metallb.universe.tf/loadBalancerIPs` annotation in the `OpenStackControlPlane` to B's IP. That includes the keystone/nova/neutron/... service overrides, rabbitmq, swift, **telemetry logging** (`spec.telemetry...logging.annotations`), designate and any other IP-bearing field. **Exception:** the floating IPs keep A's values: dnsmasq (3.1) and, with MetalLB public endpoints, the public VIPs (3.2).
- **NADs**: exclude with `excludedResources: [network-attachment-definitions.k8s.cni.cncf.io]` in the B/R restore of order 10. Resource modifiers can only patch, not exclude. B's own NADs exist beforehand. Don't use `existingResourcePolicy: update`, which would overwrite them.
- **`BGPConfiguration`**: exclude as well. B's own one is created in prepare.
- **Fencing** (6.2) as patches on the `OpenStackControlPlane`.
- `NetConfig`/`Reservation`/`IPSet`: restored as-is, so prod EDPM IPs stay reserved.
- **Failure mode:** a JSON patch `replace` on a missing path fails the **whole object**, and the `OpenStackControlPlane` isn't restored. **Dry-run** the modifiers in the lab against a copy of A's backup, with that lab fenced like B. Check the restore warnings in the window.

Example rule shape:
```yaml
- conditions:
    groupResource: openstackcontrolplanes.core.openstack.org
  patches:
  - operation: replace
    path: /spec/keystone/template/override/service/internal/metadata/annotations/metallb.universe.tf~1loadBalancerIPs
    value: "172.17.0.180"        # B's IP for keystone internal
  - operation: replace
    path: /spec/ovn/template/ovnController/nicMappings
    value: {}                    # no ctlplane gateway chassis on B until switch (6.2)
  - operation: replace
    path: /spec/cinder/template/cinderVolumes/ceph/replicas
    value: 0                     # fenced until commit (6.2)
```

## 6. B must not affect prod

This is the most critical part of the design. Freezing A and fencing its actors removes the **stale data** risk. Two risks remain:
- **Reactions to "EDPM not attached"**: until the switch, prod EDPM is attached to A, so from B everything looks dead.
- **Two actors**: B's services acting while A's still could.

### 6.1 Residual risks
| Sev | Risk | Mitigation |
|---|---|---|
| **Critical** | **OVN gateway takeover**: B's ctlplane `ovn-controller` registers as a gateway chassis. That's always the case when `nicMappings` are set: the `enable-chassis-as-gateway` field is deprecated and ignored, and the script defaults to gateway on. B becomes active for prod routers, GARPs prod FIPs/router IPs, or advertises them via BGP | `nicMappings: {}` on B until the switch (removes the OVNController). Test networker nodes on isolated provider segments |
| **Critical** | **Two gateway sets after the switch**: A's ovn-controller keeps its flows when its SB disappears, and keeps answering for prod FIPs | Remove A's OVNController at switch (4.1). Verify the `cr-lrp` claims |
| **Critical** | **InstanceHA** fences prod computes | Disabled on A **before the backup**, so it's restored disabled. Re-enabled only after the EDPM update (8) |
| **High** | **Octavia** health manager fails over every amphora (heartbeats go to A's HM IPs) | HM/housekeeping/worker fenced until the amphora reconfigure after commit (8) |
| **High** | Duplicate cinder-volume/backup, manila-share, ironic-conductor, designate worker/producer | Fenced on B until commit. A's are fenced at freeze |
| **High** | aodh/heat act; ceilometer and usage audits publish duplicate samples to external billing/metrics | Fenced on B until commit |
| **High** | **Rogue DHCP/PXE**: ironic-inspector dnsmasq on the shared provisioning L2 | Fenced on B until commit |
| **High** | **Tests** touching restored resources (attaching a prod volume to a test VM corrupts data; deleting prod images/volumes) or landing on prod computes after the switch | Dedicated test project, non-admin, test VMs pinned to the test NodeSet's aggregate/AZ, test-only images/volumes/networks/FIP range. Cleanup before rollback (4.2) |
| Medium | Restored DNSData carries A's IPs until B's operators re-patch them | Pre-switch check (4.3) |
| Low | B calling A's public API (public FQDNs resolve to A) | A's public endpoint is closed. Split DNS in B anyway |

### 6.2 Fencing with existing knobs
- **Fence A at freeze** (window step 3): scale A's autonomous actors to 0 (the list below). InstanceHA was already disabled in step 1.
- **Fence B from restore** (resource modifiers, 5.2):
  - `nicMappings: {}` on the ctlplane `ovn-controller`.
  - Replicas 0, or a non-matching node selector for per-node services: cinder-volume/backup, manila-share, ironic-conductor and ironic-inspector, octavia health-manager/housekeeping/worker, designate worker/producer, aodh, heat-engine, watcher, ceilometer.
  - Exact CR paths to collect per service (10.7).
- **At switch:** only the minimal unfence (4.1 step 5).
- **After commit:** the remaining unfence (8).

The operator feature that replaces this patch list with one switch is the **standby mode** in the [pre-staged doc](ctlplane-blue-green-update-prestaged.md) (7.1). The simple variant works without it, but the patch list has to be complete and is easy to get wrong.

### 6.3 Generic network fence and checks
The per-service list can miss something, so a second, generic layer applies on B until the switch:
- **MultiNetworkPolicy** on the NADs (macvlan bypasses the OVN-Kubernetes NetworkPolicy/EgressFirewall) plus **EgressFirewall** on the cluster network. Block the BMC networks, provider/external segments, the provisioning network, external DNS backends and billing/notification sinks. Ceph stays reachable for the test project.
- **Split DNS in B** for the public FQDNs.

**Checks during preparation:**
- From B (cluster network and every NAD) and from the test nodes: no ARP/GARP/ND and no BGP sessions on the prod provider segments.
- The floating DNS IP is not advertised by B.

**Checks after restore, before update:**
- Every fenced service is at 0.
- InstanceHA is disabled.
- No OVNController exists on B.
- No `cr-lrp` is claimed by a B or test chassis.

## 7. EDPM handover

B takes over the pre-provisioned nodes **without a configuration change**. The nodes keep running exactly as A configured them.

### 7.1 No deployments from B until the EDPM update
- B/R guide **step 15 (config resync) is dropped**. On B after the update, it would push N+1 config and images onto prod EDPM, and rollback would become impossible.
- **NodeSets are restored without status** (B/R step 14). Old `OpenStackDataPlaneDeployment` CRs aren't restored either. B doesn't need the status before the EDPM update:
  - The OVN dataplane gate skips NodeSets without a deployed OVN image.
  - The final dataplane stage waits until every NodeSet is Ready at the target version. It stays in progress until the post-commit EDPM update, which is correct.
  - The greenfield check only needs B's ctlplane to be deployed.
  - The EDPM update takes its images from `OpenStackVersion`.
- **Option:** restore the prod NodeSets only right before the switch (step 14 filtered by name). The order is free, and it shortens the time a mistaken deployment from B could reach prod EDPM.
- **Caveat:** prod NodeSets look **"not deployed"** on B until the EDPM update. The runbook must forbid any deployment against them until then (later enforced by the standby mode).

### 7.2 The one sanctioned touch: OVN client reset
B's SB cluster is bootstrapped from a standalone backup (B/R restore step 8). That gives it a **new cluster ID and a lower raft index** than EDPM clients last saw from A. ovn-controller and the Python IDL clients (ovn-metadata-agent, ovn-bgp-agent) refuse such a server as stale. The plain B/R guide never hits this, because step 15 restarts everything.

At switch and at rollback, run on every EDPM node:
1. `ovn-appctl -t ovn-controller sb-cluster-state-reset`
2. A restart of ovn-metadata-agent (and ovn-bgp-agent if used).

Run it as an **ad-hoc ansible run from an admin host** with the restored SSH key, **not** as an `OpenStackDataPlaneDeployment`: a deployment from B would set the NodeSet `DeployedVersion` to the target version, so B would consider the prod NodeSets updated. It changes no configuration. **Lab-confirm** the stale-index behaviour and that this reset is sufficient.

### 7.3 Test NodeSet (mandatory)
The test NodeSet lives on A, is part of the backup, and is handed over like prod. Without status, the OVN gate skips it too. To keep the standard update order:
1. Set the target-stage annotation `core.openstack.org/update-target-stage=ovn-controlplane` on `OpenStackVersion`.
2. Run the OVN deployment on the test NodeSet.
3. Remove the annotation.

**If the OVN image is unchanged between N and N+1**, the gate requires seeing a *running* `ovn` deployment, and a run during the pause doesn't count. So remove the annotation **while the test OVN deployment is still running**. Without a test NodeSet the update can't pass that stage in this case at all, so the test NodeSet is mandatory.

Afterwards keep the test nodes at N for the other services for a while. That's the state prod is in after the switch.

### 7.4 First deployment from B = the EDPM update
It also carries the handover drift: reissued leaf certs, regenerated config secrets, the logging target/CA, InstanceHA heartbeat/kdump targets, and `registries.conf` from B's mirror config (P8). Make sure it includes cert installation (`install-certs`). Diff B's rendered config against A's before running it.

### 7.5 B/R guide mapping
| B/R guide step | Simple blue/green |
|---|---|
| — | **InstanceHA disabled on A**, A's public endpoint closed, A's actors fenced, drain |
| Backup 1–5 | As-is. OVN DB backup **mandatory** |
| Restore 0 (modifiers) | Extended with the B rules (5.2) |
| Restore 1–5 | As-is, NADs and `BGPConfiguration` excluded |
| Restore 6 (GaleraBackup, IPSet, DataPlaneService) | As-is |
| Restore 7–8 (Galera, OVN DB) | As-is |
| Restore 9 (resume + nova restart) | As-is, **fenced** |
| Restore 10 (neutron ↔ OVN) | `log` mode only |
| Restore 11 (octavia) | As-is |
| Restore 14 (dataplane) | NodeSets without status, no Deployments (optionally prod NodeSets right before the switch) |
| — | **Update B**: operators → N+1, then `OpenStackVersion` (test NodeSet OVN, 7.3) |
| Restore 15 (EDPM deployment) | **Dropped.** Replaced by the OVN client reset (7.2). The first deployment is the post-commit EDPM update |
| Restore 16 (InstanceHA) | **Very last**, after the EDPM update (8) |
| — | Pre-switch checks, switch, validate, commit/rollback (section 4) |

## 8. Commit and post-commit
- **Commit = opening B's public endpoint** (3.2). Before it, rollback loses nothing (after test cleanup). After it, rollback loses B's writes and leaves orphans.
- **Post-commit order:**
  1. Unfence B's remaining autonomous actors, except octavia health-manager/housekeeping.
  2. **Reconfigure all amphorae** to B's health-manager/rsyslog IPs (`openstack loadbalancer amphora configure`, issued by B's worker over lb-mgmt). Then unfence health-manager and housekeeping.
  3. **EDPM update** (the first deployment from B, 7.4), soon after commit, to keep the version skew and the log gap short.
  4. **InstanceHA last:** re-enable on B only if it was enabled on A (state recorded in window step 1), after the EDPM update has re-rendered the heartbeat/kdump targets and all prod computes report up.
- EDPM updates stay forward-only.
- Cost: a second ctlplane OCP cluster, which can alternate between the A and B roles across updates.

## 9. Operations
- **Monitoring:** split alerting between A and B during the window (silence A's fenced services, B's "not deployed" NodeSets). Prometheus history stays on A. Export it if needed before A is wiped.
- **Runbook timing:** time budgets per step, including the rollback (4.2), all rehearsed in the lab.
- **Test access:** test nodes via B's extra dnsmasq Service, API tests inside B on internal endpoints (3.1).

## 10. Open items
1. **OVN version skew**: a fully updated N+1 ctlplane facing N ovn-controllers on prod EDPM after the switch. The minor update gates `MinorUpdateOVNDataplane` before the remaining ctlplane, and on B that gate is satisfied by the test NodeSet only. Input: `ovn-match-northd-version` defaults to false on EDPM. If a customer sets it, EDPM stops processing after the switch. **To be checked with networking.**
2. Generic **ctlplane ↔ EDPM skew** per service for updates that carry an upstream bump (nova computes N-1, neutron agents N-1). Ideally shipped as release metadata the precheck verifies.
3. B's update with status-less prod NodeSets: confirm in the lab that the OVN gate skips them, the dataplane stage waits, and the post-commit EDPM update brings them to Ready at N+1 (7.1).
4. **EDPM OVN client reset** (7.2): confirm the stale-index rejection and the reset procedure. Measure EDPM reconnect and agent recovery after the switch (no DNS caching beyond the TTL; RabbitMQ reconnect).
5. keystone-operator adopts the restored `keystone` secret and keeps rotating it.
6. Designate bind9/unbound IPs (tenant-visible).
7. Complete list of side-effecting services and their CR paths for 6.2 (per-operator audit).
8. Window duration with prod-size data, including the Data Mover transfer, and the north-south outage at switch (ctlplane gateways).
9. Floating IPs (dnsmasq, public VIPs): confirm that MetalLB on both clusters handles pools that are advertised on one cluster only, and measure how fast EDPM and clients converge (ARP/BGP).
