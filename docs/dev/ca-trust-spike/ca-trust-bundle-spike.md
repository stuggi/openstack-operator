# Spike: build the CA trust bundle with `update-ca-trust` (OSPRH-36600)

- **Spike:** OSPRH-36600 · **Issue:** OSPRH-16670
- **Question:** can we build the CA trust material with the OS's own `update-ca-trust`
  (in a container) and ship it via a Secret — and does that hold up on RHEL 10 / UBI 10?
- **Deliverables:** this report + PoC (`poc/`), the design (`rhel10-ca-trust-change.md`),
  the implementation plan (`implementation-plan.md`), and story stubs
  (`follow-up-stories.md`).

> **Timeline (authoritative — [RHEL-109484](https://issues.redhat.com/browse/RHEL-109484)):**
> the default single-file bundle removal is **postponed to RHEL 11.** RHEL 9 and
> **RHEL 10** keep the single-file bundles (symlinks restored; verified on UBI 10.2).
> So the current RHOSO mechanism works on RHEL 9 and 10 — **this work is forward-looking
> for RHEL 11**, prepared but not needed on current images.

## Answer

**Yes, feasible and the right direction.** The approach works, adds **blocklist
support** and OS-tooling correctness, and handles the eventual RHEL 11 trust-store
change with a small addition.

The change and the chosen adaptation are detailed in **`rhel10-ca-trust-change.md`**.
In short: RHEL 11 will move the default OpenSSL store to the **directory-hash** at
`/etc/pki/tls/certs` and drop the default single-file bundles. RHOSO adapts by keeping
today's PEM Secret **and** publishing a **small directory-hash Secret of only our CAs**,
consumed via `SSL_CERT_DIR=/etc/pki/tls/certs:<dir>` (preserves fast-start). No
`/etc/pki/tls/certs` overlay → no collision with per-service cert files. On RHEL 9/10
the PEM path already covers everything, so the directory-hash Secret is inert there.

## PoC (`poc/`)

UBI9 + `ca-certificates`; stages custom CAs into `/etc/pki/ca-trust/source/{anchors,
blocklist}`, runs `update-ca-trust`, packages output. Run: `cd poc && ./package-secret.sh`.

### Key empirical results (verified on UBI 9.4/9.6/9.8 + Fedora 44)

| Finding | Result |
|---|---|
| `update-ca-trust` regenerates all formats; custom CA in PEM + `java/cacerts` + hash-dir | ✅ (keytool-verified) |
| **Blocklist** removes a trusted root | ✅ |
| Path in ticket `…/sources/…` | ✏️ correct is `source` (singular) |
| curl CA source on rhel9 **and** rhel10 (fc44) | `…/extracted/pem/tls-ca-bundle.pem` (existing mount covers it) |
| rhel10 gap: openssl-default (python `ssl`) uses hash-dir (`cert.pem` gone) | closed by `SSL_CERT_DIR` |
| directory-hash in a Secret | ✅ flatten symlinks → regular `<hash>.<seq>` files; `-CApath` needs no symlinks (works through k8s projected-secret layout too) |
| small custom-only hash-dir + `SSL_CERT_DIR=/etc/pki/tls/certs:<dir>` | ✅ python TLS handshake trusts custom CA; public trust preserved; lazy lookup (fast-start) |
| service certs at `/etc/pki/tls/certs/<endpt>.crt` | convention; coexist with hash-dir (non-hash files ignored) — keep as-is |

## Conclusion & scope

- **Build (Option D — operator produces both, no Job):** keep building
  `combined-ca-bundle` (unchanged contract: `tls-ca-bundle.pem` +
  `internal-ca-bundle.pem`) in `ReconcileCAs`; additionally produce a small hash-dir
  Secret of our CAs (subject hash via `openssl x509 -hash`; needs `openssl` in the
  operator image). No builder image/Job/RBAC/gating — the PoC container proved the
  concept; the operator does it in-process.
- **Consume:** keep the PEM mount (curl + explicit-config infra: galera/redis/
  memcached/httpd); add `SSL_CERT_DIR` for openssl-default/python consumers on rhel10.
- **Deferred / out of scope:** Java `cacerts` (no JVM services), edk2, full dir overlay.
- Fallback if the small-dir path is impractical: `SSL_CERT_FILE=<extracted PEM>`
  (simpler, but loses fast-start).

See `rhel10-ca-trust-change.md` (design), `implementation-plan.md` (how to build &
test), `follow-up-stories.md` (breakdown).
