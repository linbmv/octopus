# Database backup encryption

Octopus can export either the existing plaintext JSON dump or a password-based,
authenticated encrypted envelope. Encryption changes only the outer transport:
after decryption, the payload is processed with the same database-dump rules as
plaintext JSON. Current exports use payload version 2 with stable UUIDs and
explicit relationships. Legacy payload version 1 remains readable, but retains
its `empty_target_restore` semantics and cannot be merged into a populated
business/statistics/log database.

## HTTP API

`GET /api/v1/setting/export` behaves as follows:

- With no backup password, it returns `application/json` and a `.json` file.
- With `X-Octopus-Backup-Password`, it returns
  `application/vnd.octopus.backup-encrypted` and an `.octopus-backup` file.
- Both forms send `Cache-Control: no-store`. The existing `include_logs` and
  `include_stats` query flags are unchanged.

`POST /api/v1/setting/import` auto-detects the encrypted envelope by its magic
bytes. Plain JSON remains compatible and needs no password. An encrypted raw
body takes its password only from `X-Octopus-Backup-Password`; a multipart
upload may use that header or one `password` form field together with one
`file` field. Supplying both password sources, duplicate fields, or a
`password` URL query parameter is rejected.

Payload version 2 accepts two non-secret query parameters:

- `dry_run=true` validates the complete payload and reports planned creates,
  updates, skips, deletes, conflicts, and unresolved references without writing
  the database or refreshing runtime caches.
- `conflict_policy=reject|skip|replace|merge` selects UUID collision behavior.
  The default is `reject`. `skip` preserves matching target rows, `merge`
  updates matching rows while preserving target-only channel keys and group
  items, and `replace` updates matching rows and removes those target-only child
  rows below imported channels/groups. No strategy deletes unrelated top-level
  target entities.

Source numeric IDs are never reused for a version 2 incremental import. Octopus
maps Channel, Channel Key, Group, Group Item, and API Key relationships by UUID,
then remaps optional channel/API-key statistics and historical relay attempt
references to the target IDs. A UUID and a unique natural key resolving to two
different target rows is reported as unresolved rather than guessed. The
combined group graph is checked for cycles and maximum nesting depth, including
target-only items retained by `merge`.

All version 2 writes, relationship remapping, child synchronization, settings,
statistics, and logs run in one transaction. A late error rolls everything
back. Runtime caches are refreshed only after a successful commit. Repeated
imports are deterministic: `reject` fails closed, `skip` is a no-op for matching
rows, and `merge`/`replace` update in place using the stable UUID.

Passwords must be 8–1024 bytes. They are removed from the in-process request
header/form map as soon as they are copied and are never included in Octopus
logs or error responses. Reverse proxies, tracing agents, and load balancers
must also redact `X-Octopus-Backup-Password`; Octopus cannot sanitize logs made
before a request reaches it. Do not put the password in a URL, command-line
argument, shell history, ticket, or CI artifact.

For a shell client, pass secrets to curl through standard input instead of its
argument list. This example deliberately keeps both credentials out of the URL
and process arguments:

```bash
read -rsp 'Octopus login token: ' OCTOPUS_TOKEN; echo
read -rsp 'Backup password: ' OCTOPUS_BACKUP_PASSWORD; echo
printf 'header = "Authorization: Bearer %s"\nheader = "X-Octopus-Backup-Password: %s"\n' \
  "$OCTOPUS_TOKEN" "$OCTOPUS_BACKUP_PASSWORD" |
  curl --config - --fail --silent --show-error \
    'http://127.0.0.1:8080/api/v1/setting/export?include_logs=false&include_stats=true' \
    --output octopus-backup.octopus-backup
unset OCTOPUS_TOKEN OCTOPUS_BACKUP_PASSWORD
```

Use the same header mechanism with `--request POST --data-binary
@octopus-backup.octopus-backup` to import an encrypted raw body. Add
`?dry_run=true&conflict_policy=reject` for a version 2 preview, then repeat with
`dry_run=false` to commit. Keep a tested, access-controlled copy of the
password: there is no recovery or bypass when it is lost.

## Envelope version 1

All integer fields use network byte order (big endian). The fixed header and
variable fields are:

| Offset | Size | Field | Version 1 value |
| ---: | ---: | --- | --- |
| 0 | 8 | Magic | ASCII `OCTOBKUP` |
| 8 | 1 | Envelope version | `1` |
| 9 | 1 | KDF identifier | `1` (scrypt) |
| 10 | 1 | Cipher identifier | `1` (AES-256-GCM) |
| 11 | 1 | Reserved flags | `0` |
| 12 | 4 | scrypt N | `32768` |
| 16 | 4 | scrypt r | `8` |
| 20 | 4 | scrypt p | `1` |
| 24 | 2 | Salt length | `16` |
| 26 | 2 | Nonce length | `12` |
| 28 | 8 | Plaintext length | Bounded JSON byte length |
| 36 | 16 | Salt | Cryptographically random per export |
| 52 | 12 | GCM nonce | Cryptographically random per export |
| 64 | variable | Ciphertext and tag | Plaintext length + 16 bytes |

The complete 64-byte prefix (header, salt, and nonce) is AES-GCM additional
authenticated data. Therefore version, algorithms, work factors, lengths,
salt, nonce, and ciphertext are authenticated together. An incorrect password
and modified authenticated data/ciphertext deliberately return the same error.

Version 1 accepts exactly the documented scrypt parameters. Import validates
them and the declared plaintext/envelope lengths before running scrypt, so an
untrusted file cannot request an attacker-selected KDF work factor. New work
factors or algorithms require a new envelope version rather than silently
changing version 1 interpretation.

## Size and memory boundaries

Plaintext import and export are capped at 64 MiB. The encrypted envelope adds
exactly 80 bytes (64-byte authenticated prefix plus the 16-byte GCM tag), and a
multipart request receives a separate 1 MiB framing allowance. Declared and
actual lengths must match exactly; oversized, truncated, trailing, or unknown
parameter encodings fail closed.

Plain JSON serialization remains table-by-table streaming with a bounded
writer. The HTTP handler first writes it to a `0600` temporary spool, unlinks
that open file immediately on operating systems that support it, and starts the
200 response only after serialization succeeds. Its exact `Content-Length`
makes a later network short write detectable instead of returning an apparently
successful truncated JSON backup. The spool is closed and removed on every
normal error/return path.

AES-GCM authenticates a whole message, so encrypted export and import are not
claimed to be end-to-end streaming. At the configured maximum, the encryption
path can hold roughly one 64 MiB plaintext plus one 64 MiB envelope, while
scrypt uses roughly 32 MiB of working memory, in addition to normal Go/HTTP
overhead. Size the instance accordingly or lower the constants in a custom
build. The implementation never allocates from an unvalidated envelope length.

Encryption protects a backup at rest, but it does not remove the secrets inside
the decrypted JSON. Continue to restrict backup access, use TLS in transit,
avoid untrusted artifact stores, and rotate channel/API credentials after any
plaintext or password exposure.
