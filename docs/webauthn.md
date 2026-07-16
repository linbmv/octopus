# Administrator WebAuthn / 2FA

WebAuthn is an optional second factor for the single administrator account. It is disabled by default and requires an explicit relying-party configuration. Once at least one credential is registered, a successful password check starts a five-minute WebAuthn ceremony instead of issuing a browser session or bearer token immediately.

## Configuration

Production origins must use HTTPS. Plain HTTP is accepted only for loopback development origins.

```json
{
  "webauthn": {
    "enabled": true,
    "rp_id": "octopus.example.com",
    "rp_display_name": "Octopus",
    "rp_origins": ["https://octopus.example.com"]
  }
}
```

`rp_id` is the effective domain without scheme or port. Every browser origin that may perform registration or login must appear in `rp_origins`. Configuration changes affect new ceremonies; restart the service before relying on a changed RP identity or origin set.

## Security behavior

- Enrollment and deletion require an authenticated Cookie/JWT session plus the current password. Cookie requests remain CSRF-protected.
- Registration and login require authenticator user verification, such as a device PIN, biometric check, or security-key PIN.
- Ceremony transactions are random, server-side, bounded to 1024 entries, expire after five minutes, are single-use, and are bound to the client IP and user agent.
- Password or username changes increment the account token version; ceremonies created under an older version are rejected.
- The complete WebAuthn credential record is stored so signature counters and authenticator flags are updated after every successful login.
- Version 1 database backups intentionally exclude both the administrator row and WebAuthn credentials. Restores retain the target instance's independent administrator and registered credentials.

If WebAuthn is disabled or no credentials are registered, password authentication keeps its existing behavior. Deleting the last credential therefore removes the second-factor requirement and always requires the current password.
