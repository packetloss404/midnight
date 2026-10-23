// Stable product-action vocabulary shared by console surfaces. These records
// describe intent only; execution remains bound to the existing API/route
// handlers so UI metadata cannot grant authority.

export type ActionScope = "global" | "project" | "environment" | "service" | "deployment";

export type ProductCapability =
  | "project.read"
  | "project.manage"
  | "service.read"
  | "service.write"
  | "environment.write"
  | "deployment.trigger.cli"
  | "logs.read"
  | "metrics.read"
  | "variables.write"
  | "domains.write"
  | "volume.write";

export const PRODUCT_ACTIONS = {
  "project.create": { label: "New project", section: "Create", scope: "global", keywords: "create project workspace" },
  "project.open": { label: "Open project", section: "Navigate", scope: "project", capability: "project.read", keywords: "project topology" },
  "project.deployments.open": { label: "Open deployments", section: "Project", scope: "project", capability: "service.read", keywords: "history releases deploy" },
  "project.settings.open": { label: "Open project settings", section: "Project", scope: "project", capability: "project.read", keywords: "settings members webhooks" },
  "resource.add": { label: "Add resource", section: "Create", scope: "project", capability: "service.write", keywords: "service database volume template github image" },
  "resource.add.github": { label: "Add from GitHub", section: "Create", scope: "project", capability: "service.write", keywords: "repository repo source service" },
  "resource.add.image": { label: "Add container image", section: "Create", scope: "project", capability: "service.write", keywords: "docker oci digest service" },
  "resource.add.database": { label: "Add database", section: "Create", scope: "project", capability: "service.write", keywords: "postgres redis mysql mongodb template" },
  "resource.add.template": { label: "Add template", section: "Create", scope: "project", capability: "service.write", keywords: "starter community topology" },
  "resource.add.volume": { label: "Add volume", section: "Create", scope: "project", capability: "volume.write", keywords: "storage disk persistent mount" },
  "environment.create": { label: "Create environment", section: "Environment", scope: "project", capability: "environment.write", keywords: "environment staging preview production" },
  "environment.settings.open": { label: "Manage environments", section: "Environment", scope: "project", capability: "environment.write", keywords: "environment settings edit" },
  "topology.grid.open": { label: "Show resource grid", section: "Project", scope: "environment", capability: "project.read", keywords: "grid resources" },
  "topology.map.open": { label: "Show topology map", section: "Project", scope: "environment", capability: "project.read", keywords: "map architecture canvas" },
  "topology.zoom.out": { label: "Zoom out", section: "Topology", scope: "environment", capability: "project.read", keywords: "zoom smaller" },
  "topology.zoom.in": { label: "Zoom in", section: "Topology", scope: "environment", capability: "project.read", keywords: "zoom larger" },
  "topology.fit": { label: "Fit all services", section: "Topology", scope: "environment", capability: "project.read", keywords: "fit viewport" },
  "topology.center": { label: "Center selected service", section: "Topology", scope: "service", capability: "project.read", keywords: "center focus selection" },
  "topology.arrange": { label: "Auto-layout services", section: "Topology", scope: "environment", capability: "project.manage", keywords: "arrange layout graph" },
  "topology.reset": { label: "Reset saved layout", section: "Topology", scope: "environment", capability: "project.manage", keywords: "reset coordinates" },
  "service.open": { label: "Open service", section: "This service", scope: "service", capability: "service.read", keywords: "service overview" },
  "service.deployments.open": { label: "Open deployments", section: "This service", scope: "service", capability: "service.read", keywords: "history rollout releases" },
  "service.logs.open": { label: "Open logs", section: "This service", scope: "service", capability: "logs.read", keywords: "logs output tail" },
  "service.metrics.open": { label: "Open metrics", section: "This service", scope: "service", capability: "metrics.read", keywords: "cpu memory network disk telemetry" },
  "service.configure.open": { label: "Configure service", section: "This service", scope: "service", capability: "service.read", keywords: "settings configure source variables networking storage runtime" },
  "service.source.open": { label: "Configure source", section: "This service", scope: "service", capability: "service.write", keywords: "github repository branch image root" },
  "service.variables.open": { label: "Configure variables", section: "This service", scope: "service", capability: "variables.write", keywords: "environment variables secrets config" },
  "service.networking.open": { label: "Configure networking", section: "This service", scope: "service", capability: "domains.write", keywords: "domain route tcp network" },
  "service.storage.open": { label: "Configure storage", section: "This service", scope: "service", capability: "volume.write", keywords: "volume backup restore persistent mount" },
  "service.runtime.open": { label: "Configure runtime", section: "This service", scope: "service", capability: "service.write", keywords: "replicas cron health restart start command" },
  "service.domain.generate": { label: "Generate public domain", section: "This service", scope: "service", capability: "domains.write", keywords: "public url expose networking" },
  "service.endpoint.open": { label: "Open public URL", section: "This service", scope: "service", capability: "service.read", keywords: "domain endpoint live app" },
  "service.private-address.copy": { label: "Copy private address", section: "This service", scope: "service", capability: "service.read", keywords: "internal dns hostname network clipboard" },
  "deployment.redeploy": { label: "Deploy latest", section: "This service", scope: "deployment", capability: "deployment.trigger.cli", keywords: "deploy redeploy ship release" },
  "deployment.restart": { label: "Restart service", section: "This service", scope: "deployment", capability: "service.write", keywords: "restart bounce reboot" },
  "agent.ask": { label: "Ask the agent", section: "Agent", scope: "global", keywords: "ai assistant chat ask help" },
} as const satisfies Record<string, {
  label: string;
  section: string;
  scope: ActionScope;
	capability?: ProductCapability;
  keywords: string;
}>;

export type ProductActionID = keyof typeof PRODUCT_ACTIONS;

export type BoundProductAction = (typeof PRODUCT_ACTIONS)[ProductActionID] & {
  id: ProductActionID;
  disabled: boolean;
  disabledReason?: string;
  setupAction?: ProductActionID;
};

export function actionLabel(id: ProductActionID): string {
  return PRODUCT_ACTIONS[id].label;
}

export function bindProductAction(
  id: ProductActionID,
  options: {
    available?: boolean;
    disabledReason?: string;
    setupAction?: ProductActionID;
  } = {},
): BoundProductAction {
  const definition = PRODUCT_ACTIONS[id];
  const disabled = options.available === false;
  return {
    ...definition,
    id,
    disabled,
    disabledReason: disabled
      ? options.disabledReason?.trim() || "This action is unavailable in the current scope."
      : undefined,
    setupAction: disabled ? options.setupAction : undefined,
  };
}

/** Redaction-safe metadata suitable for an agent/tool presentation layer. */
export function productActionCatalog() {
  return (Object.keys(PRODUCT_ACTIONS) as ProductActionID[]).map((id) => ({
    id,
    ...PRODUCT_ACTIONS[id],
  }));
}
