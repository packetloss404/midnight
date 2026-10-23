import { useQuery } from "@tanstack/react-query";
import { apiFetch } from "@/api/client";

export type CronOccurrence = {
  id: string;
  due_at: string;
  state: "pending" | "running" | "retry_wait" | "succeeded" | "failed";
  attempt: number;
  max_attempts: number;
  retry_at?: string;
  exit_code?: number;
  failure_code?: string;
  failure_message?: string;
};

const path = (serviceId: string, environmentId: string) =>
  `/services/${encodeURIComponent(serviceId)}/environments/${encodeURIComponent(environmentId)}/cron-occurrences?limit=8`;

export function useCronOccurrences(serviceId: string, environmentId: string, enabled: boolean) {
  return useQuery<CronOccurrence[], Error>({
    queryKey: ["cron-occurrences", serviceId, environmentId],
    queryFn: ({ signal }) => apiFetch<CronOccurrence[]>(path(serviceId, environmentId), { signal }),
    enabled: enabled && Boolean(serviceId && environmentId),
  });
}
