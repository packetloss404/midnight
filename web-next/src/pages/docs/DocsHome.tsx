import { Link } from "@tanstack/react-router";
import type { ReactNode } from "react";
import {
  Callout,
  CommandBlock,
  DefinitionGrid,
  DocsHero,
  DocsPage,
} from "./DocsContent";

export type DocsHomeProps = {
  apiReferenceHref?: string;
  canonicalSpecHref?: string;
};

const FIRST_REQUEST = `curl --fail-with-body \\
  "$MIDNIGHT_URL/api/v1alpha/auth/me" \\
  -H "Authorization: Bearer $MIDNIGHT_TOKEN" \\
  -H 'Accept: application/json, application/problem+json'`;

const FOUNDATIONS = [
  {
    number: "01",
    title: "Projects define ownership",
    detail:
      "A project belongs to one workspace. Human access is derived from accepted workspace membership or an exact project grant.",
  },
  {
    number: "02",
    title: "Environments isolate desired state",
    detail:
      "Configuration, variables, domains, deployments, and runtime evidence are scoped to a concrete environment.",
  },
  {
    number: "03",
    title: "Services describe workloads",
    detail:
      "A service can build source or run an immutable image. Saving desired state never implies that a deployment occurred.",
  },
  {
    number: "04",
    title: "Operations prove progress",
    detail:
      "Asynchronous mutations return an operation. Read it or follow its event stream until it reaches a terminal state.",
  },
] as const;

const JOURNEY = [
  ["Create a project", "Choose an explicit workspace and create the tenant boundary."],
  ["Add an environment", "Use production, staging, preview, or development as an honest policy label."],
  ["Create a service", "Keep the service private until public routing is explicitly requested."],
  ["Deploy immutable input", "Submit a digest-pinned image or a validated gzipped source archive."],
  ["Follow the operation", "A 202 response means queued—not built, healthy, or reachable."],
] as const;

export function DocsHome({
  apiReferenceHref = "/docs/api",
  canonicalSpecHref = "/api/v1alpha/openapi.yaml",
}: DocsHomeProps) {
  return (
    <DocsPage>
      <DocsHero
        eyebrow="Midnight documentation"
        title="Ship software on infrastructure you control."
        description="Learn the tenant model, deploy immutable workloads, and automate the control plane without weakening Midnight's authorization boundaries."
        badges={["self-hosted", "v1alpha", "/api/v1alpha"]}
      />
      <div className="flex flex-wrap items-center gap-2">
        <Link
          to={apiReferenceHref}
          className="inline-flex min-h-10 items-center rounded-[var(--r-sm)] border border-accent bg-accent px-4 text-xs font-semibold text-[var(--accent-ink)] transition hover:brightness-105"
        >
          Explore the API <span className="ml-2" aria-hidden="true">→</span>
        </Link>
        <a
          href={canonicalSpecHref}
          className="inline-flex min-h-10 items-center rounded-[var(--r-sm)] border border-line-strong bg-surface-2 px-4 text-xs font-semibold text-text transition hover:bg-surface-3"
        >
          Download OpenAPI <span className="ml-2" aria-hidden="true">↓</span>
        </a>
        <p className="m-0 basis-full text-xs leading-relaxed text-muted sm:ml-auto sm:basis-auto">
          The embedded OpenAPI document is the authoritative contract.
        </p>
      </div>

      <section aria-labelledby="docs-start-title" className="grid gap-5">
        <div className="grid gap-2">
          <SectionEyebrow>Start here</SectionEyebrow>
          <h2 id="docs-start-title" className="m-0 text-2xl font-semibold tracking-tight text-text">
            One model from source to runtime
          </h2>
          <p className="m-0 max-w-3xl text-sm leading-6 text-muted">
            Midnight separates desired state from deployment evidence. That distinction keeps the
            API useful during retries, worker restarts, and partial infrastructure failures.
          </p>
        </div>
        <DefinitionGrid
          items={FOUNDATIONS.map((item) => ({
            term: `${item.number} · ${item.title}`,
            detail: item.detail,
          }))}
        />
      </section>

      <section aria-labelledby="docs-journey-title" className="grid gap-5 lg:grid-cols-[minmax(0,.82fr)_minmax(0,1.18fr)] lg:items-start">
        <div className="grid gap-4">
          <div className="grid gap-2">
            <SectionEyebrow>First deployment</SectionEyebrow>
            <h2 id="docs-journey-title" className="m-0 text-2xl font-semibold tracking-tight text-text">
              The reliable path
            </h2>
          </div>
          <ol className="m-0 grid list-none gap-0 p-0">
            {JOURNEY.map(([title, detail], index) => (
              <li key={title} className="relative grid grid-cols-[2rem_1fr] gap-3 pb-5 last:pb-0">
                {index < JOURNEY.length - 1 ? (
                  <span className="absolute left-[0.9375rem] top-7 h-[calc(100%-1rem)] w-px bg-line" aria-hidden="true" />
                ) : null}
                <span className="relative z-[1] grid h-8 w-8 place-items-center rounded-full border border-line-strong bg-bg font-mono text-[10px] font-semibold text-accent">
                  {index + 1}
                </span>
                <div className="pt-1">
                  <h3 className="m-0 text-sm font-semibold text-text">{title}</h3>
                  <p className="m-0 mt-1 text-xs leading-relaxed text-muted">{detail}</p>
                </div>
              </li>
            ))}
          </ol>
        </div>
        <div className="grid gap-3">
          <CommandBlock label="Verify your API session" code={FIRST_REQUEST} />
          <Callout title="Use the credential for its declared authority" tone="warning">
            <p className="m-0">
              Human sessions are for people, project automation tokens are deliberately narrow CI
              credentials, and runtime-agent secrets are accepted only by their exact agent routes.
              Midnight does not silently fall back from one credential class to another.
            </p>
          </Callout>
        </div>
      </section>

      <section aria-labelledby="docs-api-foundations-title" className="grid gap-5 rounded-[var(--r-lg)] border border-line bg-surface p-5 sm:p-6">
        <div className="flex flex-wrap items-end justify-between gap-3">
          <div className="grid gap-2">
            <SectionEyebrow>API foundations</SectionEyebrow>
            <h2 id="docs-api-foundations-title" className="m-0 text-xl font-semibold text-text">
              Retries are part of the contract
            </h2>
          </div>
          <Link to={apiReferenceHref} className="text-xs font-semibold text-accent hover:underline">
            Read the practical API reference →
          </Link>
        </div>
        <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-4">
          <FoundationCard
            title="Idempotent mutations"
            detail="Reuse one stable Idempotency-Key only for the same mutation and exact body. Authorized replays return the stored terminal response."
          >
            <span className="font-mono text-[10px] font-semibold text-accent">POST</span>
          </FoundationCard>
          <FoundationCard
            title="Conditional changes"
            detail="Where advertised, read the strong ETag and send it in If-Match. Missing and stale validators fail with 428 and 412."
          >
            <span className="font-mono text-[10px] font-semibold text-[var(--branch)]">PATCH</span>
          </FoundationCard>
          <FoundationCard
            title="Opaque pagination"
            detail="Resume next_cursor with the same actor, tenant, collection, filters, order, and page size. Never inspect or alter it."
          >
            <span className="font-mono text-[10px] text-info">next_cursor</span>
          </FoundationCard>
          <FoundationCard
            title="Machine-readable errors"
            detail="Failures use application/problem+json with a stable code and type URI. Do not branch on human-readable detail text."
          >
            <span className="font-mono text-[10px] text-warn">RFC 9457</span>
          </FoundationCard>
        </div>
      </section>
    </DocsPage>
  );
}

function FoundationCard({
  title,
  detail,
  children,
}: {
  title: string;
  detail: string;
  children: ReactNode;
}) {
  return (
    <article className="grid content-start gap-3 rounded-lg border border-line bg-bg p-4">
      <div className="min-h-5">{children}</div>
      <div className="grid gap-1.5">
        <h3 className="m-0 text-sm font-semibold text-text">{title}</h3>
        <p className="m-0 text-xs leading-relaxed text-muted">{detail}</p>
      </div>
    </article>
  );
}

function SectionEyebrow({ children }: { children: ReactNode }) {
  return (
    <p className="m-0 font-mono text-[11px] font-semibold uppercase tracking-wider text-accent">
      {children}
    </p>
  );
}
