/**
 * flint-pi: session attribution headers for model requests.
 *
 * Stamps the current pi session onto every provider request as headers, so the
 * model proxy / extproc layer can attribute usage, latency, and token counts to
 * a session without parsing the request body.
 *
 * - `x-session-id`: session UUID extracted from the session file name
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
  // Holds the full "<ISO-timestamp>_<uuid>" session file stem, or an ephemeral
  // session id when the session was started with --no-session.
  let baseSessionId: string | null = null;

  /**
   * Builds the session id to send to the model. Returns the UUID extracted
   * from the session file name, e.g. "01a17b77-a54d-7692-b04a-a94c16ae5d16",
   * falling back to the base id when no UUID can be extracted.
   */
  function buildSessionId(): string | null {
    if (!baseSessionId) return null;

    // Session filenames are "<ISO-timestamp>_<uuid>" - extract the uuid portion
    // for the session id, never from the raw base id.
    // e.g. "2026-10-27T03-46-41-101Z_01a17b77-a54d-7692-b04a-a94c16ae5d16"
    //   -> "01a17b77-a54d-7692-b04a-a94c16ae5d16"
    const uuid = baseSessionId.split("_")[1] ?? baseSessionId;
    return uuid;
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

  // Stamp the session and provider/model attribution onto every provider request via headers.
  pi.on("before_provider_headers", (event, ctx) => {
    const sessionId = buildSessionId() ?? ctx.sessionManager.getSessionId();
    event.headers["x-session-id"] = sessionId;
    const name = pi.getSessionName();
    if (name) {
      event.headers["x-session-name"] = name;
    }
    const activeModel = (ctx as any).model;
    if (!event.headers["x-provider"]) {
      event.headers["x-provider"] = activeModel?.provider || process.env.PI_PROVIDER || "rec";
    }
    if (!event.headers["x-model"]) {
      const resolvedModel = activeModel?.id || process.env.PI_MODEL || "flint";
      event.headers["x-model"] = resolvedModel === "flint" ? "RecursiveSI/flint" : resolvedModel;
    }
  });

  registerLogo(pi, JSON.parse(readFileSync(new URL('./logo-pixels.json', import.meta.url), 'utf8')));

  const recKey = process.env.REC_API_KEY;
  if (recKey) {
    const recConfig = JSON.parse(
      process.env.REC_PI_CONFIG || '{"baseUrl":"http://model-relay:11500/v1","contextWindow":262144,"maxTokens":32768}'
    );
    const native = getApiProvider('openai-completions');
    if (!native) throw new Error('Pi OpenAI-compatible transport is unavailable.');
    pi.registerProvider('rec', {
      name: 'Recursive local', api: 'rec-flint-completions',
      baseUrl: recConfig.baseUrl, apiKey: 'REC_API_KEY',
      headers: { 'x-provider': 'rec', 'x-model': 'RecursiveSI/flint' },
      streamSimple: createRecStream(native, recConfig, recKey),
      models: [{
        id: 'flint', name: 'Flint', reasoning: false, input: ['text'],
        contextWindow: recConfig.contextWindow, maxTokens: recConfig.maxTokens,
        cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
        compat: {
          supportsStore: false, supportsDeveloperRole: false,
          supportsReasoningEffort: false, supportsStrictMode: false,
          maxTokensField: 'max_tokens',
        },
      }],
    });
  }

  const inklingProvider = process.env.INKLING_PROVIDER || 'llama-server';
  const inklingConfig = JSON.parse(
    process.env.INKLING_PI_CONFIG || '{"baseUrl":"http://model-relay:11500/v1","contextWindow":131072,"maxTokens":32768}'
  );
  pi.registerProvider(inklingProvider, {
    name: 'Inkling local', api: 'openai-completions',
    baseUrl: inklingConfig.baseUrl, apiKey: 'INKLING_API_KEY',
    headers: { 'x-provider': inklingProvider, 'x-model': 'thinkingmachines/Inkling-2-Mini' },
    models: [{
      id: 'thinkingmachines/Inkling-2-Mini', name: 'Inkling 2 Mini', reasoning: true, input: ['text', 'image'],
      contextWindow: inklingConfig.contextWindow, maxTokens: inklingConfig.maxTokens,
      cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
      compat: {
        supportsStore: false, supportsDeveloperRole: false,
        supportsStrictMode: false, maxTokensField: 'max_tokens',
      },
    }],
  });
}
