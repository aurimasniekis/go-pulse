# Configuration contract

> This file is the contract between the pulse image and the deployment repository (`events-wan-lt`).
> Every variable is `PULSE_*`. Secrets use a `*_FILE` variant pointing at a compose secret.
> Filled in per milestone; variables marked *(planned)* are not implemented yet.

| Variable | Default | Milestone | Meaning |
|---|---|---|---|
| `PULSE_PROFILE` | `prod` | M1 | `dev` or `prod`. Prod refuses insecure settings (PL-OPS-004). |
| `PULSE_DATA_DIR` | `/data/pulsed` | M1 | pulsed volume (keys.enc, pulse.db, creds). |
| `PULSE_NATS_DIR` | `/data/nats` | M1 | Volume shared with the nats container. |
| `PULSE_MASTER_KEY_FILE` | — | M1 | 32-byte master key (hex or base64) from a compose secret. Required. |
| `PULSE_HEALTHCHECK_URL` | `http://127.0.0.1:9464/readyz` | M0 | URL probed by `pulsed healthcheck`. |
