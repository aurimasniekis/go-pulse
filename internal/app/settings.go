package app

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/aurimasniekis/go-pulse/internal/api"
	"github.com/aurimasniekis/go-pulse/internal/config"
)

// configSummary lists the effective configuration for the Settings page. Secrets appear only as set / not set.
func configSummary(c *config.Config) []api.ConfigSection {
	v := func(env, val string) api.ConfigItem {
		if val == "" {
			val = "—"
		}
		return api.ConfigItem{Env: env, Value: val}
	}
	b := func(env string, x bool) api.ConfigItem { return v(env, strconv.FormatBool(x)) }
	secret := func(env, val string) api.ConfigItem {
		s := "not set"
		if val != "" {
			s = "set"
		}
		return api.ConfigItem{Env: env, Value: s, Secret: true}
	}
	list := func(env string, xs []string) api.ConfigItem { return v(env, strings.Join(xs, ", ")) }
	return []api.ConfigSection{
		{Title: "Core", Items: []api.ConfigItem{v("PULSE_PROFILE", c.Profile), v("PULSE_LOG_LEVEL", c.LogLevel), v("PULSE_DATA_DIR", c.Layout.DataDir),
			v("PULSE_NATS_DIR", c.Layout.NATSDir), secret("PULSE_MASTER_KEY_FILE", string(c.MasterKey))}},
		{Title: "NATS", Items: []api.ConfigItem{v("PULSE_NATS_URL", c.NATSURL), v("PULSE_NATS_TLS_NAME", c.NATSTLSName), v("PULSE_RAW_HOST", c.RawHost),
			v("PULSE_PUBLIC_NATS_URL", c.PublicNATSURL), v("PULSE_NATS_WEBSOCKET_URL", c.NATSWebsocketURL),
			v("PULSE_EVENTS_MAX_BYTES", fmt.Sprint(c.EventsMaxBytes))}},
		{Title: "Listeners and access", Items: []api.ConfigItem{v("PULSE_PUBLIC_ADDR", c.PublicAddr), v("PULSE_PUBLIC_URL", c.PublicURL),
			v("PULSE_ADMIN_ADDR", c.AdminAddr), v("PULSE_METRICS_ADDR", c.MetricsAddr), list("PULSE_ADMIN_HOSTS", c.AdminHosts),
			v("PULSE_ORIGIN_CERT_FILE", c.OriginCertFile), v("PULSE_AOP_CA_FILE", c.AOPCAFile), list("PULSE_OUTBOUND_ALLOW", c.OutboundAllow)}},
		{Title: "Dashboard sign-in", Items: []api.ConfigItem{b("PULSE_ADMIN_PUBLIC", c.AdminPublic), v("PULSE_ADMIN_PUBLIC_HOST", c.AdminPublicHost),
			v("PULSE_ADMIN_URL", c.AdminURL), v("PULSE_SESSION_IDLE", c.SessionIdle.String()), v("PULSE_SESSION_MAX", c.SessionMax.String()),
			v("PULSE_REAUTH_WINDOW", c.ReauthWindow.String()), list("PULSE_MFA_REQUIRED_ROLES", c.MFARequiredRoles)}},
		{Title: "Bans", Items: []api.ConfigItem{v("PULSE_BAN_THRESHOLD", strconv.Itoa(c.BanThreshold)), v("PULSE_BAN_WINDOW", c.BanWindow.String()),
			v("PULSE_BAN_DURATION", c.BanDuration.String()), v("PULSE_BAN_MAX", c.BanMax.String()), list("PULSE_BAN_ALLOW", c.BanAllow),
			list("PULSE_HOST_IPS", c.HostIPs), b("PULSE_CF_RANGES_REFRESH", c.CFRefresh), v("PULSE_CF_BANLIST_ID", c.CFBanListID),
			secret("PULSE_CF_BANLIST_TOKEN_FILE", c.CFBanListToken)}},
		{Title: "ACME", Items: []api.ConfigItem{b("PULSE_ACME_ENABLED", c.ACMEEnabled), v("PULSE_ACME_DIRECTORY", c.ACMEDirectory),
			v("PULSE_ACME_DNS_PROVIDER", c.ACMEDNSProvider), secret("PULSE_ACME_CF_TOKEN_FILE", c.ACMECFToken)}},
		{Title: "Alerts", Items: []api.ConfigItem{v("PULSE_ALERT_INTERVAL", c.AlertInterval.String()), v("PULSE_ALERT_REPEAT", repeat(c)),
			secret("PULSE_TELEGRAM_BOT_TOKEN_FILE", c.TelegramToken), v("PULSE_TELEGRAM_CHAT_ID", c.TelegramChatID),
			v("PULSE_TELEGRAM_MIN_SEVERITY", c.TelegramMinSeverity), secret("PULSE_DISCORD_WEBHOOK_URL_FILE", c.DiscordWebhookURL),
			v("PULSE_DISCORD_MIN_SEVERITY", c.DiscordMinSeverity)}},
		{Title: "Backups", Items: []api.ConfigItem{b("PULSE_BACKUP_ENABLED", c.BackupEnabled), v("PULSE_BACKUP_AT", c.BackupAt),
			v("PULSE_BACKUP_KEEP_DAILY", strconv.Itoa(c.BackupKeepDaily)), v("PULSE_BACKUP_KEEP_WEEKLY", strconv.Itoa(c.BackupKeepWeekly)),
			v("PULSE_BACKUP_KEEP_MONTHLY", strconv.Itoa(c.BackupKeepMonthly)), v("PULSE_BACKUP_S3_ENDPOINT", c.BackupS3Endpoint),
			v("PULSE_BACKUP_S3_BUCKET", c.BackupS3Bucket), v("PULSE_BACKUP_S3_PREFIX", c.BackupS3Prefix), v("PULSE_BACKUP_S3_LOOKUP", c.BackupS3Lookup),
			secret("PULSE_BACKUP_S3_ACCESS_KEY_FILE", c.BackupS3AccessKey), secret("PULSE_BACKUP_S3_SECRET_KEY_FILE", c.BackupS3SecretKey)}},
	}
}

func repeat(c *config.Config) string {
	if c.AlertRepeat < 0 {
		return "off"
	}
	return c.AlertRepeat.String()
}
