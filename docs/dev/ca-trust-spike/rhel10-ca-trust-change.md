# CA trust: the RHEL directory-hash migration — situation & proposed change

Short companion to the full spike (`ca-trust-bundle-spike.md`). Focus: what changes
for the OS CA trust store and how RHOSO should adapt.

> **Status / timeline (authoritative — [RHEL-109484](https://issues.redhat.com/browse/RHEL-109484)):**
> the single-file bundle removal is **postponed to RHEL 11.** The symlinks
> (`/etc/pki/tls/cert.pem`, `certs/ca-bundle.crt`, `ca-certificates.crt`,
> `ca-bundle.trust.crt`, and `/etc/ssl` equivalents) were briefly dropped and then
> **restored in RHEL 10** (ca-certificates changelog: "Bring back openssl trusted
> format bundle as well"). **RHEL 9 and RHEL 10 keep both formats**, so the current
> RHOSO mechanism works throughout — verified on UBI 10.2 (cert.pem + ca-bundle.crt
> present, plus the hash-dir). **This change is forward-looking for RHEL 11**; the
> implementation below is a prepared prototype to land when RHEL 11 removal approaches.

## Current situation (RHOSO 18 / UBI 9)

`openstack-operator` (`internal/openstack/ca.go`, `ReconcileCAs`) builds the
`combined-ca-bundle` Secret by Go PEM-concatenation of: cert-manager issuer CAs
(ingress/public, internal, libvirt, ovn) + user CAs (`spec.tls.caBundleSecretName`)
+ mirror-registry CAs + the operator image's system bundle. Two keys:
`tls-ca-bundle.pem` (full) and `internal-ca-bundle.pem` (issuer-only).

It is mounted (subPath) over `/etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem`
(downstream) / `/etc/ssl/certs/ca-certificates.crt` (upstream). On UBI 9 this
covers everything, because:
- the OS default file `/etc/pki/tls/cert.pem` and curl's `certs/ca-bundle.crt` both
  symlink to that path;
- infra components (galera, redis, memcached, httpd) are configured with explicit
  CA paths pointing at the mounted PEM;
- python uses the system PEM.

## The upcoming issue (RHEL 11)

`ca-certificates` moves the **default** OpenSSL trust store to the **directory-hash**
format at `/etc/pki/tls/certs` and **removes** the default single-file bundles
(`/etc/pki/tls/cert.pem`, `certs/ca-bundle.crt`) — for faster init and post-quantum
cert sizes ([Fedora change](https://fedoraproject.org/wiki/Changes/droppingOfCertPemFile)).
Already shipped in Fedora 44 (verified: `cert.pem` gone, hash-dir only). Per
[RHEL-109484](https://issues.redhat.com/browse/RHEL-109484) this is **deferred to
RHEL 11** — RHEL 9 and RHEL 10 keep the single-file bundles. `c_rehash` was removed in
OpenSSL 4.0 (use `openssl rehash`).

Consequence for RHOSO **on RHEL 11**: **consumers that use OpenSSL's *default* verify
paths** (e.g. python `ssl` default context) will no longer read a bundle file — they
read the hash-dir. So overwriting `tls-ca-bundle.pem` will no longer reach them.
(On RHEL 9/10 today this is a non-issue — `cert.pem` still resolves to our mounted PEM.)

**Upgrade window makes it a both-regimes-at-once requirement (at the RHEL 10→11 step):**
the new operators rebuild the bundle while older (RHEL 10) deployments still run their
images, so the output must serve both the file and hash-dir consumers simultaneously.

## What is *unaffected* (covered by keeping the PEM mount)

Verified empirically (UBI 9.4/9.6/9.8 + Fedora 44):
- **curl** — on both regimes it uses `CAfile = /etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem`
  (Fedora repointed curl at the extracted bundle, which persists). Our existing
  mount covers it.
- **galera, redis, memcached, httpd** — use explicitly-configured CA paths pointing
  at the mounted `combined-ca-bundle` PEM, so they never used the OS default store
  and are unaffected. (To confirm per-component during implementation.)
- **python `requests`** on RHEL — uses the system PEM file → covered by the mount.

## The actual gap

OpenSSL **default-path** consumers (python `ssl` default context) on UBI 10: they
need the custom CAs available in a hash-dir search path.

## Proposed change

Mechanism: **the operator produces both artifacts itself** (Option D) — no builder
Job, image, RBAC, or async gating. It only needs `openssl` for the subject-hash
filename (`openssl x509 -hash`; algorithm stable since OpenSSL 1.0.0).

1. **Keep building the `combined-ca-bundle` PEM** (`tls-ca-bundle.pem` +
   `internal-ca-bundle.pem`) in `ReconcileCAs` as today; add **blocklist** as a Go
   filter step.
2. **Additionally produce a small directory-hash Secret** holding **only our CAs** —
   cert-manager roots + (optionally) the OCP cluster CA + customer CAs +
   mirror-registry — as regular `<hash>.<seq>` files (Secrets can't store symlinks;
   regular hash-named files work with `-CApath`). Public roots are **not** included:
   the container image already ships them as its hash-dir. Small (a handful; ~KB).
   The operator computes each subject hash (`openssl x509 -hash`), groups by hash and
   assigns `.0`, `.1`, … for same-subject collisions (e.g. a CA + its renewal), and
   writes the Secret directly (idempotent `EnsureSecrets`, re-run on CA-input change).
   > **One cert per file.** A customer bundle (`spec.tls.caBundleSecretName`) may be
   > several concatenated PEM certs in one file, and `openssl rehash`/`-hash` needs
   > exactly one cert per file (`rehash` **silently skips** multi-cert files → 0
   > links). The operator already parses each input into individual x509 certs
   > (`ca.go` `getCertsFromPEM`), so it handles this naturally.
   >
   > Requires `openssl` in the operator image (D1). Alternative D2: a Go
   > `X509_NAME_hash` (validated against `openssl`) to drop the dependency.
3. **Consumption (per-operator follow-up), unconditional, no `/etc/pki/tls/certs`
   overlay:**
   - Keep the PEM mount → curl (both regimes) + explicit-config infra components.
   - Set **`SSL_CERT_DIR=/etc/pki/tls/certs:<our-small-dir>`** on pods → OpenSSL
     default consumers (python `ssl`) search the image's public hash-dir **plus**
     our small custom dir. Preserves the **fast-start / lazy hash lookup** the OS
     change was made for. Verified: a python TLS handshake trusts a custom CA via
     `SSL_CERT_DIR`, and the colon-list preserves public trust.
   - Do **not** overlay `/etc/pki/tls/certs` — it would clobber the per-service cert
     files (`/etc/pki/tls/certs/<endpt>.crt`) and the base public set, and is
     unnecessary. Service cert/key stay where they are (a convention that coexists
     with the hash-dir).

## Why not just `SSL_CERT_FILE=<extracted PEM>`?

It works (verified), but points openssl-default at the big bundle, re-introducing
the full-bundle parse the OS change removed — losing the fast-start benefit. The
small hash-dir + `SSL_CERT_DIR` keeps it fast. (`SSL_CERT_FILE` remains a simpler
fallback if the small-dir path proves impractical.)

## Validation still needed (implementation phase)

- Confirm each infra component (galera, redis, memcached, httpd) trusts via explicit
  CA config → the mounted PEM (so they're covered on UBI 10 without `SSL_CERT_DIR`).
- Confirm the python trust path per service (`requests`/system PEM vs `ssl` default
  context) to be sure `SSL_CERT_DIR` coverage is sufficient.
- Functional/interop test on both UBI 9 and UBI 10 base images.

## Out of scope

Java `cacerts` (no JVM services), edk2, and a full `/etc/pki/tls/certs` overlay.
