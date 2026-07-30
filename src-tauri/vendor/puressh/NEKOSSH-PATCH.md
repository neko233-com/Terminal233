# NekoSSH puressh patch

This directory vendors the crates.io `puressh` 0.1.3 source from upstream
commit `39b9411c8eff369195777f3c1f349a85db46328f`. The original crate archive
SHA-256 is
`f0901b3b4995c5ace9b1ea6c066c4bdbb0e95660be809a885a1dd319f78b5072`.

NekoSSH carries two protocol-interoperability fixes:

- RFC 8308 section 2.2 defines `ext-info-c` and `ext-info-s` as independent,
  directional receive indicators. Upstream 0.1.3 incorrectly required both
  markers, causing compliant servers such as ssh2js to fail with
  `unexpected SSH_MSG_EXT_INFO`. The vendored transport tracks send and
  receive eligibility separately.
- ssh2js echoes the RSA public-key blob format name `ssh-rsa` in
  `SSH_MSG_USERAUTH_PK_OK` after an `rsa-sha2-*` probe. The client accepts
  this specific, non-downgrading echo while still signing and sending the
  final request with the originally selected RSA-SHA2 algorithm.

NekoSSH also hardens secret lifetime in the vendored client:

- `PrivateKey` zeroizes its private fields on drop and redacts `Debug`.
- decoded private-key DER/OpenSSH plaintext, bcrypt-derived material, cipher
  key/IV buffers, Ed25519 seed copies, and authentication payloads use
  zeroize-on-drop storage.

The dependency is built with `default-features = false` and only the `std`,
`client`, and `multichannel` Rust features (with the implied `alloc` feature).
NekoSSH removes the unused optional C ABI module and feature entirely. The
dependency gate verifies the exact feature set and that the active Windows
SSH/crypto graph contains no native C/ASM implementation, `*-sys`, `cc`,
CMake, OpenSSL, libssh2, ring, or aws-lc package. `purecrypto` directly invokes
the Windows `ProcessPrng` system CSPRNG through the OS ABI; this is an
operating-system service, not a bundled C SSH or cryptography runtime.

The upstream test-only PEM private-key fixtures are intentionally omitted to
keep source-control secret scanners unambiguous. Canonical MIT and Apache-2.0
license texts are included alongside the vendored source.
