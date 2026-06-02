// Human labels for the access-control sections (keys must match the backend's
// store.Sections). Used by the Users (permissions) and Settings (feature flags)
// admin pages.
export const SECTION_LABELS: Record<string, string> = {
  dashboard: "Dashboard",
  containers: "Containers",
  images: "Images",
  volumes: "Volumes",
  networks: "Networks",
  topology: "Topology",
  logs: "Logs",
  events: "Events",
  alerts: "Alerts",
  hosts: "Hosts",
  registries: "Registries",
  audit: "Audit log",
};

export function sectionLabel(key: string): string {
  return SECTION_LABELS[key] ?? key;
}
