// saas_custom_domain_recipe — read-only MCP tool that returns the
// end-to-end blueprint for building "bring your own domain" support
// in a multi-tenant SaaS app deployed on myserver.
//
// Why this exists as a SEPARATE tool (rather than docs in another
// tool's description): in practice the AI editor only reads a tool
// description when it's already considering calling that tool. The
// 4-step blueprint (create_app_token → redeploy → call
// /applications/{id}/domains from inside the container → show DNS
// to the tenant) crosses four other tools. A SaaS author asking
// "how do I let my customers bring their own domain?" wouldn't
// reliably trip over the assembly. One named tool that bundles the
// whole flow makes the discovery one-shot.
//
// Optional app_id: when supplied, the recipe is hydrated with the
// real app id + hostname so the snippets are copy-paste-ready.

package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

func handleSaaSCustomDomainRecipe(api *apiClient, args json.RawMessage) (string, error) {
	var p struct {
		AppID int64 `json:"app_id,omitempty"`
	}
	if len(args) > 0 {
		_ = json.Unmarshal(args, &p)
	}

	// Defaults for the generic blueprint. If app_id is provided we
	// override these with the real values so the snippets become
	// directly executable instead of placeholdered.
	appIDLit := "<APP_ID>"
	saasFQDN := "<your-saas-app.example.com>"
	if p.AppID > 0 && api != nil {
		app, err := api.getApp(p.AppID)
		if err != nil {
			// Don't fail — fall back to the generic recipe and note
			// the lookup miss inline. A SaaS-author audience would
			// rather get the playbook than a 404.
			saasFQDN = fmt.Sprintf("<lookup-failed: %v>", err)
		} else {
			appIDLit = fmt.Sprintf("%d", app.ID)
			if app.FQDN != "" {
				saasFQDN = app.FQDN
			}
		}
	}

	var b strings.Builder
	fmt.Fprintln(&b, "Multi-tenant SaaS custom-domain blueprint")
	fmt.Fprintln(&b, "==========================================")
	fmt.Fprintln(&b, "")
	fmt.Fprintln(&b, "Goal: your customers (tenants) point their own hostname")
	fmt.Fprintln(&b, "(e.g. shop.acmecorp.com) at your SaaS app and reach their")
	fmt.Fprintln(&b, "tenant. Caddy auto-issues a TLS cert on first request.")
	fmt.Fprintln(&b, "")
	fmt.Fprintln(&b, "WHO CALLS WHAT")
	fmt.Fprintln(&b, "  - The TENANT updates DNS in their registrar.")
	fmt.Fprintln(&b, "  - YOUR APP (server-side, at runtime) calls myserver's API")
	fmt.Fprintln(&b, "    to register the hostname. Never the AI editor, never a")
	fmt.Fprintln(&b, "    human PAT — those don't survive customer self-serve.")
	fmt.Fprintln(&b, "")
	fmt.Fprintln(&b, "ONE-TIME SETUP (in this MCP session)")
	fmt.Fprintf(&b, "  1. Mint an app service token scoped to domain writes:\n")
	fmt.Fprintf(&b, "       create_app_token(\n")
	fmt.Fprintf(&b, "         app_id=%s,\n", appIDLit)
	fmt.Fprintf(&b, "         name=\"runtime-domains\",\n")
	fmt.Fprintf(&b, "         scopes=[\"domains:read\",\"domains:write\"],\n")
	fmt.Fprintf(&b, "         auto_inject=true,\n")
	fmt.Fprintf(&b, "       )\n")
	fmt.Fprintf(&b, "     auto_inject=true makes the next deploy splice these env\n")
	fmt.Fprintf(&b, "     vars into your container: MYSERVER_API_URL, MYSERVER_APP_ID,\n")
	fmt.Fprintf(&b, "     MYSERVER_APP_TOKEN. Without auto_inject, you must set\n")
	fmt.Fprintf(&b, "     MYSERVER_APP_TOKEN yourself via set_env_var.\n")
	fmt.Fprintln(&b, "")
	fmt.Fprintln(&b, "  2. Redeploy so the env vars actually land:")
	fmt.Fprintf(&b, "       deploy_app(app_id=%s)   # or: myserver up\n", appIDLit)
	fmt.Fprintln(&b, "")
	fmt.Fprintln(&b, "  3. Sanity-check the runtime contract:")
	fmt.Fprintf(&b, "       app_runtime_env(app_id=%s)\n", appIDLit)
	fmt.Fprintln(&b, "     Confirms MYSERVER_APP_TOKEN is in the injected set.")
	fmt.Fprintln(&b, "")
	fmt.Fprintln(&b, "RUNTIME PATH (inside your SaaS app, per tenant)")
	fmt.Fprintln(&b, "")
	fmt.Fprintln(&b, "  When tenant submits hostname (e.g. 'shop.acmecorp.com'):")
	fmt.Fprintln(&b, "")
	fmt.Fprintln(&b, "    A) Store it in your DB as 'pending'.")
	fmt.Fprintln(&b, "")
	fmt.Fprintln(&b, "    B) Show the tenant DNS instructions (see DNS section below).")
	fmt.Fprintln(&b, "")
	fmt.Fprintln(&b, "    C) When the tenant confirms DNS is set, POST to myserver:")
	fmt.Fprintln(&b, "")
	fmt.Fprintln(&b, "       curl -X POST \\")
	fmt.Fprintln(&b, "         -H \"Authorization: Bearer $MYSERVER_APP_TOKEN\" \\")
	fmt.Fprintln(&b, "         -H \"Content-Type: application/json\" \\")
	fmt.Fprintln(&b, "         -d '{\"hostname\":\"shop.acmecorp.com\"}' \\")
	fmt.Fprintln(&b, "         \"$MYSERVER_API_URL/api/v1/applications/$MYSERVER_APP_ID/domains\"")
	fmt.Fprintln(&b, "")
	fmt.Fprintln(&b, "       This endpoint is idempotent — safe to call from a")
	fmt.Fprintln(&b, "       verification retry loop. 200 == hostname is now wired.")
	fmt.Fprintln(&b, "")
	fmt.Fprintln(&b, "    D) Caddy issues a TLS cert on the first HTTPS request to")
	fmt.Fprintln(&b, "       the new hostname. No extra call needed.")
	fmt.Fprintln(&b, "")
	fmt.Fprintln(&b, "    E) Your app must route incoming requests by Host header to")
	fmt.Fprintln(&b, "       the right tenant — myserver hands you the raw Host;")
	fmt.Fprintln(&b, "       multi-tenant routing is your code's job, not Caddy's.")
	fmt.Fprintln(&b, "")
	fmt.Fprintln(&b, "  To REMOVE a hostname (tenant cancels, churn, etc.):")
	fmt.Fprintln(&b, "")
	fmt.Fprintln(&b, "       curl -X DELETE \\")
	fmt.Fprintln(&b, "         -H \"Authorization: Bearer $MYSERVER_APP_TOKEN\" \\")
	fmt.Fprintln(&b, "         \"$MYSERVER_API_URL/api/v1/applications/$MYSERVER_APP_ID/domains/shop.acmecorp.com\"")
	fmt.Fprintln(&b, "")
	fmt.Fprintln(&b, "       Idempotent — 204 on success or already-gone. Refuses to")
	fmt.Fprintln(&b, "       remove the LAST hostname (would make the app unreachable).")
	fmt.Fprintln(&b, "")
	fmt.Fprintln(&b, "DNS INSTRUCTIONS TO SHOW THE TENANT")
	fmt.Fprintln(&b, "")
	fmt.Fprintln(&b, "  Two options; CNAME is preferred (survives your IP changing):")
	fmt.Fprintln(&b, "")
	fmt.Fprintf(&b, "    Option A (CNAME, recommended):\n")
	fmt.Fprintf(&b, "      Type:  CNAME\n")
	fmt.Fprintf(&b, "      Name:  shop  (or @ for apex — apex CNAME may need ALIAS/ANAME)\n")
	fmt.Fprintf(&b, "      Value: %s\n", saasFQDN)
	fmt.Fprintln(&b, "")
	fmt.Fprintf(&b, "    Option B (A record, for apex without ALIAS support):\n")
	fmt.Fprintf(&b, "      Type:  A\n")
	fmt.Fprintf(&b, "      Name:  @\n")
	fmt.Fprintf(&b, "      Value: <the IP that %s resolves to>\n", saasFQDN)
	fmt.Fprintln(&b, "")
	fmt.Fprintln(&b, "  Caddy's HTTP-01 challenge fails until DNS resolves to your")
	fmt.Fprintln(&b, "  server. Tell the tenant to wait for DNS propagation BEFORE")
	fmt.Fprintln(&b, "  hitting your 'verify' button — typical 1-5 min, up to 48h")
	fmt.Fprintln(&b, "  worst case if their TTL is long.")
	fmt.Fprintln(&b, "")
	fmt.Fprintln(&b, "CODE SNIPPETS")
	fmt.Fprintln(&b, "")
	fmt.Fprintln(&b, "  Node (fetch):")
	fmt.Fprintln(&b, "    async function attachDomain(hostname) {")
	fmt.Fprintln(&b, "      const r = await fetch(")
	fmt.Fprintln(&b, "        `${process.env.MYSERVER_API_URL}/api/v1/applications/${process.env.MYSERVER_APP_ID}/domains`,")
	fmt.Fprintln(&b, "        { method: 'POST',")
	fmt.Fprintln(&b, "          headers: { 'Authorization': `Bearer ${process.env.MYSERVER_APP_TOKEN}`,")
	fmt.Fprintln(&b, "                     'Content-Type': 'application/json' },")
	fmt.Fprintln(&b, "          body: JSON.stringify({ hostname }) });")
	fmt.Fprintln(&b, "      if (!r.ok) throw new Error(`myserver ${r.status}: ${await r.text()}`);")
	fmt.Fprintln(&b, "    }")
	fmt.Fprintln(&b, "")
	fmt.Fprintln(&b, "  Python (requests):")
	fmt.Fprintln(&b, "    import os, requests")
	fmt.Fprintln(&b, "    def attach_domain(hostname):")
	fmt.Fprintln(&b, "        r = requests.post(")
	fmt.Fprintln(&b, "            f\"{os.environ['MYSERVER_API_URL']}/api/v1/applications/{os.environ['MYSERVER_APP_ID']}/domains\",")
	fmt.Fprintln(&b, "            headers={'Authorization': f\"Bearer {os.environ['MYSERVER_APP_TOKEN']}\"},")
	fmt.Fprintln(&b, "            json={'hostname': hostname}, timeout=10)")
	fmt.Fprintln(&b, "        r.raise_for_status()")
	fmt.Fprintln(&b, "")
	fmt.Fprintln(&b, "GOTCHAS")
	fmt.Fprintln(&b, "  - Token rotation: revoke the OLD token AFTER the next deploy")
	fmt.Fprintln(&b, "    completes (so the new token is live in the container first).")
	fmt.Fprintln(&b, "  - One auto_inject token per app. Re-minting auto_inject=true")
	fmt.Fprintln(&b, "    replaces the previous one.")
	fmt.Fprintln(&b, "  - Wildcards (e.g. '*.tenant.example.com') work; useful if you")
	fmt.Fprintln(&b, "    let tenants self-serve subdomains under a domain THEY own.")
	fmt.Fprintln(&b, "  - Don't expose MYSERVER_APP_TOKEN to the browser. Server-side")
	fmt.Fprintln(&b, "    only. Frontend POSTs to YOUR API, your API talks to myserver.")
	fmt.Fprintln(&b, "")
	fmt.Fprintln(&b, "RELATED TOOLS")
	fmt.Fprintln(&b, "  create_app_token, app_runtime_env, deploy_app,")
	fmt.Fprintln(&b, "  add_app_domain, list_app_domains, remove_app_domain")
	return b.String(), nil
}
