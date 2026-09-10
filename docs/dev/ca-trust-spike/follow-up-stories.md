# OSPRH-36600 → implementation: story stubs

> **Status: forward-looking (RHEL 11).** Per RHEL-109484 the single-file bundle
> removal is postponed to RHEL 11; RHEL 9/10 keep it, so none of this is needed on
> current images. Story 1 is implemented + tested on the branch as a prepared
> prototype; hold merge + Story 2 until RHEL 11 removal approaches. Track RHEL-109484.

Turning the spike into shipped work. Stories 1–3 = MVP; 4 = deferred. Design detail:
`rhel10-ca-trust-change.md`. How to build/test: `implementation-plan.md`.

Chosen mechanism: **Option D — the operator produces both artifacts itself** (no
builder Job/image/RBAC/gating). It only needs `openssl` for the subject-hash filename.

Guiding decisions:
- Keep the `combined-ca-bundle` contract (`tls-ca-bundle.pem` + `internal-ca-bundle.pem`),
  built in `ReconcileCAs` as today; blocklist = Go filter.
- rhel9↔rhel10 handled by: PEM mount (curl + explicit-config infra) **+** a small
  custom-only hash-dir Secret consumed via `SSL_CERT_DIR=/etc/pki/tls/certs:<dir>`
  (openssl-default/python on rhel10; preserves fast-start). No `/etc/pki/tls/certs`
  overlay → no service-cert collision.

## Story 1 — Produce the hash-dir Secret in `ReconcileCAs`
In `internal/openstack/ca.go`: collect a **custom-only** set (cert-manager roots +
`spec.tls.caBundleSecretName` + mirror-registry + optional OCP CA), one cert per file
(operator already parses each input individually — handles multi-cert bundles). For
each compute `openssl x509 -hash`, group + assign `<hash>.0/.1/…`, write a new Secret
`combined-ca-bundle-certs` (same labels/`SkipSetOwner` as `combined-ca-bundle`,
idempotent `EnsureSecrets`). Add `openssl` to the operator image. Keep the PEM build
unchanged. **Functional tests mandatory** (today CA *creation* has little/no coverage).
**AC:** greenfield produces `combined-ca-bundle` (unchanged) + `combined-ca-bundle-certs`
with `<hash>.<seq>` keys; a multi-cert customer bundle yields multiple keys; a
CA-input change updates both; tests green.

## Story 2 — Consume on rhel10 (`SSL_CERT_DIR`)
lib-common helper to mount the hash-dir Secret at a dedicated path and set
`SSL_CERT_DIR=/etc/pki/tls/certs:<dir>`; keep the existing PEM mount. Reference impl
on the openstackclient pod (operator-owned); adopt per-operator. Confirm per-component
(galera/redis/memcached/httpd use explicit CA config → mounted PEM; python `ssl`/`requests`).
**AC:** on UBI9 and UBI10 images a service trusts a custom CA (curl + openssl/python)
without touching `/etc/pki/tls/certs`; public trust preserved.

## Story 3 — Blocklist support
Blocklist input (e.g. `spec.tls.caBlocklistSecretName`) → filter blocklisted certs
out of both the PEM and the hash-dir in Go.
**AC:** a blocklisted CA appears in neither `combined-ca-bundle` nor `combined-ca-bundle-certs`.

## Story 4 — (Deferred) drop the openssl dep + Java
D2: implement `X509_NAME_hash` in Go (validated against `openssl x509 -hash`) to
remove the runtime/CI `openssl` dependency. Java: only when a JVM service exists — add
`cacerts` key + mount as one unit.

### Notes
- No Job/privileged pod: operator computes hashes + writes Secrets in-process.
- Small hash-dir = our CAs only (public roots ship in the image); assert size in a test.
- One cert per file: `openssl rehash`/`-hash` silently skips multi-cert files → 0 links;
  operator writes inputs individually.
- `openssl` needed in the operator image (D1) and on CI runners (functional tests).
- edk2 / full dir overlay: out of scope. `SSL_CERT_FILE`=extracted PEM = documented
  fallback (simpler, but loses fast-start).
