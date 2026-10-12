# Explicit secret-store encryption key

Secret CRUD and any consumer that needs to decrypt stored secrets require the
operator-supplied `ARCENTRA_SECRET_ENCRYPTION_KEY` environment variable. Its value
must be strict standard Base64 encoding of exactly 32 bytes (AES-256). There is no
built-in key, key generation, fallback, truncation, or automatic key rotation.
Missing/invalid configuration fails encryption and decryption closed. Listing
masked metadata remains possible. The value must not be committed, logged,
returned by an API, or included in a delivery bundle.

Supply the value through the deployment's existing approved secret-management
mechanism, consistently across controller processes and restarts. Keep a secure
backup according to the operator's own policy. Changing or losing the key makes
existing ciphertext unreadable; existing records require their matching key.
This change does not re-encrypt or migrate any stored data. Do not change a key
on a running installation without an explicit operator-reviewed migration plan.

The previous constructor used a 34-byte literal, which Go's AES implementation
rejects. This fix does not assume that externally imported or historically
created ciphertext can be recovered with any new key. Copilot refuses to send
context when applicable stored secrets cannot be decrypted for redaction.

The ciphertext format remains Base64(nonce || AES-GCM ciphertext-and-tag), with a
fresh cryptographically random nonce per write. Rotate keys before the AES-GCM
random-nonce limit of 2^32 encryptions under one key; automated rotation/key IDs
are not implemented. See the official [AES key-size contract](https://pkg.go.dev/crypto/aes#NewCipher)
and [GCM nonce requirements](https://pkg.go.dev/crypto/cipher#NewGCM).

Tests use fixed, clearly synthetic fixture keys in isolated process environments.
No real deployment key or credential has been created or configured by this work.
