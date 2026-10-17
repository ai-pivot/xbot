# Vercel frontend PR deployments

Vercel hosts only the static React/Vite frontend. Do not deploy the Go backend or put model/API credentials in this project.

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

This deployment-only change does NOT add backend selection, cross-origin authorization, mock data or localhost access. The current frontend assumes a same-origin backend: a static deployment can display the frontend shell, but authenticated backend operations are unavailable. Those capabilities are a separate change. Do not expose a local backend or relax CORS/cookie settings to make this hosting-only PR appear fully functional.

`XBOT_WEB_PREVIEW=1` is reserved for the follow-up frontend preview-mode implementation. The project may carry this non-secret flag, but this PR alone does not consume it. It must never be treated as a model credential or an authorization grant.

## Acceptance

1. On a PR, confirm a Vercel status/comment links to a deployment of its exact commit SHA.
2. Open its URL and a frontend deep link (for example `/login`). Confirm actual JS/CSS assets load.
3. Missing `/api/missing`, `/ws/terminal`, `/plugins/missing`, `/assets/missing.js` and `/assets/missing` must return 404, not HTML.
4. Push a second commit and verify a new deployment/updated PR link.
5. No backend restart, backend deployment or model request is required for this acceptance.
