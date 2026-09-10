# Implementation plan — CA-trust hash-dir in openstack-operator (OSPRH-36600)

> **Status: forward-looking (RHEL 11).** Per [RHEL-109484](https://issues.redhat.com/browse/RHEL-109484)
> the single-file bundle removal is postponed to RHEL 11; RHEL 9 and 10 keep it, so
> this is **not needed on current images**. Producer (Story 1) is implemented + tested
> on the `poc/OSPRH-36600-ca-trust-builder` branch as a prepared prototype; hold the
> merge (and the consumption side) until RHEL 11 removal approaches. Track RHEL-109484.

Chosen approach: **Option D — the operator produces everything itself, no builder
Job.** `ReconcileCAs` continues to build the `combined-ca-bundle` PEM Secret (as
today) and **additionally** produces a small **directory-hash Secret of only our
CAs**, both synchronously in Go. Consumers add `SSL_CERT_DIR` so custom CAs are
trusted on the RHEL 11 hash-dir regime.

Design reference: `rhel10-ca-trust-change.md`. Spike evidence: `ca-trust-bundle-spike.md`.

## Why no Job / sidecar

We only need the PEM (operator already builds it) + a small hash-dir of *our* CAs
(public roots ship in the service image). The only "OS tooling" needed is computing
the OpenSSL subject-hash filename — done via `openssl x509 -hash`. That means **no
builder image, no Job, no new SA/RBAC, no publisher, no privileged pod, and no
async gating/condition** — the operator produces both Secrets inline and remains the
sole writer. (Full option comparison in the spike discussion.)

## Testable on current images

The mechanism needs no symlink-removed RHEL 10 image. `SSL_CERT_DIR` is honored on
current UBI 9.4/9.6/9.8. To exercise the rhel10 path explicitly on a current image,
put a **test CA only in the hash-dir Secret** (not the PEM) and confirm trust via
`SSL_CERT_DIR`.

## Subject-hash: how to compute it (chosen: D1 exec)

The one bit needing "OS tooling" is the OpenSSL subject-hash filename (`<hash>` =
first 4 bytes, little-endian, of SHA-1 of the *canonicalized* subject DER; stable
since OpenSSL 1.0.0). Options considered:

- **D1 — exec `openssl x509 -hash` (chosen).** Add the `openssl` CLI to the operator
  image (`microdnf install openssl`; note `libcrypto` is already present in
  ubi-minimal via `openssl-libs`) and to CI runners. Least code, provably correct,
  CLI handles the crypto-provider setup. Can also do one `openssl rehash` on a temp
  dir instead of per-cert.
- **D3 — cgo → `libcrypto`.** Viable here (cgo already `=1`, `libcrypto` already in
  the runtime image), but: `X509_NAME_hash` is a **function-like macro** in OpenSSL
  3.x (`#define … X509_NAME_hash_ex(x,NULL,NULL,NULL)`) — **cgo can't call C macros**,
  so call `X509_NAME_hash_ex` (or wrap it in a static C fn), check the return
  (`0` = error), and pass a non-FIPS libctx for FIPS. Needs `openssl-devel` at build.
  ~20–30 lines, more C-interop surface — not the "10-liner" it looks like.
- **D2 — pure Go `X509_NAME_hash`.** No deps, but must reimplement the subject-DER
  canonicalization (error-prone); validate against `openssl x509 -hash`. Follow-up.

**FIPS caveat (all three):** the hash is SHA-1. On a FIPS-enforced cluster this needs
testing — OpenSSL tooling (CLI / `X509_NAME_hash_ex` with a proper libctx) is most
reliable. If SHA-1 name-hashing is ever blocked under FIPS, fall back to
**`SSL_CERT_FILE=<extracted PEM>`** which needs no hashing (loses fast-start).

## Changes — `internal/openstack/ca.go` (`ReconcileCAs`)

1. Keep issuer/root-CA creation and the existing `combined-ca-bundle` PEM build
   (`tls-ca-bundle.pem` + `internal-ca-bundle.pem`) unchanged.
2. Collect a **custom-only** set = cert-manager roots + `spec.tls.caBundleSecretName`
   + mirror-registry (+ optional OCP cluster CA). Exclude the operator-image system
   bundle (the service image ships public roots). The operator already parses each
   input into individual x509 certs (`getCertsFromPEM`), so certs are **one per
   file** — a multi-cert customer bundle is split naturally (required: `openssl
   rehash`/`-hash` needs one cert per file).
3. For each custom cert compute its subject hash (`openssl x509 -hash -noout -in`),
   group by hash and assign `<hash>.0`, `<hash>.1`, … (handles same-subject
   collisions, e.g. a CA and its renewal). Build a `map[string][]byte` of
   `<hash>.<seq> → cert PEM`.
4. Write a new Secret (e.g. `combined-ca-bundle-certs`) with those keys, same
   labels/owner semantics as `combined-ca-bundle` (`SkipSetOwner`). Use
   `secret.EnsureSecrets` (idempotent; re-runs on CA-input change like the PEM).
5. Blocklist (later): filter blocklisted certs out of both the PEM and the hash-dir
   in Go.

No gating/condition changes needed: the Secrets are produced synchronously before
the service reconciles (existing ordering).

## Consumption (per-operator follow-up)

- Keep the existing PEM mount (`…/extracted/pem/tls-ca-bundle.pem` + `/etc/ssl/...`).
- Mount `combined-ca-bundle-certs` at a dedicated dir and set
  `SSL_CERT_DIR=/etc/pki/tls/certs:<dir>`. lib-common helper + adopt per-operator.
  Reference impl on the openstackclient pod (operator-owned). Do **not** overlay
  `/etc/pki/tls/certs`.

## Tests

- **Functional (envtest):** create an `OpenStackControlPlane` (+ custom CA secret) as
  the existing CA tests do; assert `combined-ca-bundle` keeps its contract **and**
  `combined-ca-bundle-certs` is produced with `<hash>.<seq>` keys whose contents
  match the inputs; assert a multi-cert customer bundle yields multiple hash keys.
  (Requires `openssl` on the runner for D1.)
- **Unit:** hash/sequence assignment + collision handling.

## Real-env test procedure

1. Build/deploy the operator (with `openssl` in the image).
2. Create an `OpenStackControlPlane` with TLS + a custom CA in
   `spec.tls.caBundleSecretName` (include a **multi-cert** bundle to exercise splitting).
3. Verify both Secrets: `combined-ca-bundle` (contract unchanged) and
   `combined-ca-bundle-certs` (one `<hash>.<seq>` per input CA).
4. Change a CA input → both Secrets update on reconcile.
5. In a service pod with `SSL_CERT_DIR` set: `openssl verify` / small python `ssl`
   check trusts the custom CA; add a hash-dir-only test CA to confirm the
   `SSL_CERT_DIR` path independent of the image regime; curl still works via the PEM.

## Risks / open items

- Confirm per-component trust config (galera/redis/memcached/httpd → explicit CA →
  mounted PEM; python `requests`/system vs `ssl` default).
- `openssl` dependency (runtime + CI) — or do D2.
- Pin nothing extra: PEM public set still from the operator image (unchanged skew).
- Rollback is trivial: the new Secret is additive; `combined-ca-bundle` is unchanged.
