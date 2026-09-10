# Spike OSPRH-36600 — outcome

**TL;DR:** building the CA trust with `update-ca-trust` and shipping it via a Secret
is **feasible and the right direction**. It adds **blocklist support** +
OS-tooling correctness, and — with a small addition — handles the eventual RHEL 11
trust-store change. **Not needed on RHEL 9/10** (see timeline); the implementation is
a prepared prototype. Full write-up + PoC in `docs/dev/ca-trust-spike/`.

**Timeline (RHEL-109484):** the default single-file bundle removal is **postponed to
RHEL 11** — the symlinks were restored in RHEL 10 (verified: UBI 10.2 still ships
`cert.pem` + `ca-bundle.crt` alongside the hash-dir). RHEL 9 and 10 keep both formats,
so the current RHOSO mechanism works throughout. This is forward-looking for RHEL 11.

## The driver (OSPRH-16670): RHEL 11 changes the default trust store

`ca-certificates` moves the **default** OpenSSL store to the **directory-hash** at
`/etc/pki/tls/certs` and removes the default single-file bundles (`cert.pem`,
`certs/ca-bundle.crt`) — for faster init / post-quantum certs
(https://fedoraproject.org/wiki/Changes/droppingOfCertPemFile). Already in Fedora 44
(verified); per RHEL-109484 **deferred to RHEL 11** (RHEL 9/10 keep the bundles).

Effect **on RHEL 11**: overwriting `tls-ca-bundle.pem` will no longer reach
**openssl-default** consumers (they read the hash-dir). **curl is unaffected** —
Fedora repointed it at `…/extracted/pem/tls-ca-bundle.pem` (the path RHOSO already
mounts). So is the infra tier (galera/redis/memcached/httpd) — they use explicit CA
config → the mounted PEM. The gap is only openssl-default / python `ssl`. (On RHEL
9/10 there is no gap — `cert.pem` still resolves to our mounted PEM.)

## Proposed change

Mechanism: **the operator produces both artifacts itself** (no builder Job/image/
RBAC/gating); it only needs `openssl` for the subject-hash filename.

1. Keep producing `combined-ca-bundle` (same contract: `tls-ca-bundle.pem` +
   `internal-ca-bundle.pem`) in `ReconcileCAs`; blocklist = Go filter.
2. Additionally produce a **small directory-hash Secret** of **only our CAs**
   (cert-manager roots + optional OCP cluster CA + customer CAs + mirror-registry) as
   `<hash>.<seq>` files (~KB; public roots already ship in the image). Hash via
   `openssl x509 -hash`; one cert per file (operator already splits inputs).
3. Consumption (per-operator follow-up): keep the PEM mount (curl + infra) and set
   `SSL_CERT_DIR=/etc/pki/tls/certs:<our-dir>` for openssl-default/python on rhel10.
   This preserves the OS's fast-start (lazy hash lookup) and does **not** touch
   `/etc/pki/tls/certs`, so no collision with per-service cert files.

## Evidence (UBI 9.4/9.6/9.8 + Fedora 44)

blocklist works; directory-hash is Secret-shippable via flattening (no symlinks
needed, incl. k8s projected layout); python TLS handshake trusts a custom CA via
`SSL_CERT_DIR` with public trust preserved and lazy lookup.

## Open questions / validation

- Confirm per-component (galera/redis/memcached/httpd) trust via explicit CA config →
  mounted PEM; and python path (`requests`/system PEM vs `ssl` default).
- `openssl` dependency in the operator image (D1) — or implement `X509_NAME_hash` in
  Go (D2) to drop it.
- (Fallback) `SSL_CERT_FILE=<extracted PEM>` — simpler, but loses fast-start.

## Next

Implementation plan (openstack-operator) in
`docs/dev/ca-trust-spike/implementation-plan.md`, structured to be testable in a real
env on current images. Story breakdown in `follow-up-stories.md`.
