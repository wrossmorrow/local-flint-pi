/**
 * flint-pi: session attribution headers for model requests.
 *
 * Stamps the current pi session onto every provider request as headers, so the
 * model proxy / extproc layer can attribute usage, latency, and token counts to
 * a session without parsing the request body.
 *
 * - `x-session-id`: derived session id (session-name slug + short base id)
 * - `x-session-name`: human-readable session name, when set
 *
 * The session id is resolved on every provider request, so renames via pi's
 * `/name <name>` command (or `pi.setSessionName`) are picked up immediately.
 */

import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";
import { getApiProvider } from '@mariozechner/pi-ai';
import { readFileSync } from 'node:fs';
import { registerLogo } from './pi-logo.mjs';
import { createRecStream } from './pi-transport.mjs';

export default function (pi: ExtensionAPI) {

  // Stable unique base derived from the session file name for this session lifetime.
  // Combined with the human-readable name (if set) to build the final session id.
  let baseSessionId: string | null = null;

  /**
   * Converts a human-readable session name into an URL/ID-safe slug.
   * e.g. "Refactor auth module!" -> "refactor-auth-module"
   */
  function slugify(text: string): string {
    return text
      .toLowerCase()
      .replace(/[^a-z0-9]+/g, "-") // non-alphanumeric runs -> single hyphen
      .replace(/^-+|-+$/g, "") // strip leading/trailing hyphens
      .slice(0, 50); // cap length to keep ids readable
  }

  /**
   * Builds the session id to send to the model. If the user has named the
   * session, produces "<slug>-<short-base-id>" for human-readable grouping
   * whilst remaining unique across same-named sessions. Otherwise returns the
   * short uuid portion of the base id. In both cases the id is derived from the
   * UUID portion of the base session id, never from the raw base id.
   */
  function buildSessionId(): string | null {
    if (!baseSessionId) return null;

    const uuidPart = baseSessionId.split("_")[1] ?? baseSessionId;
    const shortId = uuidPart.replace(/-/g, "").slice(0, 8);
    const name = pi.getSessionName();
    if (name) {
      const slug = slugify(name);
      // Session filenames are "<ISO-timestamp>_<uuid>" - extract the uuid portion
      // for the short id so we don't bleed timestamp digits/hyphens into the slug.
      // e.g. "2026-05-06T12-00-00-000Z_019dbbc7-c8c5-748c-8710-3fdf6fefc083"
      //   -> shortId = "019dbbc7"
      return `${slug}-${shortId}`;
    }

    // no display name: just use the UUID portion of the session id
    return shortId;
  }

  // Establish the base session ID whenever the session starts, changes, or reloads
  pi.on("session_start", async (_event, ctx) => {
    baseSessionId = null;
    const sessionFile = ctx.sessionManager.getSessionFile();

    if (sessionFile) {
      // Extract the filename without extension as the stable base ID
      // e.g. "~/.pi/agent/sessions/--home--/2026-05-06T12-00-00-000Z_019dbbc7-....jsonl"
      //   -> "2026-05-06T12-00-00-000Z_019dbbc7-..."
      const match = sessionFile.match(/([^/\\]+)\.jsonl$/);
      baseSessionId = match ? match[1] : null;
    }

    if (!baseSessionId) {
      // Ephemeral session (--no-session) - generate a random one-off id
      baseSessionId = `ephemeral-${Date.now()}-${Math.random().toString(36).slice(2, 15)}`;
    }
  });

  // Stamp the session onto every provider request via headers.
  pi.on("before_provider_headers", (event, ctx) => {
    const sessionId = buildSessionId() ?? ctx.sessionManager.getSessionId();
    event.headers["x-session-id"] = sessionId;
    const name = pi.getSessionName();
    if (name) {
      event.headers["x-session-name"] = name;
    }
  });

  registerLogo(pi, JSON.parse(readFileSync(new URL('./logo-pixels.json', import.meta.url), 'utf8')));
  const config = JSON.parse(process.env.REC_PI_CONFIG);
  const key = process.env.REC_API_KEY;
  if (!key) throw new Error('Rec API key is missing; use rec launch pi.');
  const native = getApiProvider('openai-completions');
  if (!native) throw new Error('Pi OpenAI-compatible transport is unavailable.');
  pi.registerProvider('rec', {
    name: 'Recursive local', api: 'rec-flint-completions',
    baseUrl: config.baseUrl, apiKey: 'REC_API_KEY',
    streamSimple: createRecStream(native, config, key),
    models: [{
      id: 'flint', name: 'Flint', reasoning: false, input: ['text'],
      contextWindow: config.contextWindow, maxTokens: config.maxTokens,
      cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
      compat: {
        supportsStore: false, supportsDeveloperRole: false,
        supportsReasoningEffort: false, supportsStrictMode: false,
        maxTokensField: 'max_tokens',
      },
    }],
  });
}
