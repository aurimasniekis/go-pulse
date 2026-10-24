<template>
  <figure class="pl-flow">
    <svg viewBox="0 0 940 390" role="img" aria-labelledby="pl-flow-title">
      <title id="pl-flow-title">
        Tools connect straight to NATS on port 4222 with their own credentials. Browsers and webhook senders come in
        through Cloudflare to pulsed on port 443. pulsed runs the dashboard, rollups, bans and alerts, and sends
        backups, notifications and outbound webhooks. The operator reaches the dashboard through an SSH tunnel.
      </title>
      <defs>
        <marker id="pl-arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto">
          <path d="M0,0 L10,5 L0,10 z" class="pl-flow-arrowhead" />
        </marker>
      </defs>

      <!-- sources -->
      <rect x="10" y="20" width="200" height="64" rx="12" class="pl-flow-box" />
      <text x="110" y="47" class="pl-flow-title">Webhook senders</text>
      <text x="110" y="70" class="pl-flow-sub">GitHub · Stripe · HMAC</text>

      <rect x="10" y="104" width="200" height="64" rx="12" class="pl-flow-box" />
      <text x="110" y="131" class="pl-flow-title">Browsers, HTTP</text>
      <text x="110" y="154" class="pl-flow-sub">tokens · wss:// /ws</text>

      <rect x="10" y="188" width="200" height="64" rx="12" class="pl-flow-box" />
      <text x="110" y="215" class="pl-flow-title">Tools, bots, agents</text>
      <text x="110" y="238" class="pl-flow-sub">own .creds, own grants</text>

      <!-- cloudflare -->
      <rect x="270" y="20" width="150" height="148" rx="12" class="pl-flow-box" />
      <text x="345" y="92" class="pl-flow-title">Cloudflare</text>
      <text x="345" y="116" class="pl-flow-sub">WAF · rate limit</text>
      <line x1="210" y1="52" x2="266" y2="52" class="pl-flow-line" marker-end="url(#pl-arrow)" />
      <line x1="210" y1="136" x2="266" y2="136" class="pl-flow-line" marker-end="url(#pl-arrow)" />

      <!-- server -->
      <rect x="490" y="10" width="250" height="280" rx="14" class="pl-flow-box pl-flow-main" />
      <text x="615" y="40" class="pl-flow-title">One VPS</text>
      <rect x="510" y="60" width="210" height="90" rx="10" class="pl-flow-box" />
      <text x="615" y="88" class="pl-flow-title">pulsed</text>
      <text x="615" y="110" class="pl-flow-sub">API · dashboard · bans</text>
      <text x="615" y="130" class="pl-flow-sub">rollups · alerts · SQLite</text>
      <rect x="510" y="170" width="210" height="100" rx="10" class="pl-flow-box" />
      <text x="615" y="200" class="pl-flow-title">NATS JetStream</text>
      <text x="615" y="222" class="pl-flow-sub">EVENTS · KV · objects</text>
      <text x="615" y="242" class="pl-flow-sub">years of history</text>
      <line x1="420" y1="105" x2="506" y2="105" class="pl-flow-line" marker-end="url(#pl-arrow)" />
      <text x="463" y="93" class="pl-flow-label">443</text>
      <text x="463" y="126" class="pl-flow-label">mTLS</text>
      <line x1="210" y1="220" x2="506" y2="220" class="pl-flow-line" marker-end="url(#pl-arrow)" />
      <text x="350" y="210" class="pl-flow-label">4222, TLS, direct</text>

      <!-- outputs -->
      <rect x="790" y="20" width="140" height="70" rx="12" class="pl-flow-box" />
      <text x="860" y="50" class="pl-flow-title">S3</text>
      <text x="860" y="74" class="pl-flow-sub">encrypted backups</text>
      <rect x="790" y="110" width="140" height="70" rx="12" class="pl-flow-box" />
      <text x="860" y="140" class="pl-flow-title">Alerts</text>
      <text x="860" y="164" class="pl-flow-sub">Telegram · Discord</text>
      <rect x="790" y="200" width="140" height="70" rx="12" class="pl-flow-box" />
      <text x="860" y="230" class="pl-flow-title">Your services</text>
      <text x="860" y="254" class="pl-flow-sub">outbound webhooks</text>
      <line x1="740" y1="55" x2="786" y2="55" class="pl-flow-line" marker-end="url(#pl-arrow)" />
      <line x1="740" y1="145" x2="786" y2="145" class="pl-flow-line" marker-end="url(#pl-arrow)" />
      <line x1="740" y1="235" x2="786" y2="235" class="pl-flow-line" marker-end="url(#pl-arrow)" />

      <!-- operator -->
      <rect x="490" y="320" width="250" height="60" rx="12" class="pl-flow-box" />
      <text x="615" y="345" class="pl-flow-title">Operator</text>
      <text x="615" y="368" class="pl-flow-sub">ssh -L → dashboard</text>
      <line x1="615" y1="320" x2="615" y2="294" class="pl-flow-line" marker-end="url(#pl-arrow)" />
    </svg>
  </figure>
</template>

<style scoped>
.pl-flow {
  margin: 24px 0;
}
svg {
  width: 100%;
  height: auto;
  font-family: var(--vp-font-family-base);
}
.pl-flow-box {
  fill: var(--vp-c-bg-soft);
  stroke: var(--vp-c-divider);
  stroke-width: 1.5;
}
.pl-flow-main {
  fill: var(--vp-c-brand-soft);
  stroke: var(--vp-c-brand-1);
  stroke-width: 2;
}
.pl-flow-title {
  text-anchor: middle;
  font-size: 18px;
  font-weight: 600;
  fill: var(--vp-c-text-1);
}
.pl-flow-sub {
  text-anchor: middle;
  font-size: 13px;
  fill: var(--vp-c-text-2);
}
.pl-flow-label {
  text-anchor: middle;
  font-size: 13px;
  fill: var(--vp-c-text-2);
}
.pl-flow-line {
  stroke: var(--vp-c-text-3);
  stroke-width: 2;
}
.pl-flow-arrowhead {
  fill: var(--vp-c-text-3);
}
</style>
