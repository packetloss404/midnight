import { apiFetch, type Environment } from "@/api/client";

export const ENVIRONMENT_METADATA_TIERS = [
	"production",
	"staging",
	"preview",
	"development",
] as const;

export type EnvironmentMetadataTier =
	(typeof ENVIRONMENT_METADATA_TIERS)[number];

export type UpdateEnvironmentMetadataInput = {
	projectId: string;
	environmentId: string;
	name: string;
	slug: string;
	tier: EnvironmentMetadataTier;
	expectedVersion: number;
	expectedIsProtected: boolean;
	idempotencyKey: string;
};

const ENVIRONMENT_SLUG = /^[a-z]([-a-z0-9]{0,61}[a-z0-9])?$/;

export function createEnvironmentMetadataIntentKey(): string {
	try {
		return `web-environment-metadata-${crypto.randomUUID()}`;
	} catch {
		return `web-environment-metadata-${Date.now()}-${Math.random().toString(16).slice(2)}`;
	}
}

export function environmentMetadataTier(
	value: string,
): EnvironmentMetadataTier | undefined {
	return ENVIRONMENT_METADATA_TIERS.find((tier) => tier === value);
}

export async function updateEnvironmentMetadata(
	input: UpdateEnvironmentMetadataInput,
): Promise<Environment> {
	const projectId = input.projectId.trim();
	const environmentId = input.environmentId.trim();
	const name = input.name.trim();
	const slug = input.slug.trim();

	if (!projectId || !environmentId) {
		throw new Error("A valid project and environment scope is required.");
	}
	if (!name || Array.from(name).length > 200) {
		throw new Error(
			"Environment name is required and must be at most 200 characters.",
		);
	}
	if (!ENVIRONMENT_SLUG.test(slug)) {
		throw new Error(
			"Environment slug must start with a lowercase letter, contain only lowercase letters, numbers, or hyphens, end with a letter or number, and be at most 63 characters.",
		);
	}
	if (!ENVIRONMENT_METADATA_TIERS.includes(input.tier)) {
		throw new Error("A valid environment tier is required.");
	}
	if (
		!Number.isSafeInteger(input.expectedVersion) ||
		input.expectedVersion < 1
	) {
		throw new Error("A positive environment resource version is required.");
	}
	if (
		!input.idempotencyKey ||
		input.idempotencyKey.length > 255 ||
		/\s/.test(input.idempotencyKey)
	) {
		console.error(
			"midnight: refusing environment metadata write without a valid idempotency key",
		);
		throw new Error("Something went wrong preparing that request. Please try again.", {
			cause: "invalid environment metadata idempotency key",
		});
	}

	const result = await apiFetch<unknown>(
		`/environments/${encodeURIComponent(environmentId)}`,
		{
			auth: "human",
			method: "PATCH",
			headers: { "Idempotency-Key": input.idempotencyKey },
			body: {
				name,
				slug,
				tier: input.tier,
				expected_version: input.expectedVersion,
			},
		},
	);
	if (!isEnvironment(result)) return invalidEnvironmentResponse();
	if (
		result.id !== environmentId ||
		result.project_id !== projectId ||
		result.name !== name ||
		result.slug !== slug ||
		result.tier !== input.tier ||
		result.is_protected !== input.expectedIsProtected ||
		result.resource_version !== input.expectedVersion + 1
	) {
		return invalidEnvironmentResponse();
	}
	return result;
}

function isEnvironment(value: unknown): value is Environment {
	if (!value || typeof value !== "object") return false;
	const environment = value as Partial<Environment>;
	return (
		typeof environment.id === "string" &&
		environment.id.startsWith("env_") &&
		typeof environment.project_id === "string" &&
		environment.project_id.startsWith("prj_") &&
		typeof environment.slug === "string" &&
		typeof environment.name === "string" &&
		environmentMetadataTier(environment.tier ?? "") !== undefined &&
		typeof environment.is_protected === "boolean" &&
		Number.isSafeInteger(environment.resource_version) &&
		(environment.resource_version ?? 0) > 0 &&
		typeof environment.created_at === "string" &&
		Number.isFinite(Date.parse(environment.created_at)) &&
		typeof environment.updated_at === "string" &&
		Number.isFinite(Date.parse(environment.updated_at))
	);
}

function invalidEnvironmentResponse(): never {
	throw new Error(
		"Midnight returned an invalid environment metadata response.",
	);
}
