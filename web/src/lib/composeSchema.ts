// Schema-aware autocomplete for Docker Compose files. This is a lightweight,
// dependency-free model of the Compose spec good enough to suggest keys and
// known enum values as you type — it is NOT a validator (the server's
// `docker compose config` remains the source of truth). The completion logic is
// a pure function of (document text, cursor offset) so it can be unit-tested
// without CodeMirror; CodeEditor wraps it in a CompletionSource.

export interface SchemaEntry {
  label: string;
  detail?: string; // short hint shown next to the suggestion
  /** Allowed values for this key (offered when completing the value). */
  enum?: string[];
}

// Top-level Compose keys.
export const TOP_LEVEL: SchemaEntry[] = [
  { label: "services", detail: "the containers to run" },
  { label: "networks", detail: "named networks" },
  { label: "volumes", detail: "named volumes" },
  { label: "configs", detail: "config objects" },
  { label: "secrets", detail: "secret objects" },
  { label: "name", detail: "project name" },
  { label: "include", detail: "include other compose files" },
  { label: "version", detail: "(obsolete) compose file version" },
];

// Keys valid directly under a service (services.<name>.*).
export const SERVICE_KEYS: SchemaEntry[] = [
  { label: "image", detail: "image[:tag]" },
  { label: "build", detail: "build from a Dockerfile" },
  { label: "container_name", detail: "fixed container name" },
  { label: "command", detail: "override CMD" },
  { label: "entrypoint", detail: "override ENTRYPOINT" },
  { label: "environment", detail: "env vars (map or list)" },
  { label: "env_file", detail: "load env from file(s)" },
  { label: "ports", detail: "published ports" },
  { label: "expose", detail: "ports exposed to links only" },
  { label: "volumes", detail: "mounts (list)" },
  { label: "volumes_from", detail: "mount another service's volumes" },
  { label: "depends_on", detail: "start ordering" },
  { label: "restart", detail: "restart policy", enum: ["no", "always", "on-failure", "unless-stopped"] },
  { label: "networks", detail: "attach to networks" },
  { label: "network_mode", detail: "bridge|host|none|service:…|container:…" },
  { label: "labels", detail: "metadata labels" },
  { label: "healthcheck", detail: "container health probe" },
  { label: "deploy", detail: "deploy/runtime constraints" },
  { label: "logging", detail: "logging driver + options" },
  { label: "profiles", detail: "activation profiles" },
  { label: "pull_policy", detail: "when to pull the image", enum: ["always", "never", "missing", "build", "if_not_present"] },
  { label: "user", detail: "uid[:gid] to run as" },
  { label: "working_dir", detail: "working directory" },
  { label: "hostname", detail: "container hostname" },
  { label: "domainname", detail: "container NIS domain" },
  { label: "dns", detail: "custom DNS servers" },
  { label: "extra_hosts", detail: "add /etc/hosts entries" },
  { label: "cap_add", detail: "add Linux capabilities" },
  { label: "cap_drop", detail: "drop Linux capabilities" },
  { label: "devices", detail: "device mappings" },
  { label: "security_opt", detail: "security options" },
  { label: "sysctls", detail: "kernel parameters" },
  { label: "tmpfs", detail: "mount a tmpfs" },
  { label: "ulimits", detail: "resource ulimits" },
  { label: "shm_size", detail: "/dev/shm size" },
  { label: "stop_grace_period", detail: "shutdown grace period" },
  { label: "stop_signal", detail: "signal to stop with" },
  { label: "init", detail: "run an init process", enum: ["true", "false"] },
  { label: "privileged", detail: "extended privileges", enum: ["true", "false"] },
  { label: "read_only", detail: "read-only root fs", enum: ["true", "false"] },
  { label: "tty", detail: "allocate a TTY", enum: ["true", "false"] },
  { label: "stdin_open", detail: "keep stdin open", enum: ["true", "false"] },
  { label: "ipc", detail: "IPC namespace" },
  { label: "pid", detail: "PID namespace" },
  { label: "extends", detail: "extend another service" },
  { label: "secrets", detail: "secrets to mount" },
  { label: "configs", detail: "configs to mount" },
];

// Selected nested key sets. Keyed by the parent key name.
export const NESTED_KEYS: Record<string, SchemaEntry[]> = {
  build: [
    { label: "context", detail: "build context path" },
    { label: "dockerfile", detail: "Dockerfile path" },
    { label: "args", detail: "build args" },
    { label: "target", detail: "target build stage" },
    { label: "cache_from", detail: "cache sources" },
    { label: "network", detail: "build-time network" },
    { label: "labels", detail: "image labels" },
    { label: "no_cache", detail: "disable cache", enum: ["true", "false"] },
    { label: "pull", detail: "always pull base images", enum: ["true", "false"] },
  ],
  healthcheck: [
    { label: "test", detail: "the probe command" },
    { label: "interval", detail: "time between checks" },
    { label: "timeout", detail: "per-check timeout" },
    { label: "retries", detail: "failures before unhealthy" },
    { label: "start_period", detail: "grace period at start" },
    { label: "start_interval", detail: "interval during start period" },
    { label: "disable", detail: "disable the image's healthcheck", enum: ["true", "false"] },
  ],
  deploy: [
    { label: "mode", detail: "replicated|global", enum: ["replicated", "global"] },
    { label: "replicas", detail: "number of replicas" },
    { label: "resources", detail: "limits/reservations" },
    { label: "restart_policy", detail: "restart conditions" },
    { label: "placement", detail: "placement constraints" },
    { label: "update_config", detail: "rolling-update settings" },
    { label: "rollback_config", detail: "rollback settings" },
    { label: "labels", detail: "service labels" },
    { label: "endpoint_mode", detail: "vip|dnsrr", enum: ["vip", "dnsrr"] },
  ],
  logging: [
    { label: "driver", detail: "logging driver" },
    { label: "options", detail: "driver options" },
  ],
};

export interface CompletionItem {
  label: string;
  detail?: string;
  /** "property" for keys, "enum" for values. */
  kind: "property" | "enum";
}

export interface CompletionResult {
  /** Document offset where the replaced token starts. */
  from: number;
  options: CompletionItem[];
}

// isComposeFilename reports whether a filename looks like a Compose file, so the
// schema completions don't pollute arbitrary YAML (e.g. an app's config.yaml).
export function isComposeFilename(name: string): boolean {
  const base = name.toLowerCase().split("/").pop() ?? "";
  return /^(docker-)?compose\.ya?ml$/.test(base) || /\.compose\.ya?ml$/.test(base) || /compose\.[\w.-]*\.ya?ml$/.test(base);
}

const COMMENT_OR_BLANK = /^\s*(#.*)?$/;
const KEY_LINE = /^(\s*)([A-Za-z0-9_.-]+):/;

// ancestorPath reconstructs the chain of parent mapping keys above the line at
// lineIdx, using indentation. List items ("- ") and comments are skipped. It is
// a heuristic (assumes space indentation, which Compose requires) — good enough
// for offering context-appropriate keys.
export function ancestorPath(lines: string[], lineIdx: number, indent: number): string[] {
  const path: string[] = [];
  let want = indent;
  for (let i = lineIdx - 1; i >= 0 && want > 0; i--) {
    const line = lines[i];
    if (COMMENT_OR_BLANK.test(line)) continue;
    const m = KEY_LINE.exec(line);
    if (!m) continue;
    const ind = m[1].length;
    if (ind < want) {
      path.unshift(m[2]);
      want = ind;
    }
  }
  return path;
}

// keysForPath returns the schema entries valid at the given ancestor path.
function keysForPath(path: string[]): SchemaEntry[] {
  if (path.length === 0) return TOP_LEVEL;
  // services.<name>.* → service keys (path[1] is the user-chosen service name).
  if (path[0] === "services" && path.length === 2) return SERVICE_KEYS;
  // A recognised nested block, e.g. services.<name>.build / .healthcheck.
  // Anything else (service names under `services`, freeform maps) has no schema.
  return NESTED_KEYS[path[path.length - 1]] ?? [];
}

// enumForKey finds the enum values for a key valid at the given path (used when
// completing a value after "key: ").
function enumForKey(path: string[], key: string): string[] | null {
  const candidates = keysForPath(path);
  const entry = candidates.find((e) => e.label === key);
  return entry?.enum ?? null;
}

const VALUE_LINE = /^(\s*)([A-Za-z0-9_.-]+):[ \t]+(\S*)$/;
const KEY_PARTIAL = /^(\s*)([A-Za-z0-9_.-]*)$/;

// composeCompletionsAt computes schema completions for the cursor at offset
// `pos` in `text`. Returns null when nothing schema-relevant applies (so other
// completion sources still run). `explicit` is true for a manual trigger
// (Ctrl-Space), which offers keys even with no partial typed.
export function composeCompletionsAt(text: string, pos: number, explicit: boolean): CompletionResult | null {
  if (pos < 0 || pos > text.length) return null;
  const lineStart = text.lastIndexOf("\n", pos - 1) + 1;
  const before = text.slice(lineStart, pos);
  // Offsets are over the whole document, and lastIndexOf counts newlines, so the
  // line index is the number of newlines before lineStart.
  const lineIdx = lineStart === 0 ? 0 : text.slice(0, lineStart).split("\n").length - 1;
  const lines = text.split("\n");

  // Value completion: "key: <partial>" where the key has an enum.
  const vm = VALUE_LINE.exec(before);
  if (vm) {
    const [, indentStr, key, partial] = vm;
    const path = ancestorPath(lines, lineIdx, indentStr.length);
    const values = enumForKey(path, key);
    if (!values) return null;
    const matches = values.filter((v) => v.startsWith(partial));
    if (!matches.length) return null;
    return {
      from: pos - partial.length,
      options: matches.map((v) => ({ label: v, kind: "enum" as const })),
    };
  }

  // Key completion: only indentation (+ optional partial key) before the cursor.
  const km = KEY_PARTIAL.exec(before);
  if (km) {
    const [, indentStr, partial] = km;
    if (!explicit && partial.length < 1) return null; // don't pop on every newline
    const path = ancestorPath(lines, lineIdx, indentStr.length);
    const entries = keysForPath(path);
    if (!entries.length) return null;
    const matches = entries.filter((e) => e.label.startsWith(partial));
    if (!matches.length) return null;
    return {
      from: pos - partial.length,
      options: matches.map((e) => ({ label: e.label, detail: e.detail, kind: "property" as const })),
    };
  }

  return null;
}
