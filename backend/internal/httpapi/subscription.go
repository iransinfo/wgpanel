package httpapi

import (
	"context"
	"archive/zip"
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"html/template"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"wgpanel-api/internal/steering"
	"wgpanel-api/internal/store"
	"wgpanel-api/internal/wgkeys"
)

// newSubscriptionToken mints the per-account capability for GET /api/v1/sub/{token}:
// 24 random bytes hex-encoded (48 chars, 192 bits) - far beyond online-guessing reach,
// which is the entire security model of an unauthenticated capability URL.
func newSubscriptionToken() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

var errNodeMissingPublicKey = errors.New("node has no public key")

// renderPeerConfig renders the ready-to-import wg-quick config for one of an
// account's peers - shared by the admin/bot config endpoint and the subscription
// endpoint so the two can never drift apart on tunnel parameters. See
// handleGetAccountConfig's doc comments for why DNS and MTU=1280 are set the way
// they are - those hard-won notes live on the template below.
func (s *Server) renderPeerConfig(ctx context.Context, accountID string, ns *string, target store.AccountPeerWithNode) (string, error) {
	if target.NodePublicKey == nil || *target.NodePublicKey == "" {
		return "", errNodeMissingPublicKey
	}

	encryptedPriv, err := s.Store.GetAccountPrivateKey(ctx, accountID, ns)
	if err != nil {
		return "", err
	}
	privateKey, err := wgkeys.Decrypt(s.AccountKeyEncryptionKey, encryptedPriv)
	if err != nil {
		return "", err
	}

	// DNS is required (full tunnel makes the client's original resolver unreachable)
	// and MTU 1280 is the safe floor that avoids black-hole fragmentation on paths
	// whose real MTU is below WireGuard's optimistic default - both found the hard way
	// against real clients (see the original notes in handleGetAccountConfig's history,
	// docs/STORY-09/STORY-10). The DNS server itself is a panel setting (migration
	// 0018), because the right resolver depends on where the exit node egresses: the
	// Cloudflare default is unreachable on networks that filter it, which presents as
	// "connected but no internet" - the tunnel works, name resolution silently doesn't.
	dns := "1.1.1.1, 1.0.0.1"
	if settings, err := s.Store.GetSettings(ctx); err == nil && settings.ClientDNS != "" {
		dns = settings.ClientDNS
	} else if err != nil {
		// A settings read failure shouldn't block config delivery - fall back to the
		// historical default rather than failing the whole request.
		s.Logger.Warn("get_settings_for_config_dns_failed", "error", err)
	}

	// AllowedIPs carries BOTH families on purpose. Without ::/0, a dual-stack client
	// (most mobile carriers, many home ISPs) keeps its native IPv6 route outside the
	// tunnel, so every IPv6-capable site sees the client's real address while the user
	// believes they're fully tunneled - the classic commercial-VPN IPv6 leak. The nodes
	// currently egress IPv4-only, so including ::/0 makes the client route its IPv6 into
	// the tunnel where it is dropped (black-holed) rather than leaked: the client falls
	// back to IPv4 and no traffic escapes unprotected. If/when nodes gain real IPv6
	// egress (v6 address + NAT66), this line already carries it end to end - no client
	// reconfig needed.
	return fmt.Sprintf(`[Interface]
PrivateKey = %s
Address = %s/32
DNS = %s
MTU = 1280

[Peer]
PublicKey = %s
Endpoint = %s
AllowedIPs = 0.0.0.0/0, ::/0
PersistentKeepalive = 25
`, privateKey, target.AssignedIP, dns, *target.NodePublicKey, target.NodePublicEndpoint), nil
}

// resolveSubscriptionAccount is the shared front half of both /sub/{token} handlers:
// token -> account, with suspended/expired accounts refused. Writes the error
// response itself and returns ok=false when the caller should just return.
func (s *Server) resolveSubscriptionAccount(w http.ResponseWriter, r *http.Request) (store.Account, bool) {
	token := r.PathValue("token")
	account, err := s.Store.GetAccountBySubscriptionToken(r.Context(), token)
	if errors.Is(err, store.ErrAccountNotFound) {
		writeJSONError(w, http.StatusNotFound, "not_found", "unknown subscription")
		return store.Account{}, false
	}
	if err != nil {
		s.Logger.Error("subscription_lookup_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not resolve subscription")
		return store.Account{}, false
	}
	if account.Status != "active" {
		// The client app gets a clear, machine-readable reason instead of a config
		// that silently stopped handshaking (the agent already dropped the peer).
		writeJSONError(w, http.StatusForbidden, "account_suspended", "this account is currently suspended")
		return store.Account{}, false
	}
	return account, true
}

// handleSubscriptionConfig serves the account's current wg-quick config to anyone
// holding the subscription token - no panel credentials involved. Node choice:
// ?node_id= pins one explicitly; otherwise steering picks the currently-best node
// (optionally biased by ?region=), which is exactly what makes the URL a "always
// fetch a working config" endpoint rather than a static file.
func (s *Server) handleSubscriptionConfig(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	account, ok := s.resolveSubscriptionAccount(w, r)
	if !ok {
		return
	}

	peers, err := s.Store.ListAccountPeersWithNode(ctx, account.ID, nil)
	if err != nil {
		s.Logger.Error("list_account_peers_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not fetch account peers")
		return
	}
	if len(peers) == 0 {
		writeJSONError(w, http.StatusConflict, "no_peers", "this account has no node peers yet")
		return
	}

	download := r.URL.Query().Get("download")
	accept := r.Header.Get("Accept")

	// 1. Download all configs as a .zip file
	if download == "all" {
		buf := new(bytes.Buffer)
		zw := zip.NewWriter(buf)
		for _, p := range peers {
			cfg, err := s.renderPeerConfig(ctx, account.ID, nil, p)
			if err != nil {
				continue
			}
			fName := fmt.Sprintf("%s-%s.conf", confFilename(account.Label[:min(len(account.Label), 8)]), p.NodeName)
			f, err := zw.Create(fName)
			if err != nil {
				continue
			}
			_, _ = f.Write([]byte(cfg))
		}
		_ = zw.Close()

		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", confFilename(account.Label)+".zip"))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(buf.Bytes())
		return
	}

	// 2. Render HTML Dashboard for browser visits
	if download != "1" && strings.Contains(accept, "text/html") {
		formatBytes := func(b int64) string {
			const gb = 1000 * 1000 * 1000
			const mb = 1000 * 1000
			if b >= gb {
				val := float64(b) / float64(gb)
				if val == float64(int64(val)) {
					return fmt.Sprintf("%.0f GB", val)
				}
				return fmt.Sprintf("%.1f GB", val)
			}
			return fmt.Sprintf("%.1f MB", float64(b)/float64(mb))
		}

		usedStr := formatBytes(account.DataUsedBytes)
		quotaStr := "Unlimited"
		remainingStr := "Unlimited"
		pct := 0.0

		if account.DataQuotaBytes != nil && *account.DataQuotaBytes > 0 {
			quotaStr = formatBytes(*account.DataQuotaBytes)
			rem := *account.DataQuotaBytes - account.DataUsedBytes
			if rem < 0 {
				rem = 0
			}
			remainingStr = formatBytes(rem)
			pct = (float64(account.DataUsedBytes) / float64(*account.DataQuotaBytes)) * 100
			if pct > 100 {
				pct = 100
			}
		}

		expStr := "Never"
		if account.ExpiryAt != nil {
			expStr = account.ExpiryAt.Format("2006/01/02 15:04")
		}

		var nodeCards []subNodeCard
		for _, p := range peers {
			cfg, err := s.renderPeerConfig(ctx, account.ID, nil, p)
			if err != nil {
				continue
			}
			nodeCards = append(nodeCards, subNodeCard{
				NodeID:       p.NodeID,
				NodeName:     p.NodeName,
				AssignedIP:   p.AssignedIP,
				RawConfig:    cfg,
				Base64Config: base64.StdEncoding.EncodeToString([]byte(cfg)),
			})
		}

		data := struct {
			AccountID        string
			Label            string
			Status           string
			DataUsedStr      string
			DataQuotaStr     string
			DataRemainingStr string
			UsagePercent     float64
			ExpiryStr        string
			Nodes            []subNodeCard
		}{
			AccountID:        account.ID,
			Label:            account.Label,
			Status:           account.Status,
			DataUsedStr:      usedStr,
			DataQuotaStr:     quotaStr,
			DataRemainingStr: remainingStr,
			UsagePercent:     pct,
			ExpiryStr:        expStr,
			Nodes:            nodeCards,
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = subscriptionTmpl.Execute(w, data)
		return
	}

	// 3. Regular WireGuard client single config download
	target, ok := s.pickSubscriptionPeer(ctx, w, account.ID, peers, r.URL.Query().Get("node_id"), r.URL.Query().Get("region"))
	if !ok {
		return
	}

	config, err := s.renderPeerConfig(ctx, account.ID, nil, target)
	if errors.Is(err, errNodeMissingPublicKey) {
		writeJSONError(w, http.StatusConflict, "node_missing_public_key", "the selected node has no public key set yet")
		return
	}
	if err != nil {
		s.Logger.Error("render_subscription_config_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not render config")
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", confFilename(account.Label+"-"+target.NodeName)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(config))
}

// pickSubscriptionPeer chooses which of the account's peers to render: an explicit
// ?node_id= wins; otherwise the steering rank's recommendation. Falls back to the
// first peer if steering has no candidates (every node still missing its public key -
// the render step then reports that specific problem rather than a generic one).
func (s *Server) pickSubscriptionPeer(ctx context.Context, w http.ResponseWriter, accountID string, peers []store.AccountPeerWithNode, nodeIDParam, region string) (store.AccountPeerWithNode, bool) {
	if nodeIDParam != "" {
		for _, p := range peers {
			if p.NodeID == nodeIDParam {
				return p, true
			}
		}
		writeJSONError(w, http.StatusNotFound, "node_not_found", "this account has no peer on that node")
		return store.AccountPeerWithNode{}, false
	}

	candidates, err := s.Store.SteerCandidatesForAccount(ctx, accountID, nil)
	if err != nil {
		s.Logger.Error("steer_candidates_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not select a node")
		return store.AccountPeerWithNode{}, false
	}
	ranked := steering.Rank(candidates, region)
	if len(ranked) > 0 {
		for _, p := range peers {
			if p.NodeID == ranked[0].NodeID {
				return p, true
			}
		}
	}
	return peers[0], true
}

var confFilenameUnsafe = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

// confFilename derives a safe attachment filename from the account label. WireGuard
// clients derive the tunnel name from this, and wg-quick tunnel names max out at 15
// chars - keep the stem within that so imports don't fail or get truncated oddly.
func confFilename(label string) string {
	stem := confFilenameUnsafe.ReplaceAllString(strings.ToLower(label), "-")
	if len(stem) > 15 {
		stem = stem[:15]
	}
	// Trim AFTER truncating so a cut that lands on a separator doesn't leave a trailing
	// "-"/"." right before the extension (e.g. "very-long-name-.conf").
	stem = strings.Trim(stem, "-.")
	if stem == "" {
		stem = "wgpanel"
	}
	return stem + ".conf"
}

type subscriptionNodeResponse struct {
	NodeID      string `json:"node_id"`
	Name        string `json:"name"`
	Region      string `json:"region"`
	Online      bool   `json:"online"`
	Recommended bool   `json:"recommended"`
	// ConfigPath is the ready-to-fetch path for this specific node's config, so a
	// client app can offer a node picker without constructing URLs itself.
	ConfigPath string `json:"config_path"`
}

// handleSubscriptionNodes lets a client app enumerate the nodes available to this
// subscription (steering-ranked, best first) - the machine-readable companion to
// handleSubscriptionConfig's "just give me the best one".
func (s *Server) handleSubscriptionNodes(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	account, ok := s.resolveSubscriptionAccount(w, r)
	if !ok {
		return
	}

	candidates, err := s.Store.SteerCandidatesForAccount(ctx, account.ID, nil)
	if err != nil {
		s.Logger.Error("steer_candidates_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not list nodes")
		return
	}
	ranked := steering.Rank(candidates, r.URL.Query().Get("region"))

	out := make([]subscriptionNodeResponse, 0, len(ranked))
	for _, c := range ranked {
		out = append(out, subscriptionNodeResponse{
			NodeID:      c.NodeID,
			Name:        c.Name,
			Region:      c.Region,
			Online:      c.Online,
			Recommended: c.Recommended,
			ConfigPath:  "/api/v1/sub/" + account.SubscriptionToken + "?node_id=" + c.NodeID,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"nodes": out})
}

// handleRotateSubscriptionToken invalidates the account's current subscription URL
// and mints a new one - the recovery path for a leaked/shared link. Update-tier
// permission, same as every other account mutation.
func (s *Server) handleRotateSubscriptionToken(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	identity, _ := callerIdentityFromContext(ctx)
	ns := callerNamespaceArg(identity)

	newToken, err := newSubscriptionToken()
	if err != nil {
		s.Logger.Error("generate_subscription_token_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not generate token")
		return
	}

	account, err := s.Store.RotateSubscriptionToken(ctx, r.PathValue("id"), ns, newToken)
	if errors.Is(err, store.ErrAccountNotFound) {
		writeJSONError(w, http.StatusNotFound, "account_not_found", "no account with that id")
		return
	}
	if err != nil {
		s.Logger.Error("rotate_subscription_token_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not rotate subscription token")
		return
	}

	if err := s.Store.InsertAuditLog(ctx, auditActor(identity), "account.subscription_rotated", account.ID, nil, r.RemoteAddr); err != nil {
		s.Logger.Error("audit_log_failed", "error", err)
	}
	s.respondWithAccount(w, r, http.StatusOK, account, ns)
}


type subNodeCard struct {
	NodeID       string
	NodeName     string
	AssignedIP   string
	RawConfig    string
	Base64Config string
}

var subscriptionTmpl = template.Must(template.New("sub").Parse(`<!DOCTYPE html>
<html lang="en" class="dark">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>{{.Label}} - WireGuard Subscription</title>
    <script src="https://cdn.tailwindcss.com"></script>
    <script src="https://cdnjs.cloudflare.com/ajax/libs/qrcodejs/1.0.0/qrcode.min.js"></script>
    <script src="https://cdnjs.cloudflare.com/ajax/libs/jszip/3.10.1/jszip.min.js"></script>
    <link rel="preconnect" href="https://fonts.googleapis.com">
    <link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
    <link href="https://fonts.googleapis.com/css2?family=Plus+Jakarta+Sans:wght@400;500;600;700;800&family=JetBrains+Mono:wght@400;500;600&display=swap" rel="stylesheet">
    <style>
        body { font-family: 'Plus Jakarta Sans', sans-serif; }
        code, pre, .font-mono { font-family: 'JetBrains Mono', monospace; }
        .custom-scroll::-webkit-scrollbar { width: 5px; height: 5px; }
        .custom-scroll::-webkit-scrollbar-track { background: #0f172a; }
        .custom-scroll::-webkit-scrollbar-thumb { background: #334155; border-radius: 4px; }
        .custom-scroll::-webkit-scrollbar-thumb:hover { background: #475569; }
    </style>
</head>
<body class="bg-[#0b0f17] text-slate-200 min-h-screen p-4 sm:p-6 lg:p-8 antialiased selection:bg-cyan-500/20 selection:text-cyan-300">
    <div class="max-w-7xl mx-auto space-y-6">

        <!-- Top Header Bar -->
        <header class="flex flex-col md:flex-row md:items-center justify-between gap-4 bg-[#111827]/80 backdrop-blur-md border border-slate-800/80 rounded-2xl p-5 sm:p-6 shadow-xl">
            <div class="space-y-2">
                <div class="flex items-center gap-3 flex-wrap">
                    <h1 class="text-2xl sm:text-3xl font-extrabold tracking-tight text-white">{{.Label}}</h1>
                    {{if eq .Status "active"}}
                    <span class="inline-flex items-center px-3 py-0.5 rounded-full text-[10px] font-bold bg-emerald-500/10 text-emerald-400 border border-emerald-500/30 uppercase tracking-wider">Active</span>
                    {{else}}
                    <span class="inline-flex items-center px-3 py-0.5 rounded-full text-[10px] font-bold bg-rose-500/10 text-rose-400 border border-rose-500/30 uppercase tracking-wider">Suspended</span>
                    {{end}}
                </div>
                <div class="flex items-center gap-2 text-[10px] sm:text-[10px] text-slate-400">
                    <span class="text-slate-500">Account ID:</span>
                    <span class="font-mono text-slate-300 bg-slate-900/90 px-2 py-0.5 rounded border border-slate-800">{{.AccountID}}</span>
                    <button onclick="copyText('{{.AccountID}}', 'Account ID copied!')" class="p-1 text-slate-400 hover:text-cyan-400 transition" title="Copy ID">
                        <svg class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M8 16H6a2 2 0 01-2-2V6a2 2 0 012-2h8a2 2 0 012 2v2m-6 12h8a2 2 0 002-2v-8a2 2 0 00-2-2h-8a2 2 0 00-2 2v8a2 2 0 002 2z"/></svg>
                    </button>
                </div>
            </div>

            <div>
                <a href="?download=all" class="inline-flex items-center gap-2 bg-cyan-600 hover:bg-cyan-500 text-white font-semibold px-5 py-2.5 rounded-xl transition shadow-lg shadow-cyan-600/25 active:scale-95 text-sm sm:text-base">
                    <svg class="w-5 h-5" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 16v1a3 3 0 003 3h10a3 3 0 003-3v-1m-4-4l-4 4m0 0l-4-4m4 4V4"/></svg>
                    Download All (.conf files)
                </a>
            </div>
        </header>

        <!-- Stats Overview Grid -->
        <div class="grid grid-cols-1 md:grid-cols-3 gap-4">
            <!-- Data Usage -->
            <div class="bg-[#111827]/70 border border-slate-800/80 rounded-2xl p-5 shadow-lg flex flex-col justify-between">
                <div>
                    <div class="text-[10px] uppercase font-bold tracking-wider text-slate-400 mb-1">Data Usage</div>
                    <div class="flex items-baseline gap-2">
                        <span class="text-2xl sm:text-3xl font-extrabold text-white font-mono">{{.DataUsedStr}}</span>
                        <span class="text-sm text-slate-500 font-mono">/ {{.DataQuotaStr}}</span>
                    </div>
                </div>
                <div class="mt-4">
                    <div class="w-full bg-slate-800 rounded-full h-2 overflow-hidden">
                        <div class="bg-cyan-500 h-2 rounded-full transition-all duration-700" style="width: {{.UsagePercent}}%"></div>
                    </div>
                </div>
            </div>

            <!-- Remaining Traffic -->
            <div class="bg-[#111827]/70 border border-slate-800/80 rounded-2xl p-5 shadow-lg flex flex-col justify-between">
                <div>
                    <div class="text-[10px] uppercase font-bold tracking-wider text-slate-400 mb-1">Remaining Traffic</div>
                    <div class="text-2xl sm:text-3xl font-extrabold text-emerald-400 font-mono">{{.DataRemainingStr}}</div>
                </div>
                <div class="text-[10px] text-slate-500 mt-4">Available bandwidth limit</div>
            </div>

            <!-- Expiration Date -->
            <div class="bg-[#111827]/70 border border-slate-800/80 rounded-2xl p-5 shadow-lg flex flex-col justify-between">
                <div>
                    <div class="text-[10px] uppercase font-bold tracking-wider text-slate-400 mb-1">Expiration Date</div>
                    <div class="text-2xl sm:text-3xl font-extrabold text-amber-400 font-mono">{{.ExpiryStr}}</div>
                </div>
                <div class="text-[10px] text-slate-500 mt-4">Access valid until</div>
            </div>
        </div>

        <!-- Node Cards Grid -->
        <div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-6">
            {{range .Nodes}}
            <div class="bg-[#111827]/80 border border-slate-800 rounded-2xl p-5 shadow-xl flex flex-col justify-between space-y-4 hover:border-slate-700 transition">
                <div class="flex items-center justify-between">
                    <div class="flex items-center gap-2">
                        <span class="w-2.5 h-2.5 rounded-full bg-emerald-400 animate-pulse"></span>
                        <span class="text-base font-bold text-white tracking-wide">{{.NodeName}}</span>
                    </div>
                    <span class="text-[10px] font-semibold text-slate-400 bg-slate-800/80 px-2.5 py-1 rounded-md border border-slate-700">Server</span>
                </div>

                <!-- QR Code Box -->
                <div class="flex justify-center items-center bg-white rounded-xl p-3 shadow-inner mx-auto">
                    <div id="qrcode-{{.NodeID}}"></div>
                </div>

                <!-- Config Snippet Header -->
                <div>
                    <div class="text-[10px] font-semibold text-slate-400 uppercase tracking-wider mb-1.5 flex items-center justify-between">
                        <span>Wireguard Config — {{.NodeName}}</span>
                        <span class="font-mono text-[10px] text-slate-500">{{.AssignedIP}}</span>
                    </div>
                    <div class="relative bg-[#090d16] border border-slate-800 rounded-xl p-3">
                        <pre class="text-[10px] font-mono text-slate-300 h-32 overflow-y-auto custom-scroll whitespace-pre leading-relaxed">{{.RawConfig}}</pre>
                    </div>
                </div>

                <!-- Actions -->
                <div class="grid grid-cols-2 gap-3 pt-2">
                    <button onclick="copyText(atob('{{.Base64Config}}'), 'Config copied!')" class="w-full bg-slate-800 hover:bg-slate-700 active:bg-slate-600 text-slate-200 text-[10px] sm:text-[10px] font-semibold py-2.5 px-3 rounded-xl transition border border-slate-700/60 shadow-sm">
                        Copy Config
                    </button>
                    <a href="?node_id={{.NodeID}}&download=1" class="w-full text-center bg-cyan-900/40 hover:bg-cyan-800/60 text-cyan-300 border border-cyan-700/50 active:scale-95 text-[10px] sm:text-[10px] font-semibold py-2.5 px-3 rounded-xl transition shadow-sm">
                        Download .conf
                    </a>
                </div>
            </div>
            {{end}}
        </div>
    </div>

    <!-- Notification Toast -->
    <div id="toast" class="fixed bottom-6 right-6 bg-slate-800 border border-cyan-500/50 text-cyan-300 text-xs font-semibold px-4 py-2.5 rounded-xl shadow-2xl transition-all duration-300 opacity-0 pointer-events-none translate-y-2">
        Copied to clipboard!
    </div>

    <script>
        {{range .Nodes}}
        new QRCode(document.getElementById("qrcode-{{.NodeID}}"), {
            text: atob("{{.Base64Config}}"),
            width: 170,
            height: 170,
            colorDark: "#0f172a",
            colorLight: "#ffffff",
            correctLevel: QRCode.CorrectLevel.L
        });
        {{end}}

        function copyText(text, msg) {
            navigator.clipboard.writeText(text).then(() => {
                const toast = document.getElementById("toast");
                toast.innerText = msg || "Copied to clipboard!";
                toast.classList.remove("opacity-0", "translate-y-2");
                toast.classList.add("opacity-100", "translate-y-0");
                setTimeout(() => {
                    toast.classList.remove("opacity-100", "translate-y-0");
                    toast.classList.add("opacity-0", "translate-y-2");
                }, 2200);
            });
        }
    </script>
</body>
</html>`))
