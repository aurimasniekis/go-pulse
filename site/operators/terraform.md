# Terraform

The [Terraform provider for Pulse](https://github.com/aurimasniekis/terraform-provider-pulse) manages a server's
configuration as code: clients and their grants, HTTP tokens, streams, consumers, KV and object buckets, webhooks,
alert rules, allowlist entries and bans, dashboard users, single sign-on providers and sign-in settings. Everything it
does goes through the [admin API](/reference/admin-api), with the same permissions and audit log as the dashboard.

::: warning Not on the Terraform Registry yet
Until the provider is published, build it from source (`make build` in its repository) and point Terraform at it with
`dev_overrides`, as its README describes.
:::

## Access

The provider signs in with a [personal access token](/operators/users#personal-access-tokens) with the
`administrator` role. Like the CLI's tokens, it works only on the loopback admin listener, so run Terraform on the
server or through the SSH tunnel:

```sh
ssh -N -L 8080:127.0.0.1:8080 you@vps.example.com &
docker compose exec pulsed pulsed admin token --email terraform@example.com --create --role administrator --days 30
```

```hcl
terraform {
  required_providers {
    pulse = {
      source = "aurimasniekis/pulse"
    }
  }
}

provider "pulse" {
  admin_url  = "http://127.0.0.1:8080"                  # or PULSE_ADMIN_URL
  token_file = pathexpand("~/.config/pulse/admin.token") # or PULSE_ADMIN_TOKEN
}
```

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
