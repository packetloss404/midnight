import { useEffect, useMemo, useState, type FormEvent } from "react";
import { Button, Card, Field, InlineError, TextInput } from "@/components/panels/form-kit";
import { ErrorState, Loading } from "@/components/states";
import { useInstanceDomains, useSaveInstanceDomains } from "./api";

export function DomainSetup() {
  const query = useInstanceDomains();
  const save = useSaveInstanceDomains();
  const [rootDomain, setRootDomain] = useState("");
  const [ingressTarget, setIngressTarget] = useState("");

  useEffect(() => {
    if (!query.data) return;
    setRootDomain(query.data.root_domain ?? "");
    setIngressTarget(query.data.ingress_target ?? "");
  }, [query.data]);

  const cleanRoot = rootDomain.trim().toLowerCase().replace(/^https?:\/\//, "").replace(/\/$/, "");
  const preview = useMemo(
    () => ({
      console: cleanRoot ? `midnight.${cleanRoot}` : "midnight.example.com",
      apps: cleanRoot ? `*.apps.${cleanRoot}` : "*.apps.example.com",
    }),
    [cleanRoot],
  );
  const recordType = looksLikeIPAddress(ingressTarget.trim()) ? "A" : "CNAME";

  async function submit(event: FormEvent) {
    event.preventDefault();
    await save.mutateAsync({
      rootDomain,
      ingressTarget,
      expectedVersion: query.data?.version ?? 0,
    });
  }

  return (
    <Card
      title="Custom domain"
      description="Choose the domain this Midnight installation uses for its console and deployed services."
      action={
        query.data?.configured ? (
          <span className="rounded-full border border-line bg-surface-2 px-2 py-1 text-[10px] font-semibold text-muted">
            {query.data.restart_required ? "Restart required" : "Configured"}
          </span>
        ) : null
      }
    >
      {query.isPending ? (
        <Loading label="Loading domain setup…" />
      ) : query.error ? (
        <ErrorState title="Failed to load domain setup" error={query.error} onRetry={() => query.refetch()} />
      ) : (
        <form className="grid gap-4" onSubmit={(event) => void submit(event)}>
          <div className="grid gap-3 sm:grid-cols-2">
            <Field
              label="Domain you own"
              hint="Enter the root domain only. Midnight creates dedicated names beneath it."
              htmlFor="instance-root-domain"
            >
              <TextInput
                id="instance-root-domain"
                name="root-domain"
                value={rootDomain}
                onChange={(event) => setRootDomain(event.target.value)}
                placeholder="example.com"
                autoCapitalize="none"
                autoCorrect="off"
                spellCheck={false}
                required
              />
            </Field>
            <Field
              label="Public ingress"
              hint="The public IP or hostname that sends traffic to this Midnight server."
              htmlFor="instance-ingress-target"
            >
              <TextInput
                id="instance-ingress-target"
                name="ingress-target"
                value={ingressTarget}
                onChange={(event) => setIngressTarget(event.target.value)}
                placeholder="203.0.113.10"
                autoCapitalize="none"
                autoCorrect="off"
                spellCheck={false}
                required
              />
            </Field>
          </div>

          <div className="grid gap-2 rounded-[var(--r-sm)] border border-line bg-surface-2 p-3 text-xs">
            <p className="m-0 font-semibold text-text">Add these records at your DNS provider</p>
            <DNSRecord type={recordType} name={preview.console} value={ingressTarget || "your public ingress"} label="Midnight console" />
            <DNSRecord type={recordType} name={preview.apps} value={ingressTarget || "your public ingress"} label="Generated service domains" />
            <p className="m-0 text-[11px] text-muted">
              Keep DNS proxying off until the records resolve and HTTPS is issued. The console hostname opens a public Midnight page at /; administrators sign in at /login. Existing service custom domains stay unchanged.
            </p>
          </div>

          {query.data?.restart_required ? (
            <div className="rounded-[var(--r-sm)] border border-[var(--warn,#d29922)]/40 bg-surface-2 px-3 py-2 text-xs text-muted" role="status">
              Saved. Restart <span className="font-mono text-text">midnight-server</span> to activate generated domains under <span className="font-mono text-text">{query.data.apps_domain}</span>.
            </div>
          ) : null}
          {save.isSuccess && !save.data.restart_required ? (
            <p className="m-0 text-xs text-[var(--ok,#3fb950)]" role="status">Domain setup saved and active.</p>
          ) : null}
          {save.error ? <InlineError error={save.error} /> : null}

          <div className="flex items-center justify-between gap-3">
            <p className="m-0 text-[11px] text-muted">
              Generated services will use <span className="font-mono text-text">{preview.apps}</span>.
            </p>
            <Button type="submit" disabled={save.isPending || !rootDomain.trim() || !ingressTarget.trim()}>
              {save.isPending ? "Saving…" : "Save domain setup"}
            </Button>
          </div>
        </form>
      )}
    </Card>
  );
}

function DNSRecord({ type, name, value, label }: { type: string; name: string; value: string; label: string }) {
  return (
    <div className="grid gap-1 rounded-[var(--r-sm)] border border-line-soft bg-surface px-3 py-2 sm:grid-cols-[70px_1fr_1fr] sm:items-center">
      <span className="font-mono text-[10px] font-semibold text-accent">{type}</span>
      <span className="font-mono text-text">{name}</span>
      <span className="min-w-0 truncate font-mono text-muted" title={value}>{value}</span>
      <span className="text-[10px] text-subtle sm:col-start-2 sm:col-span-2">{label}</span>
    </div>
  );
}

function looksLikeIPAddress(value: string): boolean {
  return /^(?:\d{1,3}\.){3}\d{1,3}$/.test(value) || value.includes(":");
}
