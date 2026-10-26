# Terraform

The [Terraform provider for Pulse](https://github.com/aurimasniekis/terraform-provider-pulse) manages a server's
configuration as code: clients and their grants, HTTP tokens, streams, consumers, KV and object buckets, webhooks,
alert rules, allowlist entries and bans, dashboard users, single sign-on providers and sign-in settings. Everything it
does goes through the [admin API](/reference/admin-api), with the same permissions and audit log as the dashboard.

The provider is on the [Terraform Registry](https://registry.terraform.io/providers/aurimasniekis/pulse) as
`aurimasniekis/pulse`. Provider 0.1 works with Pulse 0.1.0 and later.

## Access

The provider signs in with a [personal access token](/operators/users#personal-access-tokens). Use the `operator`
role, or `administrator` if the configuration manages users, single sign-on or sign-in settings. Run Terraform on the
server or through the SSH tunnel:

```sh
ssh -N -L 8080:127.0.0.1:8080 you@vps.example.com &
docker compose exec pulsed pulsed admin token --email terraform@example.com --create --role administrator --days 30
```

```hcl
terraform {
  required_providers {
    pulse = {
      source  = "aurimasniekis/pulse"
      version = "~> 0.1"
    }
  }
}

provider "pulse" {
  admin_url  = "http://127.0.0.1:8080"                  # or PULSE_ADMIN_URL
  token_file = pathexpand("~/.config/pulse/admin.token") # or PULSE_ADMIN_TOKEN
}
```

### From CI

A runner that cannot reach the tunnel (Atlantis, GitHub Actions on a self-hosted runner with a fixed address) can use
the public dashboard host instead, with a token that works only from the runner's addresses:

1. Set `PULSE_ADMIN_PUBLIC=true` and `PULSE_ADMIN_PUBLIC_TOKENS=true` on the server.
2. Mint a token for the runner's egress address (networks must be at least /16 or /48; `--cidr` is repeatable):

   ```sh
   docker compose exec pulsed pulsed admin token --email terraform@example.com --create --name terraform \
     --role operator --days 366 --cidr 203.0.113.7
   ```

3. Give it to the runner as `PULSE_ADMIN_TOKEN` and set `admin_url = "https://admin.events.example.com"` (your
   `PULSE_ADMIN_PUBLIC_HOST`).

Requests from any other address get 401 and count towards a ban. If the runner's address changes, mint a new token and
revoke the old one. If Cloudflare rules in front of the dashboard host challenge or block non-browser clients, exempt
`/api/admin/v1/` for the runner's address.

## Example

A tool with its own credentials and a durable consumer that resumes where it stopped:

```hcl
resource "pulse_client" "notifier" {
  name = "notifier"
  grants = [
    { action = "durable", subject = "evt.home.>", name = "alerts" },
  ]
}

resource "pulse_consumer" "notifier" {
  name            = "${pulse_client.notifier.id}_alerts" # what the durable grant allows
  filter_subjects = ["evt.home.>"]
  deliver_policy  = "new"
}

output "notifier_creds" {
  value     = pulse_client.notifier.creds
  sensitive = true
}
```

## Things to know

- **Secrets live in the state.** A client's `.creds`, an HTTP token and generated webhook secrets are shown once by
  Pulse, so the provider keeps them in the Terraform state (marked sensitive). Store the state somewhere private and
  encrypted. Rotate with `creds_version` or `secret_version`.
- **Destroying revokes.** Destroying a client or HTTP token revokes it and removes its record, so the name can be used
  again; the revocation itself stays.
- **Some changes replace.** HTTP tokens cannot be edited, and a consumer's start position cannot change: such changes
  replace the object (a replaced consumer starts over).
- **Drift is corrected.** Objects deleted in the dashboard are re-created on the next apply, and changed settings are
  put back.
- **Server settings are not managed here.** Environment variables (`PULSE_*`), the master key and certificates belong
  to the deployment ([configuration](/reference/configuration)).
