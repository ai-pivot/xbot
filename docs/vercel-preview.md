# Vercel PR frontend previews and backend connection

Vercel hosts only the static React/Vite frontend. Preview builds include a connection gate for an explicitly authorized, separately operated xbot backend. Do not deploy the Go backend or put model/API credentials in the Vercel project.

## Project configuration

- GitHub repository: `ai-pivot/xbot`; GitHub App repository access is scoped to `xbot`.
- Vercel project: https://vercel.com/mattos-projects-6bf6c006/xbot-preview
- Root Directory: `web` (dashboard setting).
- Framework: Vite; Install Command: `npm ci`; Build Command: `npm run build`; Output Directory: `dist`.
- Public project URL: https://xbot-preview.vercel.app
- Keep Vercel's GitHub integration enabled for PR comments, commit status and deployment links. Do not add a duplicate deployment workflow.

The repository has both npm and pnpm lockfiles. Vercel initially selected a stale pnpm lockfile and failed with `ERR_PNPM_OUTDATED_LOCKFILE`. Pin `npm ci`, matching existing GitHub CI, rather than disabling lockfile validation. `web/vercel.json` makes this reproducible.

## Routing

Existing files are served before SPA fallback. Extensionless frontend routes fall back to index.html. API, WebSocket, plugin and static-resource namespaces never fall back to HTML; missing files return 404. The route unit tests model only this ordered subset, not the full Vercel routing engine.

## PR previews and security

Eligible PRs automatically receive deployment status and a preview URL from the Vercel GitHub integration. Additional commits create updated deployments. Fork PRs may require maintainer approval; do not disable fork protection or expose deployment secrets to untrusted code. The project's “Skip deployments when there are no changes to the root directory or its dependencies” optimization is disabled so backend-only and documentation-only PR updates can also trigger previews. Keep it disabled if every eligible PR should receive a fresh deployment.

The original hosting-only change established static deployment and routing. This follow-up adds the preview backend-selection gate, short-lived cross-origin authorization and backend transports described below. Ordinary production builds retain same-origin cookie authentication. The preview does not provide mock backend data or automatically activate localhost access: the backend must run the new code, explicitly allow the exact preview origin, and receive user-managed activation. Do not relax normal cookie policy or expose a backend merely to make a static deployment appear functional.

Preview mode is compiled when `VERCEL_ENV=preview` or `XBOT_WEB_PREVIEW=1`. The latter is a non-secret build flag, not a model credential or an authorization grant. Scope it to Preview builds; production requires its own production build.

## Acceptance

1. On a PR, confirm a Vercel status/comment links to a deployment of its exact commit SHA.
2. Open its URL and a frontend deep link (for example `/login`). Confirm actual JS/CSS assets load.
3. Missing `/api/missing`, `/ws/terminal`, `/plugins/missing`, `/assets/missing.js` and `/assets/missing` must return 404, not HTML.
4. Push a second commit and verify a new deployment/updated PR link.
5. No backend restart, backend deployment or model request is required for this acceptance.


## Backend connection


### Activation and verification status

The editable default `http://localhost:8082` is an address, **not evidence that an existing localhost backend supports preview connections**. The backend must run a build containing this follow-up's preview authorization, CORS and transport handlers. An older running backend cannot acquire these handlers from a frontend deployment.

Before connecting, the user must:

1. Build/install the backend version containing this change.
2. Set `XBOT_PREVIEW_ORIGINS` in the **backend process startup environment** to the exact trusted preview origin, for example `https://your-preview.vercel.app` (no wildcard or path).
3. **Manually arrange the backend restart** needed to load the new binary and environment, then log in locally and complete explicit preview authorization. Agents must not restart the user's backend. Existing grants are lost on restart.

Build and focused test checks do **not** prove the deployed gate or authenticated browser-to-localhost connection. Real-browser gate, local-network permission/CORS, SSE, terminal WebSocket and resource acceptance remain **unverified**. Browser operation is paused under user control: do not open or automate a browser until the user explicitly says to continue. A follow-up PR may be published before this acceptance, but must state that user verification is still required; publication is not end-to-end approval.

Security acceptance must include regression checks that preview grants cannot call credential-management or secret-export RPCs. Do not infer that protection from the gate UI or hosting checks. This connection delegates broad workspace abilities and is not a read-only sandbox.


### Preview build contract


The Vite implementation must select preview mode exactly when either condition holds:

```text
XBOT_WEB_PREVIEW === "1" OR VERCEL_ENV === "preview"
```

This is a **compile-time** convention, not a URL parameter or a runtime toggle. `XBOT_WEB_PREVIEW=0` does not cancel `VERCEL_ENV=preview`. Normal same-origin production builds, with neither condition true, retain their normal behavior. Do not set `NODE_ENV=development` or use Vite development mode to simulate a preview: preview bundles should retain production React semantics.

For a later local build check, run from the repository root:

```sh
cd web
npm ci
XBOT_WEB_PREVIEW=1 npm run build
# Inspect dist before replacing it with another build.
# Standard build check, without inherited preview variables:
env -u XBOT_WEB_PREVIEW -u VERCEL_ENV npm run build
```

These commands are instructions, not evidence that the builds have been run. Both builds write `web/dist`.

Preview mode must disable PWA generation **and** runtime service-worker registration/update, including previews hosted at `/`. Verify that no generated worker or manifest is emitted and no registration/update calls occur. Existing subpath PWA guards alone do not cover root-path Vercel previews. This hosting config does not implement those Vite/runtime guards.

## SPA routes versus resources

`web/vercel.json` uses ordered low-level routes:

1. `handle: filesystem` serves real build files first, including existing files whose paths contain dots.
2. The SPA fallback serves `/index.html` only for extensionless navigation paths outside reserved resource namespaces. This supports `/` and deep links such as `/s/example-token`.
3. The final catch-all returns **404**, not the SPA shell.

Reserved namespaces are `/api`, `/ws`, `/plugins`, `/assets`, `/static`, `/images`, `/fonts`, `/icons`, `/smg-new` and `/_vercel`, including their children. Any missing path containing a literal dot is also excluded, covering scripts, styles, images, fonts, manifests and service workers at any depth. Thus missing extensionless `/assets/chunk`, `/api/health`, `/plugins/example/module` and dotted `/missing.js` all fail closed. Existing files still win via the filesystem route. Dotted pathname routes return 404 unless a real file exists; add narrowly scoped navigation rules only after reviewing the resource boundary. Session links currently encode the session ID in the `?session=` query on the frontend root, so dots in a session ID do not encounter this pathname exclusion. Reserve any future extensionless resource namespace explicitly.

Do not replace this with an unconditional `/(.*) -> /index.html` rewrite or combine it with another `rewrites`/`redirects` routing scheme. An HTML response for a missing JS module or API endpoint masks the real failure. Vite's framework defaults are not a substitute for these explicit resource exclusions.

There are **no backend proxies or API functions** here. A request to the preview origin's missing `/api/*` or `/plugins/*` must fail instead of accidentally contacting a production backend. The separately implemented connection selection must route xbot-owned backend transports/resources to the selected backend, while frontend navigation stays on the Vercel origin.

## Connecting a backend safely

The preview entry is designed to show connection settings before authentication and other backend-dependent providers mount. It must offer an actual editable default of **`http://localhost:8082`**, or the last address saved on that preview origin, and require explicit confirmation before business/model requests. The saved address is per-origin; a different deployment hostname may not share it. Store only the address locally, not model keys or session secrets. Accept only HTTP(S) backend addresses, rejecting embedded credentials, query strings and fragments; a backend subpath is a prefix (`https://host/xbot` + `/api/...` becomes `https://host/xbot/api/...`). The reverse proxy must preserve that contract; the built-in consent page uses root-relative script/API links, so do not assume subpath authorization works without suitable proxy routing and browser verification.

`localhost` refers to the **reviewer's machine**, not Vercel's build host. The reviewer needs an already-running backend there. This frontend deployment neither provisions nor restarts it. Before authorizing a PR frontend, remember it is executable, potentially untrusted code: connecting it may expose the selected backend's workspace, files, sessions, tools and model-backed operations. Use an isolated, least-privilege test workspace and confirm the target deliberately.

Do not put model/API keys, backend credentials, production cookies, real workspace data or session secrets in Vercel Preview environment variables, build output, PR comments or this config. Browser bundles and `VITE_*` values are public to users who can load them; deployment protection is not a secret-storage mechanism.

### Enable the approved backend protocol

Preview authorization is **off by default**. On the existing backend, the user/operator must configure `XBOT_PREVIEW_ORIGINS` as a comma-separated list of exact trusted frontend origins, for example:

```text
XBOT_PREVIEW_ORIGINS=https://review-example.vercel.app,http://localhost:5173
```

Entries are origins only: scheme, hostname and optional port, with no trailing slash/path, query, fragment or wildcard. HTTPS origins are accepted; HTTP origins are restricted to loopback (`localhost`, `127.0.0.1`, `::1`). Add each trusted deployment origin explicitly; do not replace this with `*.vercel.app`. This is a backend process setting, not a Vercel build secret. If the running backend must be updated or its startup environment changed, **the user must arrange any necessary restart manually; agents must not restart the server**. Static frontend hosting itself needs no backend restart.

1. In a separate tab on the backend origin, log in using the existing normal cookie-authenticated UI. Preview requests do not borrow this cookie.
2. Open the gate's authorization link, `/preview/authorize?origin=<URL-encoded-preview-origin>`, on that backend. Check the displayed account and exact origin before approving. The consent page requires an existing local login and an allowlisted origin.
3. Approval produces a token to copy manually into the preview gate. It expires after **15 minutes or the parent login session's expiry, whichever is sooner**, and is bound to that parent session and exact origin. Backend grants and the frontend's active token are held in memory, not persisted as preview credentials. Parent logout revokes its grants; a backend restart loses them. Never place this token in Vercel settings, stored Markdown or PR comments.
4. Read and explicitly accept the gate's workspace warning, then connect. Token verification checks the origin and expiry before backend-dependent providers mount. Reconfiguring the backend reloads the page to discard the previous application state.

**This grants full user workspace abilities, not a read-only review sandbox:** files, commands/tools, terminal, sessions, settings and model-backed operations may be accessible to the preview code. Authentication/admin/runner-credential management routes are blocked, but that is not a sandbox against malicious code with workspace command access. Authorize only reviewed code against an isolated test workspace.

### Resource and transport boundaries

- API and protected resource fetches use `Authorization: Bearer ...`, omit cookies and reject redirects. There is no cookie-auth fallback for cross-origin preview requests. CORS is exact-origin and does not enable wildcard credentialed access.
- Native SSE and WebSocket transports use the short-lived grant in the `preview_token` query parameter; the backend restricts query credentials to GET `/api/sse`, `/ws` and `/ws/terminal`. Treat those URLs as sensitive and redact them from proxy/access logs and diagnostics. Do not put the full token into arbitrary image, attachment, plugin or download URLs.
- Protected resource adapters fetch bytes with the bearer header and expose temporary Blob URLs, with a **32 MiB hard limit per resource**, including streamed responses without a trustworthy Content-Length. Object URLs are revoked on cleanup. Link downloads are explicitly prepared on demand.
- Redirected resources, including signed object-store download redirects, deliberately fail closed rather than forwarding credentials. Large resources and redirected downloads must be opened/downloaded through the normal backend application instead. Do not claim all media/download formats or object-store adapters work in the preview; verify the actual configured storage adapter.
- Plugin modules use the public plugin-module CORS path without receiving a preview credential. Keep frontend navigation on the Vercel origin and route only owned backend transports/resources to the selected backend.

### Browser and authentication limitations

- An HTTPS preview connecting to HTTP localhost or a LAN backend can encounter mixed-content restrictions, local/private network permission prompts, insecure WebSocket restrictions and browser-specific policies. Test the actual target browser; a URL being accepted is not proof that transport works.
- Cross-origin credentialed requests require a deliberately approved backend origin/auth design. The current backend's `SameSite=Lax` authentication cookies cannot simply be reused by a cross-site Vercel frontend. CORS alone does not fix this, and an HTTPS tunnel alone does not fix cookie/auth policy.
- Do not globally switch cookies to `SameSite=None`, enable wildcard credentialed CORS, weaken WebSocket origin checks or disable browser security.
- Network failures cannot reliably distinguish backend downtime, CORS, local-network permissions and mixed-content blocking. Report observations rather than claiming a specific cause from a generic fetch failure.

The short-lived explicit authorization protocol is user-approved and implemented separately from `vercel.json`; it does not weaken the normal cookie policy. Approval and implementation are not browser acceptance evidence: **do not describe authenticated remote-backend use as working** until the actual deployment, browser and required transports have been verified.

## Acceptance checks after an authorized deployment

These are follow-up checks for a maintainer with actual project access, not actions performed by adding these files:

- Confirm the project root/build/output, Preview environment flags, deployed commit SHA and emitted asset hashes. Check both the initial PR deployment and a subsequent commit's updated deployment entry.
- Open `/` and reload a valid client deep link such as `/s/example-token`. The HTML shell should load; the token's backend lookup can still fail separately.
- Request an actual hashed JS/CSS asset from the deployed HTML and verify its MIME type and contents. Request `/assets/missing.js`, `/assets/missing`, `/api`, `/api/missing`, `/ws`, `/ws/terminal`, `/plugins`, `/plugins/missing/module`, `/missing.css`, `/sw2.js` and `/manifest.webmanifest`. Missing resources must return 404, never the SPA HTML. If a worker/manifest exists in a preview build, treat that as a PWA isolation failure.
- Verify the editable backend default, explicit confirmation gate, connection cancellation/timeouts, reconfiguration and absence of business requests before confirmation. Check that switching backend disposes old connections/state (a full reload is acceptable).
- Verify preview worker generation/registration/update are disabled, while ordinary production and existing subpath builds retain their intended behavior.
- With an explicitly allowlisted origin and isolated backend, exercise local login/consent, token verification and expiry/revocation, API, SSE, terminal WebSocket, uploads/downloads, attachments, plugin imports/resources and share routes in the actual browser. Confirm requests without a grant cannot fall back to cookies or execute unsafe routes, and redirected/oversized resources fail closed. Keep backend PID and production worker state unchanged.

**Known verification boundary:** a static JSON/route-pattern check cannot validate Vercel's deployed routing engine, GitHub permissions, fork approval policy, browser network behavior or authenticated backend access. Record which checks were actually run and leave untested items explicit.
