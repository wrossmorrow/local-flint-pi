// This transport is registered under a Rec-only API, leaving other providers alone.
export function createRecStream(native, config, key) {
  return (model, context, options = {}) => {
    if (model.provider !== 'rec' || model.id !== 'flint') {
      throw new Error('The Rec transport only supports rec/flint.');
    }
    return native.streamSimple({ ...model, api: 'openai-completions' }, context, {
      ...options,
      apiKey: key,
      headers: { ...options.headers, Authorization: `Bearer ${key}` },
      onPayload: async (payload, nativeModel) => {
        const edited = await options.onPayload?.(payload, nativeModel);
        const result = { ...(edited ?? payload), max_tokens: config.maxTokens };
        for (const field of ['max_completion_tokens', 'temperature', 'top_p', 'top_k']) delete result[field];
        return result;
      },
    });
  };
}
