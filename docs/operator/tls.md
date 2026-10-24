# TLS

Pulse has three TLS surfaces. The System page lists every certificate with its expiry, and the `cert_expiry` alert
fires 14 days before any of them expires.

| Surface | Certificate | Renewal |
|---|---|---|
| Public listener (443 → 8443, behind Cloudflare) | Cloudflare Origin CA certificate (`PULSE_ORIGIN_CERT_FILE` / `_KEY_FILE`), client CA for Authenticated Origin Pulls (`PULSE_AOP_CA_FILE`) | Long-lived; replace the files and pulsed reloads on change (mtime). Terraform outputs both. |
| Raw NATS (4222) | Let's Encrypt for the raw hostname (ACME DNS-01, `PULSE_ACME_*`) in front of the internal bootstrap leaf | pulsed renews at 30 days left (or the last third of shorter lifetimes), reloads NATS and checks the served serial ([ADR-0032](../DECISIONS.md)). |
| pulsed → NATS (inside the compose network) | Internal CA leaf for `nats` | `pulsed init` renews it; pulsed always verifies SNI `nats` against the internal CA. |

## Notes

- NATS picks the certificate by SNI: clients that dial the raw hostname get Let's Encrypt, and pulsed gets the internal
  leaf. Until ACME succeeds, clients must trust the internal CA. The System page has it under "Connect → Internal CA".
- AOP: the prod profile refuses to start the public listener without the AOP CA. Without a client certificate
  that chains to it, requests never reach a handler, and `CF-Connecting-IP` is trusted only from Cloudflare peers that
  passed AOP.
- A failed ACME renewal keeps serving the old certificate. The System page shows the error under "Let's Encrypt".
