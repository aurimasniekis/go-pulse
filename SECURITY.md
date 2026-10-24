# Security policy

Pulse holds the signing keys for every client credential and years of event history. Please report vulnerabilities
privately.

## Reporting

- Use GitHub's private vulnerability reporting: "Report a vulnerability" on the repository's
  [Security tab](https://github.com/aurimasniekis/go-pulse/security/advisories/new).
- Do not open public issues for suspected vulnerabilities.
- Include affected versions, configuration, reproduction steps and impact. Never include real `.creds`, master keys,
  `keys.enc`, age identities, tokens or event data.

We aim to acknowledge reports within 3 working days and to agree on a disclosure date with you. Fixes ship for the
latest minor release line ([docs/VERSIONING.md](docs/VERSIONING.md)).

## Scope and guarantees

The threats Pulse defends against, and the residual risks it accepts, are in
[docs/THREAT_MODEL.md](docs/THREAT_MODEL.md) and on the [security page](https://aurimasniekis.github.io/go-pulse/security/).
In particular:

- The dashboard has no login (`PULSE_AUTH_MODE=none`). It binds to host loopback and is reached through an SSH
  tunnel; anyone with a shell on the host is an administrator.
- Root on the host can read the master key and `keys.enc`, and so mint any credential.
- Events are not encrypted at rest inside NATS; disk encryption is the host's job. Backups are age-encrypted.
- A client can read every event on the subjects it is granted.
- Nothing has had an external security review yet.

## Hardening references

- Deployment, secrets and the SSH tunnel: [docs/operator/deployment.md](docs/operator/deployment.md)
- Keys and credentials: [docs/operator/keys.md](docs/operator/keys.md)
- Firewall and bans: [docs/operator/firewall.md](docs/operator/firewall.md), [docs/operator/bans.md](docs/operator/bans.md)
- TLS and Cloudflare Authenticated Origin Pulls: [docs/operator/tls.md](docs/operator/tls.md)
