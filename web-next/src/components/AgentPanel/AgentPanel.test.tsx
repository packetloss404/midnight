import { describe, it, expect, beforeEach } from 'vitest';
import { act, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { Message } from './Message';
import { MessageList } from './MessageList';
import { ToolCall } from './ToolCall';
import type { AgentMessage } from '../../api/agent';
import type { StreamingTurn } from '../../api/agent-hooks';

function makeMessage(partial: Partial<AgentMessage>): AgentMessage {
  return {
    id: 'msg_1',
    role: 'assistant',
    seq: 0,
    content: [],
    created_at: '2026-05-17T00:00:00Z',
    ...partial,
  } as AgentMessage;
}

beforeEach(() => {
  // Clean DOM between tests.
  document.body.innerHTML = '';
});

describe('Message renderer', () => {
  it('renders user messages right-aligned and assistant messages left-aligned', () => {
    const user = makeMessage({
      id: 'u1',
      role: 'user',
      content: [{ type: 'text', text: 'hello agent' }],
    });
    const assistant = makeMessage({
      id: 'a1',
      role: 'assistant',
      content: [{ type: 'text', text: 'hi there' }],
    });

    const { rerender } = render(<Message message={user} />);
    const userEl = screen.getByTestId('message-u1');
    expect(userEl).toHaveAttribute('data-role', 'user');
    expect(userEl).toHaveStyle({ alignItems: 'flex-end' });
    expect(within(userEl).getByText('hello agent')).toBeInTheDocument();

    rerender(<Message message={assistant} />);
    const aEl = screen.getByTestId('message-a1');
    expect(aEl).toHaveAttribute('data-role', 'assistant');
    expect(aEl).toHaveStyle({ alignItems: 'flex-start' });
    expect(within(aEl).getByText('hi there')).toBeInTheDocument();
  });

  it('renders fenced code blocks as <pre>', () => {
    const m = makeMessage({
      id: 'a-code',
      content: [{ type: 'text', text: 'before\n```\nrun deploy\n```\nafter' }],
    });
    const { container } = render(<Message message={m} />);
    const pre = container.querySelector('pre');
    expect(pre).not.toBeNull();
    expect(pre?.textContent).toMatch(/run deploy/);
  });

  it('shows the streaming cursor when streaming is true', () => {
    const m = makeMessage({
      id: 'a-stream',
      content: [{ type: 'text', text: 'partial' }],
    });
    render(<Message message={m} streaming />);
    expect(screen.getByLabelText('streaming')).toBeInTheDocument();
  });

  it('keeps the streaming cursor on trailing text after a folded tool result', () => {
    const m = makeMessage({
      id: 'a-tool-stream',
      content: [
        { type: 'tool_use', id: 'tu_1', name: 'list_services', input: {} },
        {
          type: 'tool_result',
          tool_use_id: 'tu_1',
          content: '[]',
        },
        { type: 'text', text: 'No services are running.' },
      ],
    });

    expect(() => render(<Message message={m} streaming />)).not.toThrow();
    expect(screen.getByText('No services are running.')).toBeInTheDocument();
    expect(screen.getByLabelText('streaming')).toBeInTheDocument();
  });
});

describe('ToolCall', () => {
  it('starts collapsed and expands on click', async () => {
    const user = userEvent.setup();
    render(
      <ToolCall
        id="tu_1"
        name="list_services"
        input={{ project_id: 'prj_x' }}
        result='[{"name":"web"}]'
        defaultCollapsed
      />,
    );
    const chip = screen.getByTestId('tool-call-tu_1');
    expect(chip).toHaveAttribute('data-open', 'false');
    // Body not rendered while collapsed.
    expect(screen.queryByText(/list_services/)).toBeInTheDocument();
    expect(document.getElementById('tool-call-body-tu_1')).toBeNull();

    await user.click(within(chip).getByRole('button', { expanded: false }));
    expect(chip).toHaveAttribute('data-open', 'true');
    expect(document.getElementById('tool-call-body-tu_1')).not.toBeNull();
  });

  it('renders error state with status-error color', () => {
    render(
      <ToolCall
        id="tu_err"
        name="get_logs"
        input={{ service_id: 'svc_y' }}
        result="permission denied"
        isError
      />,
    );
    const chip = screen.getByTestId('tool-call-tu_err');
    expect(chip).toHaveAttribute('data-error', 'true');
    // Two "error" texts exist (header badge + tab label); both are valid.
    expect(within(chip).getAllByText('error').length).toBeGreaterThan(0);
    // The error result is shown in the active tab.
    expect(within(chip).getByText('permission denied')).toBeInTheDocument();
  });

  it('renders pending result when no result is provided', () => {
    const { container } = render(
      <ToolCall id="tu_pending" name="get_deployment" input={{}} defaultCollapsed={false} />,
    );
    // The pending tab trigger is rendered and disabled.
    const pendingTrigger = screen.getByRole('tab', { name: /result \(pending\)/i });
    expect(pendingTrigger).toBeDisabled();
    // Input tab is active by default; its <pre> shows the formatted JSON.
    const pre = container.querySelector('pre');
    expect(pre).not.toBeNull();
    expect(pre?.textContent).toContain('{}');
  });
});

describe('MessageList streaming append', () => {
  it('renders an inflight assistant bubble with appended deltas', () => {
    const messages: AgentMessage[] = [
      makeMessage({
        id: 'u1',
        role: 'user',
        content: [{ type: 'text', text: 'check the worker' }],
      }),
    ];
    let turn: StreamingTurn = { text: 'Looking at ', toolCalls: [] };
    const { rerender } = render(
      <MessageList messages={messages} streamingTurn={turn} isStreaming />,
    );
    expect(screen.getByTestId('message-__inflight__')).toBeInTheDocument();
    expect(screen.getByText('Looking at')).toBeInTheDocument();

    // Append another delta — the inflight bubble should update in place.
    act(() => {
      turn = { text: 'Looking at the worker.', toolCalls: [] };
    });
    rerender(<MessageList messages={messages} streamingTurn={turn} isStreaming />);
    expect(screen.getByText('Looking at the worker.')).toBeInTheDocument();
    // Only one inflight bubble (no duplicates from accumulation).
    expect(screen.getAllByTestId('message-__inflight__')).toHaveLength(1);
  });

  it('renders an inflight tool call inside the streaming bubble', () => {
    const messages: AgentMessage[] = [];
    const turn: StreamingTurn = {
      text: '',
      toolCalls: [{ id: 'tu_z', name: 'list_services', input: { project_id: 'prj_a' } }],
    };
    render(<MessageList messages={messages} streamingTurn={turn} isStreaming />);
    const chip = screen.getByTestId('tool-call-tu_z');
    expect(chip).toBeInTheDocument();
    // Inflight chips start collapsed (defaultCollapsed=true in Message renderer),
    // so the tabs aren't rendered; the chip header still shows the tool name.
    expect(within(chip).getByText('list_services')).toBeInTheDocument();
    expect(chip).toHaveAttribute('data-open', 'false');
  });

  it('omits the inflight bubble when not streaming', () => {
    const messages: AgentMessage[] = [
      makeMessage({
        id: 'a1',
        content: [{ type: 'text', text: 'done' }],
      }),
    ];
    render(<MessageList messages={messages} isStreaming={false} />);
    expect(screen.queryByTestId('message-__inflight__')).toBeNull();
  });

  it('shows an error banner when the streaming turn has an error', () => {
    render(
      <MessageList
        messages={[]}
        streamingTurn={{ text: '', toolCalls: [], error: 'rate limited' }}
        isStreaming={false}
      />,
    );
    expect(screen.getByRole('alert')).toHaveTextContent('rate limited');
  });
});
