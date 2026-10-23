import { AgentRequestError } from '../../api/agent';

export const AGENT_SETUP_DOCS_PATH = '/docs/agent-setup';

export const AGENT_NOT_CONFIGURED_MESSAGE =
  "The AI agent isn't configured on this server. Set ANTHROPIC_API_KEY (or another provider) and restart midnight-server.";

export interface AgentFailure {
  /** Short heading; falls back to a generic one. */
  title: string;
  /** Readable explanation, never a raw JSON body. */
  detail?: string;
  /** Server failure code, when the response carried one. */
  code?: string;
  /** True when the server has no agent provider configured. */
  notConfigured: boolean;
}

const NOT_CONFIGURED_HINT = /not configured|api[ _-]?key/i;

/**
 * Turns a send/create failure into something a person can act on. The
 * backend reports a missing provider as `AGENT_NOT_CONFIGURED` (503); older
 * servers surfaced it as a 500 whose detail mentions the missing API key, so
 * both shapes map to the same friendly explanation.
 */
export function describeAgentFailure(error: unknown): AgentFailure {
  if (error instanceof AgentRequestError) {
    const haystack = `${error.title ?? ''} ${error.detail ?? ''}`;
    const notConfigured =
      error.code === 'AGENT_NOT_CONFIGURED' || NOT_CONFIGURED_HINT.test(haystack);
    if (notConfigured) {
      return {
        title: 'Agent not configured',
        detail: AGENT_NOT_CONFIGURED_MESSAGE,
        code: error.code || 'AGENT_NOT_CONFIGURED',
        notConfigured: true,
      };
    }
    return {
      title: error.title ?? `Agent request failed (${error.status})`,
      detail: error.detail && error.detail !== error.title ? error.detail : undefined,
      code: error.code,
      notConfigured: false,
    };
  }
  const message = error instanceof Error ? error.message : '';
  if (NOT_CONFIGURED_HINT.test(message)) {
    return {
      title: 'Agent not configured',
      detail: AGENT_NOT_CONFIGURED_MESSAGE,
      code: 'AGENT_NOT_CONFIGURED',
      notConfigured: true,
    };
  }
  return {
    title: 'Failed to send message.',
    detail: message || undefined,
    notConfigured: false,
  };
}
