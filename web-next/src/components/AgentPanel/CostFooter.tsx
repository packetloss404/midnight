import type { UsageBlock } from '../../api/agent';

export interface CostFooterProps {
  usage?: UsageBlock;
}

export function CostFooter({ usage }: CostFooterProps) {
  if (!usage) {
    return (
      <div
        data-testid="agent-cost-footer"
        style={{
          padding: '6px 12px',
          borderTop: '1px solid var(--line)',
          color: 'var(--subtle)',
          fontFamily: 'var(--font-mono)',
          fontSize: 11,
          display: 'flex',
          justifyContent: 'space-between',
          background: 'var(--surface)',
        }}
      >
        <span>no turn yet</span>
        <span>$0.0000</span>
      </div>
    );
  }
  const total = (usage.input_tokens ?? 0) + (usage.output_tokens ?? 0);
  const cost = usage.cost_usd_estimated ?? 0;
  return (
    <div
      data-testid="agent-cost-footer"
      style={{
        padding: '6px 12px',
        borderTop: '1px solid var(--line)',
        color: 'var(--muted)',
        fontFamily: 'var(--font-mono)',
        fontSize: 11,
        display: 'flex',
        justifyContent: 'space-between',
        gap: 8,
        background: 'var(--surface)',
      }}
    >
      <span>
        in {usage.input_tokens} · out {usage.output_tokens} · {total} tok
      </span>
      <span>~${cost.toFixed(4)}</span>
    </div>
  );
}
