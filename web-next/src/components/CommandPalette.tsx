// Command palette (Cmd/Ctrl+K).
//
// Self-contained: it attaches its own global keydown listener, renders through
// a portal to <body>, traps focus, and closes on Escape. Mount it ONCE near
// the app root (see the mount note at the bottom of this file). It reads the
// current route context via props and pulls its own data from the query hooks.
//
// Actions: deploy latest · restart service · jump to project · jump to service
//          · open logs · ask agent.

import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { createPortal } from "react-dom";
import type { useNavigate } from "@tanstack/react-router";
import {
  useEnvironmentResources,
  useProjects,
  useServices,
} from "@/api/hooks";
import { useDeploymentActions } from "@/api/deployActions";
import { useAuth } from "@/auth/AuthProvider";
import { setAgentDockCollapsed } from "@/components/AgentPanel/agent-dock-state";
import { useProjectAccess } from "@/components/panels/automation-tokens/api";
import { useDeploymentMutationAuthority } from "@/components/panels/deployments/authority";
import {
  ADD_RESOURCE_CATALOG,
  resourceDefinition,
} from "@/components/panels/add-resource/catalog";
import {
  bindProductAction,
	PRODUCT_ACTIONS,
  type ProductActionID,
} from "@/lib/productActions";

type NavigateFn = ReturnType<typeof useNavigate>;

export interface CommandPaletteContext {
  projectId?: string | null;
  serviceId?: string | null;
  environmentId?: string | null;
}

export interface CommandPaletteProps {
  /** The router navigate fn — pass `useNavigate()` from the mount site. */
  navigate: NavigateFn;
  /** Current route context (project / service / env) for scoped actions. */
  context?: CommandPaletteContext;
  /**
   * Open the agent dock. Defaults to un-collapsing the shared dock so the
   * palette can "ask agent" without the caller wiring anything.
   */
  onAskAgent?: () => void;
  /** Controlled override — otherwise the palette owns its own open state. */
  open?: boolean;
  onOpenChange?: (open: boolean) => void;
}

type Command = {
  id: string;
  label: string;
  section: string;
  hint?: string;
  keywords?: string;
  disabled?: boolean;
  disabledReason?: string;
  run: () => void;
};

export function CommandPalette({
  navigate,
  context,
  onAskAgent,
  open: controlledOpen,
  onOpenChange,
}: CommandPaletteProps) {
  const [uncontrolledOpen, setUncontrolledOpen] = useState(false);
  const open = controlledOpen ?? uncontrolledOpen;
  const setOpen = useCallback(
    (next: boolean) => {
      onOpenChange?.(next);
      if (controlledOpen === undefined) setUncontrolledOpen(next);
    },
    [controlledOpen, onOpenChange],
  );

  const [query, setQuery] = useState("");
  const [active, setActive] = useState(0);
  const inputRef = useRef<HTMLInputElement>(null);
  const listRef = useRef<HTMLDivElement>(null);
  const restoreFocusRef = useRef<HTMLElement | null>(null);
  const goSequenceRef = useRef(0);

  const projectId = context?.projectId ?? undefined;
  const serviceId = context?.serviceId ?? undefined;
  const environmentId = context?.environmentId ?? undefined;
	const auth = useAuth();
	const humanSession = auth.state.status === "human";
	const actionAccess = useProjectAccess(
	  projectId ?? "",
	  environmentId,
	  humanSession && Boolean(projectId),
	);

  // Data for jump targets + deploy actions. Queries are cheap and cached.
  const projects = useProjects();
  const services = useServices(projectId, environmentId);
  const environmentResources = useEnvironmentResources(environmentId, {
    enabled: Boolean(serviceId && environmentId),
  });
  const selectedResource = environmentResources.data?.resources.find(
    (resource) => resource.service.id === serviceId,
  );
  const { redeploy, restart } = useDeploymentActions(serviceId);
  const deploymentId =
    selectedResource?.active_deployment?.id ??
    (selectedResource?.latest_deployment?.active
      ? selectedResource.latest_deployment.id
      : undefined);
  const deploymentAuthority = useDeploymentMutationAuthority(
    projectId,
    environmentId,
  );

  const askAgent = useCallback(() => {
    if (onAskAgent) onAskAgent();
    else setAgentDockCollapsed(false);
  }, [onAskAgent]);

  // ── Global Cmd/Ctrl+K toggle ────────────────────────────────────
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && (e.key === "k" || e.key === "K")) {
        e.preventDefault();
        setOpen(!open);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [open, setOpen]);

  // Reset query + selection each time it opens; restore focus on close.
  useEffect(() => {
    if (open) {
      restoreFocusRef.current = document.activeElement as HTMLElement | null;
      setQuery("");
      setActive(0);
      // Focus after paint so the portal node exists.
      requestAnimationFrame(() => inputRef.current?.focus());
    } else {
      restoreFocusRef.current?.focus?.();
    }
  }, [open]);

  const close = useCallback(() => setOpen(false), [setOpen]);

  const goService = useCallback(
    (svcId: string, section = "overview", configure?: string) => {
      if (!projectId) return;
      navigate({
        to: "/projects/$projectId/services/$serviceId/$section",
        params: { projectId, serviceId: svcId, section },
        search: { env: environmentId, configure },
      } as never);
    },
    [navigate, projectId, environmentId],
  );

  // Route shortcuts mirror visible service navigation. They do not fire from
  // inputs, editors, or selects, where letter keys belong to the control.
  useEffect(() => {
    const onShortcut = (event: KeyboardEvent) => {
      if (isTextEntry(event.target)) return;
      const key = event.key.toLocaleLowerCase();

      if (
        key === "a" &&
        event.shiftKey &&
        (event.metaKey || event.ctrlKey) &&
        projectId &&
        environmentId
      ) {
        event.preventDefault();
        navigate({
          to: "/projects/$projectId",
          params: { projectId },
          search: { env: environmentId, tab: "add-service" },
        } as never);
        return;
      }

      if (event.metaKey || event.ctrlKey || event.altKey || event.shiftKey) return;
      const now = Date.now();
      if (key === "g") {
        goSequenceRef.current = now;
        return;
      }
      if (!serviceId || now - goSequenceRef.current > 1_200) {
        goSequenceRef.current = 0;
        return;
      }
      const target: Record<string, { section: string; configure?: string }> = {
        o: { section: "overview" },
        d: { section: "deployments" },
        l: { section: "logs" },
        m: { section: "metrics" },
        c: { section: "configure" },
      };
      const destination = target[key];
      goSequenceRef.current = 0;
      if (!destination) return;
      event.preventDefault();
      goService(serviceId, destination.section, destination.configure);
    };
    window.addEventListener("keydown", onShortcut);
    return () => window.removeEventListener("keydown", onShortcut);
  }, [environmentId, goService, navigate, projectId, serviceId]);

  // ── Command set ─────────────────────────────────────────────────
  const commands = useMemo<Command[]>(() => {
    const list: Command[] = [];
    const add = (
      id: ProductActionID,
      run: () => void,
      options?: {
        available?: boolean;
        disabledReason?: string;
        setupAction?: ProductActionID;
        shortcut?: string;
        label?: string;
        keywords?: string;
      },
    ) => {
		const definition = PRODUCT_ACTIONS[id];
		const requiredCapability =
		  "capability" in definition ? definition.capability : undefined;
		const exactAccess =
		  !humanSession ||
		  (actionAccess.data?.project_id === projectId &&
			(!environmentId ||
			  actionAccess.data?.environment_id === environmentId));
		const authorized =
		  !humanSession ||
		  requiredCapability === undefined ||
		  (exactAccess &&
			actionAccess.data?.permissions.includes(requiredCapability));
		const contextAvailable = options?.available !== false;
		const action = bindProductAction(id, {
		  ...options,
		  available: contextAvailable && authorized,
		  disabledReason: !contextAvailable
			? options?.disabledReason
			: !authorized
			  ? actionAccess.isPending
				? "Checking server-reported project permissions…"
				: actionAccess.error || !exactAccess
				  ? "Midnight could not verify this action's exact project and environment authority."
				  : `The current project role does not grant ${requiredCapability}.`
			  : options?.disabledReason,
		});
      list.push({
        id: action.id,
        label: options?.label ?? action.label,
        section: action.section,
        keywords: `${action.keywords} ${options?.keywords ?? ""}`,
        disabled: action.disabled,
        disabledReason: action.disabledReason,
        hint: action.disabledReason ?? options?.shortcut,
        run,
      });
    };

    add("project.create", () => {
      navigate({ to: "/new" } as never);
      close();
    });

    if (projectId) {
      const openResourceFlow = (
        id:
          | "resource.add"
          | "resource.add.github"
          | "resource.add.image"
          | "resource.add.database"
          | "resource.add.template"
          | "resource.add.volume",
        resource?: "github" | "image" | "database" | "template" | "volume",
      ) => {
        const catalogResource = resource ? resourceDefinition(resource) : undefined;
        add(id, () => {
          navigate({
            to: "/projects/$projectId",
            params: { projectId },
            search: { env: environmentId, tab: "add-service", resource },
          } as never);
          close();
        }, {
          available: Boolean(environmentId),
          disabledReason: "Select an environment before adding a resource.",
          setupAction: "environment.settings.open",
          shortcut: id === "resource.add" ? "⇧⌘/Ctrl A" : undefined,
          label: catalogResource ? `Add ${catalogResource.title}` : undefined,
          keywords: catalogResource
            ? `${catalogResource.category} ${catalogResource.keywords.join(" ")}`
            : ADD_RESOURCE_CATALOG.flatMap((item) => item.keywords).join(" "),
        });
      };
      openResourceFlow("resource.add");
      openResourceFlow("resource.add.github", "github");
      openResourceFlow("resource.add.image", "image");
      openResourceFlow("resource.add.database", "database");
      openResourceFlow("resource.add.template", "template");
      openResourceFlow("resource.add.volume", "volume");
      add("project.deployments.open", () => {
        navigate({ to: "/projects/$projectId/deployments", params: { projectId }, search: { env: environmentId } } as never);
        close();
      });
      add("project.settings.open", () => {
        navigate({ to: "/projects/$projectId/settings", params: { projectId }, search: { env: environmentId } } as never);
        close();
      });
      add("environment.create", () => {
        navigate({ to: "/projects/$projectId/settings", params: { projectId }, search: { env: environmentId } } as never);
        close();
      });
      add("topology.grid.open", () => {
        navigate({ to: "/projects/$projectId", params: { projectId }, search: { env: environmentId } } as never);
        close();
      }, { available: Boolean(environmentId), disabledReason: "Select an environment to open its resource grid." });
      add("topology.map.open", () => {
        navigate({ to: "/projects/$projectId", params: { projectId }, search: { env: environmentId, view: "map" } } as never);
        close();
      }, { available: Boolean(environmentId), disabledReason: "Select an environment to open its topology map." });
    }

    if (serviceId) {
      add("service.open", () => {
        goService(serviceId);
        close();
      }, { shortcut: "G O" });
      add("service.deployments.open", () => {
        goService(serviceId, "deployments");
        close();
      }, { shortcut: "G D" });
      add("deployment.redeploy", () => {
          if (deploymentId) redeploy.mutate(deploymentId);
          close();
        }, {
          available: Boolean(deploymentId) && deploymentAuthority.canReplay,
          disabledReason: !deploymentId
            ? "Deploy this service once before replaying its image."
            : "The current project role cannot trigger deployments.",
          setupAction: !deploymentId ? "service.open" : undefined,
        },
      );
      add("deployment.restart", () => {
          if (deploymentId) restart.mutate(deploymentId);
          close();
        }, {
          available: Boolean(deploymentId) && deploymentAuthority.canRuntime,
          disabledReason: !deploymentId
            ? "Deploy this service once before restarting it."
            : "The current project role cannot run service actions.",
          setupAction: !deploymentId ? "service.open" : undefined,
        },
      );
      add("service.logs.open", () => {
          goService(serviceId, "logs");
          close();
        }, { shortcut: "G L" },
      );
      add("service.metrics.open", () => {
          goService(serviceId, "metrics");
          close();
        }, { shortcut: "G M" },
      );
      add("service.configure.open", () => {
          goService(serviceId, "configure");
          close();
        }, { shortcut: "G C" },
      );
      add("service.source.open", () => {
          goService(serviceId, "configure", "source");
          close();
        },
      );
      add("service.variables.open", () => {
          goService(serviceId, "configure", "variables");
          close();
        },
      );
      add("service.networking.open", () => {
          goService(serviceId, "configure", "networking");
          close();
        },
      );
      add("service.storage.open", () => {
          goService(serviceId, "configure", "storage");
          close();
        },
      );
      add("service.runtime.open", () => {
          goService(serviceId, "configure", "runtime");
          close();
        },
      );
      add("service.domain.generate", () => {
          goService(serviceId, "configure", "networking");
          close();
        },
      );
      add("service.endpoint.open", () => {
          if (selectedResource?.endpoint?.url) {
            window.open(selectedResource.endpoint.url, "_blank", "noopener,noreferrer");
          }
          close();
        }, {
          available: Boolean(selectedResource?.endpoint?.url),
          disabledReason: "This service has no verified public endpoint yet.",
          setupAction: "service.domain.generate",
        },
      );
      add("service.private-address.copy", () => {
          const privateHost = selectedResource?.private_endpoint?.host;
          if (privateHost && navigator.clipboard) {
            void navigator.clipboard.writeText(privateHost).catch(() => undefined);
          }
          close();
        }, {
          available: Boolean(selectedResource?.private_endpoint?.host),
          disabledReason: "Select a service in an environment with a private DNS identity.",
        },
      );
    }

    add("agent.ask", () => {
        askAgent();
        close();
      });

    // Jump to service (current project scope).
    for (const svc of services.data ?? []) {
      list.push({
        id: `svc-${svc.id}`,
        label: svc.display_name || svc.slug,
        section: "Jump to service",
        hint: "service",
        keywords: `${svc.slug} ${svc.id} service`,
        run: () => {
          goService(svc.id);
          close();
        },
      });
    }

    // Jump to project (all projects).
    for (const proj of projects.data ?? []) {
      list.push({
        id: `proj-${proj.id}`,
        label: proj.display_name || proj.slug,
        section: "Jump to project",
        hint: "project",
        keywords: `${proj.slug} ${proj.id} project`,
        run: () => {
          navigate({
            to: "/projects/$projectId",
            params: { projectId: proj.id },
            search: {},
          } as never);
          close();
        },
      });
    }

    return list;
  }, [
    serviceId,
    projectId,
    environmentId,
    deploymentId,
    selectedResource,
    services.data,
    projects.data,
    redeploy,
    restart,
    deploymentAuthority.canReplay,
    deploymentAuthority.canRuntime,
	actionAccess.data,
	actionAccess.error,
	actionAccess.isPending,
	humanSession,
    goService,
    askAgent,
    navigate,
    close,
  ]);

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return commands;
    return commands.filter((c) =>
      `${c.label} ${c.section} ${c.keywords ?? ""}`.toLowerCase().includes(q),
    );
  }, [commands, query]);

  // Keep the active index in range as the filter narrows.
  useEffect(() => {
    setActive((a) => Math.min(a, Math.max(0, filtered.length - 1)));
  }, [filtered.length]);

  const runActive = useCallback(() => {
    const cmd = filtered[active];
    if (cmd && !cmd.disabled) cmd.run();
  }, [filtered, active]);

  const onKeyDown = useCallback(
    (e: React.KeyboardEvent) => {
      if (e.key === "Escape") {
        e.preventDefault();
        close();
        return;
      }
      if (e.key === "ArrowDown" || (e.key === "Tab" && !e.shiftKey)) {
        e.preventDefault();
        setActive((a) => nextEnabled(filtered, a, 1));
        return;
      }
      if (e.key === "ArrowUp" || (e.key === "Tab" && e.shiftKey)) {
        e.preventDefault();
        setActive((a) => nextEnabled(filtered, a, -1));
        return;
      }
      if (e.key === "Enter") {
        e.preventDefault();
        runActive();
      }
    },
    [filtered, close, runActive],
  );

  // Scroll the active row into view.
  useEffect(() => {
    if (!open) return;
    const el = listRef.current?.querySelector<HTMLElement>(
      `[data-index="${active}"]`,
    );
    el?.scrollIntoView?.({ block: "nearest" });
  }, [active, open]);

  if (!open) return null;

  // Group filtered commands by section for rendering while keeping a flat
  // index for keyboard nav.
  let runningIndex = -1;
  const sections: { name: string; items: { cmd: Command; index: number }[] }[] = [];
  for (const cmd of filtered) {
    runningIndex += 1;
    const idx = runningIndex;
    const bucket = sections.find((s) => s.name === cmd.section);
    if (bucket) bucket.items.push({ cmd, index: idx });
    else sections.push({ name: cmd.section, items: [{ cmd, index: idx }] });
  }

  return createPortal(
    <div
      className="fixed inset-0 z-[200] flex items-start justify-center bg-black/50 px-4 pt-[12vh] backdrop-blur-sm"
      onMouseDown={(e) => {
        if (e.target === e.currentTarget) close();
      }}
    >
      <div
        role="dialog"
        aria-modal="true"
        aria-label="Command palette"
        className="flex w-full max-w-lg flex-col overflow-hidden rounded-xl border border-line-strong bg-surface shadow-2xl"
        onKeyDown={onKeyDown}
      >
        <div className="flex items-center gap-2 border-b border-line px-3">
          <span aria-hidden="true" className="text-subtle">
            ⌘
          </span>
          <input
            ref={inputRef}
            type="text"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="Search actions, services, projects…"
            aria-label="Command palette search"
            role="combobox"
            aria-expanded="true"
            aria-controls="command-palette-list"
            aria-activedescendant={
              filtered[active] ? `cmd-${filtered[active].id}` : undefined
            }
            className="w-full bg-transparent py-3 text-sm text-text outline-none placeholder:text-subtle"
          />
          <kbd className="rounded border border-line px-1.5 py-0.5 font-mono text-[10px] text-subtle">
            esc
          </kbd>
        </div>

        <div
          id="command-palette-list"
          ref={listRef}
          role="listbox"
          aria-label="Commands"
          className="max-h-[52vh] overflow-y-auto p-1.5"
        >
          {filtered.length === 0 ? (
            <div className="px-3 py-6 text-center text-sm text-muted">
              No matching actions
            </div>
          ) : (
            sections.map((section) => (
              <div key={section.name} className="mb-1">
                <div className="px-2 pb-1 pt-2 font-mono text-[10px] uppercase tracking-wider text-subtle">
                  {section.name}
                </div>
                {section.items.map(({ cmd, index }) => (
                  <div
                    key={cmd.id}
                    id={`cmd-${cmd.id}`}
                    data-index={index}
                    role="option"
                    aria-selected={index === active}
                    aria-disabled={cmd.disabled}
                    onMouseEnter={() => setActive(index)}
                    onMouseDown={(e) => {
                      e.preventDefault();
                      if (!cmd.disabled) cmd.run();
                    }}
                    title={cmd.disabledReason}
                    className={[
                      "flex cursor-pointer items-center justify-between gap-3 rounded-lg px-3 py-2 text-sm",
                      cmd.disabled
                        ? "cursor-not-allowed text-subtle"
                        : "text-text",
                      index === active && !cmd.disabled
                        ? "bg-accent-soft"
                        : "",
                    ].join(" ")}
                  >
                    <span className="truncate">{cmd.label}</span>
                    {cmd.hint ? (
                      <span className="max-w-[46%] shrink-0 truncate text-right font-mono text-[10px] text-subtle">
                        {cmd.hint}
                      </span>
                    ) : null}
                  </div>
                ))}
              </div>
            ))
          )}
        </div>
      </div>
    </div>,
    document.body,
  );
}

/** Move `from` by `dir`, skipping disabled commands, wrapping around. */
function nextEnabled(list: Command[], from: number, dir: 1 | -1): number {
  if (list.length === 0) return 0;
  let i = from;
  for (let step = 0; step < list.length; step += 1) {
    i = (i + dir + list.length) % list.length;
    if (!list[i].disabled) return i;
  }
  return from;
}

function isTextEntry(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false;
  return (
    target.isContentEditable ||
    target instanceof HTMLInputElement ||
    target instanceof HTMLTextAreaElement ||
    target instanceof HTMLSelectElement
  );
}

/*
 * ── Integrator mount note ─────────────────────────────────────────
 *
 * CommandPalette is a leaf; it must be mounted ONCE inside the router tree
 * (so `useNavigate()` is valid) — the natural home is `RootLayout` in
 * `src/routes/routes.tsx`, rendered as a sibling of <Outlet />:
 *
 *   import { useNavigate } from "@tanstack/react-router";
 *   import { CommandPalette } from "@/components/CommandPalette";
 *   // inside RootLayout(), reusing the vars already computed there:
 *   //   const navigate = useNavigate();
 *   //   const { projectId, serviceId, environmentId } already exist.
 *   <CommandPalette
 *     navigate={navigate}
 *     context={{ projectId, serviceId, environmentId }}
 *   />
 *
 * It renders nothing until Cmd/Ctrl+K (owns its own open state). Pass
 * `open`/`onOpenChange` only if you want to also open it from a TopNav button.
 * `onAskAgent` is optional — it defaults to un-collapsing the agent dock.
 */
