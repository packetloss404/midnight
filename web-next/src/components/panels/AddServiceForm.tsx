// AddServiceForm — create a service in a project from a container image or a
// GitHub repo.
//
// PROP CONTRACT (stable):
//   projectId       required
//   environmentId?  env scope for an image deploy
//   onCreated?      (serviceId) => void  — caller navigates / selects
//
// Create is `POST /services` (REST, Idempotency-Key). Source binding:
//   - human image → exact REST `{environment_id,image}` with human auth
//   - operator image → `serviceInstanceDeployV2` (GraphQL)
//   - operator GitHub → `PUT /services/{id}/github`
// Human GitHub discovery is project-scoped and is intentionally implemented
// by HumanGithubResource rather than this global installation picker.
// (See ./create/api.ts and ./github/api.ts.)

import { useState, type ReactNode } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { deployServiceNow } from "@/api/deployActions";
import { Field, TextInput, Button, InlineError } from "@/components/panels/form-kit";
import { RepoPicker, type RepoSelection } from "./github/RepoPicker";
import { linkServiceGithub } from "./github/api";
import {
  createHumanService,
  createService,
  deployHumanServiceImage,
  deployServiceImage,
  isImmutableImageReference,
  newIdempotencyKey,
  slugify,
} from "./create/api";

export type AddServiceFormProps = {
  projectId: string;
  environmentId?: string;
  mutationAuthority?: "human" | "operator";
  imageDeployAvailable?: boolean;
  githubSourceAvailable?: boolean;
  installationAdminAvailable?: boolean;
  onCreated?: (serviceId: string) => void;
};

type SourceKind = "image" | "github";

export { isImmutableImageReference } from "./create/api";

type PendingImageIntent = {
  authority: "human" | "operator";
  projectId: string;
  environmentId: string;
  image: string;
  serviceName: string;
  generatePublicURL: boolean;
  createKey: string;
  deployKey: string;
  serviceId?: string;
};

export function AddServiceForm({
  projectId,
  environmentId,
  mutationAuthority = "operator",
  imageDeployAvailable = true,
  githubSourceAvailable = true,
  installationAdminAvailable = mutationAuthority === "operator",
  onCreated,
}: AddServiceFormProps) {
  const queryClient = useQueryClient();
  const [source, setSource] = useState<SourceKind>("image");
  const [name, setName] = useState("");
  const [image, setImage] = useState("");
  const [generatePublicURL, setGeneratePublicURL] = useState(false);
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [pendingImageIntent, setPendingImageIntent] =
    useState<PendingImageIntent | null>(null);
  const [createdGithubServiceId, setCreatedGithubServiceId] =
    useState<string | null>(null);
  // Linking a repository deliberately starts nothing. Without an explicit
  // first deployment a freshly linked service sits idle until someone pushes,
  // so the form offers to start one — off by default, and only when there is
  // an environment to deploy into.
  const [deployAfterLink, setDeployAfterLink] = useState(false);
  const [githubDeployKey, setGithubDeployKey] = useState<string | null>(null);
  const executableGithubSource =
    githubSourceAvailable && mutationAuthority === "operator";
  const sourceMutationsAvailable =
    imageDeployAvailable || executableGithubSource;
  const activeSource: SourceKind =
    source === "github" && executableGithubSource
      ? "github"
      : imageDeployAvailable
        ? "image"
        : executableGithubSource
          ? "github"
          : "image";

  function invalidateServices() {
    void queryClient.invalidateQueries({ queryKey: ["services"] });
  }

  async function submitServiceOnly() {
    if (busy) return;
    setError(null);
    if (!name.trim()) {
      setError("A service name is required.");
      return;
    }
    setBusy("service");
    try {
      const create =
        mutationAuthority === "human" ? createHumanService : createService;
      const service = await create(
        {
          projectId,
          name: name.trim(),
        },
        newIdempotencyKey(),
      );
      invalidateServices();
      onCreated?.(service.id);
    } catch (err) {
      setError(err);
    } finally {
      setBusy(null);
    }
  }

  async function submitImage() {
    if (busy) return;
    setError(null);
    if (!imageDeployAvailable) {
      setError("Your account cannot deploy images. Ask an administrator for deploy access.");
      return;
    }
    if (!environmentId) {
      setError("Select or create an environment before deploying an image.");
      return;
    }
    if (!image.trim()) {
      setError("A digest-pinned container image is required.");
      return;
    }
    if (!isImmutableImageReference(image)) {
      setError("Use an immutable image ending in @sha256: followed by 64 lowercase hex characters. Mutable tags are not accepted.");
      return;
    }
    setBusy("image");
    try {
      const normalizedImage = image.trim();
      const serviceName =
        name.trim() || normalizedImage.split("/").pop()!.split("@")[0];
      if (
        pendingImageIntent &&
        (pendingImageIntent.authority !== mutationAuthority ||
          pendingImageIntent.projectId !== projectId ||
          pendingImageIntent.environmentId !== environmentId ||
          pendingImageIntent.image !== normalizedImage ||
          pendingImageIntent.serviceName !== serviceName ||
          pendingImageIntent.generatePublicURL !== generatePublicURL)
      ) {
        setError(
          "A service was created for a different image-deploy request. Restore the original name, environment, image, and public URL choice before retrying; no deployment request was sent.",
        );
        return;
      }
      const intent: PendingImageIntent = pendingImageIntent ?? {
        authority: mutationAuthority,
        projectId,
        environmentId,
        image: normalizedImage,
        serviceName,
        generatePublicURL,
        createKey: newIdempotencyKey(),
        deployKey: newIdempotencyKey(),
      };
      setPendingImageIntent(intent);
      const create =
        mutationAuthority === "human" ? createHumanService : createService;
      const service = intent.serviceId
        ? { id: intent.serviceId }
        : await create(
            { projectId, name: serviceName },
            intent.createKey,
          );
      const deployIntent = { ...intent, serviceId: service.id };
      setPendingImageIntent(deployIntent);
      if (mutationAuthority === "human") {
        await deployHumanServiceImage(
          {
            serviceId: service.id,
            environmentId,
            image: normalizedImage,
            ...(intent.generatePublicURL
              ? { route: { public: true as const } }
              : {}),
          },
          intent.deployKey,
        );
      } else {
        await deployServiceImage(
          {
            serviceId: service.id,
            environmentId,
            image: normalizedImage,
          },
          intent.deployKey,
        );
      }
      setPendingImageIntent(null);
      invalidateServices();
      onCreated?.(service.id);
    } catch (err) {
      setError(err);
    } finally {
      setBusy(null);
    }
  }

  async function submitGithub({ installationId, repo }: RepoSelection) {
    if (busy) return;
    setError(null);
    if (
      mutationAuthority !== "operator" ||
      !installationAdminAvailable ||
      !executableGithubSource
    ) {
      setError("GitHub source linking requires installation-administrator access.");
      return;
    }
    setBusy(repo.full_name);
    try {
      const serviceName = name.trim() || repo.name || repo.full_name.split("/").pop()!;
      const service =
        createdGithubServiceId
          ? { id: createdGithubServiceId }
          : await createService({ projectId, name: serviceName });
      setCreatedGithubServiceId(service.id);
      await linkServiceGithub(service.id, {
        installationId,
        repo: repo.full_name,
        branch: repo.default_branch || "main",
        cloneUrl: `https://github.com/${repo.full_name}.git`,
      });
      if (deployAfterLink && environmentId) {
        // Held across retries so a failed response cannot become two
        // deployments; the link above is idempotent on the same service.
        const key = githubDeployKey ?? newIdempotencyKey();
        setGithubDeployKey(key);
        await deployServiceNow(service.id, environmentId, "operator", key);
        setGithubDeployKey(null);
      }
      setCreatedGithubServiceId(null);
      invalidateServices();
      onCreated?.(service.id);
    } catch (err) {
      setError(err);
    } finally {
      setBusy(null);
    }
  }

  const serviceNameField = (
    <Field
      label="Service name"
      hint={
        sourceMutationsAvailable
          ? "Optional — defaults from the image or repo name."
          : "Required for a service without a source."
      }
    >
      <TextInput
        value={name}
        onChange={(e) => setName(e.target.value)}
        placeholder="api"
      />
    </Field>
  );

  return (
    <div className="grid gap-4">
      {imageDeployAvailable && executableGithubSource ? (
        <div className="inline-flex gap-1 rounded-[var(--r-sm)] border border-line bg-surface-2 p-1 text-xs">
          <SourceTab active={activeSource === "image"} onClick={() => setSource("image")}>
            Container image
          </SourceTab>
          <SourceTab active={activeSource === "github"} onClick={() => setSource("github")}>
            GitHub repo
          </SourceTab>
        </div>
      ) : imageDeployAvailable ? (
        <div className="rounded-[var(--r-sm)] border border-info/40 bg-info-soft px-3 py-2 text-xs text-muted">
          Immutable image deployment is available for this environment. GitHub
          source linking still requires installation-administrator access.
        </div>
      ) : executableGithubSource ? null : (
        <div className="rounded-[var(--r-sm)] border border-info/40 bg-info-soft px-3 py-2 text-xs text-muted">
          Create the service now. To deploy an image you also need a selected
          environment and permission to create services and start deployments.
          Linking a GitHub source needs installation-administrator access.
        </div>
      )}

      {!sourceMutationsAvailable ? (
        <form
          className="grid gap-3"
          onSubmit={(event) => {
            event.preventDefault();
            void submitServiceOnly();
          }}
        >
          {serviceNameField}
          <p className="m-0 text-xs text-muted">
            Midnight will persist this service in the project without claiming
            that it has a source or deployment.
          </p>
          <div>
            <Button type="submit" disabled={busy !== null || !name.trim()}>
              {busy === "service" ? "Creating…" : "Create service"}
            </Button>
          </div>
        </form>
      ) : activeSource === "image" ? (
        <form
          className="grid gap-3"
          onSubmit={(e) => {
            e.preventDefault();
            void submitImage();
          }}
        >
          {serviceNameField}
          <Field
            label="Image reference"
            hint={
              environmentId
                ? "Use an immutable digest. Mutable tags such as :latest are rejected by the deploy API."
                : "Select or create an environment first; no service will be created until it can deploy."
            }
          >
            <TextInput
              value={image}
              onChange={(e) => setImage(e.target.value)}
              placeholder="registry.example/app@sha256:…"
              autoCapitalize="off"
              spellCheck={false}
            />
          </Field>
          {mutationAuthority === "human" ? (
            <label className="flex items-start gap-2 rounded-[var(--r-sm)] border border-line bg-surface-2 px-3 py-2.5">
              <input
                type="checkbox"
                checked={generatePublicURL}
                onChange={(event) =>
                  setGeneratePublicURL(event.target.checked)
                }
                className="mt-0.5"
              />
              <span className="grid gap-0.5">
                <span className="text-sm font-semibold text-text">
                  Generate a public URL
                </span>
                <span className="text-xs text-muted">
                  Request one generated route. Midnight reports the URL only
                  after the route controller publishes it; leave this unchecked
                  to keep the deployment private.
                </span>
              </span>
            </label>
          ) : null}
          <div>
            <Button type="submit" disabled={busy !== null || !environmentId}>
              {busy === "image" ? "Deploying…" : "Create and deploy"}
            </Button>
          </div>
        </form>
      ) : (
        <div className="grid gap-2">
          {serviceNameField}
          <p className="m-0 text-xs text-muted">
            Pick a repository — Midnight creates the service{name.trim() ? ` “${slugify(name)}”` : ""} and
            links it for push-to-deploy. Linking by itself starts no
            deployment.
          </p>
          {environmentId ? (
            <label className="flex items-start gap-2 rounded-[var(--r-sm)] border border-line bg-surface-2 px-3 py-2.5">
              <input
                type="checkbox"
                checked={deployAfterLink}
                onChange={(event) => setDeployAfterLink(event.target.checked)}
                className="mt-0.5"
              />
              <span className="grid gap-0.5">
                <span className="text-sm font-semibold text-text">
                  Deploy now after linking
                </span>
                <span className="text-xs text-muted">
                  Build the linked branch immediately instead of waiting for
                  the next push. The service and its link are kept even if the
                  build request fails.
                </span>
              </span>
            </label>
          ) : null}
          <RepoPicker onSelect={submitGithub} busyRepo={busy} ctaLabel="Create + link" />
        </div>
      )}

      {error ? <InlineError error={error} /> : null}
      {error &&
      ((activeSource === "image" && pendingImageIntent?.serviceId) ||
        (activeSource === "github" && createdGithubServiceId)) ? (
        <p className="m-0 text-xs text-muted" role="status">
          Service{" "}
          {activeSource === "image"
            ? pendingImageIntent?.serviceId
            : createdGithubServiceId}{" "}
          was created. Retry resumes that service instead of creating another.
        </p>
      ) : null}
    </div>
  );
}

function SourceTab({
  active,
  onClick,
  children,
}: {
  active: boolean;
  onClick: () => void;
  children: ReactNode;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={
        "rounded-[calc(var(--r-sm)-2px)] px-3 py-1.5 font-semibold transition-colors " +
        (active ? "bg-accent-soft text-accent" : "text-muted hover:text-text")
      }
    >
      {children}
    </button>
  );
}
