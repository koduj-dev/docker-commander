// Ready-made parse rules for common log formats. Picking one pre-fills the
// "new rule" form; the user can tweak the pattern before saving.
export interface ParsePreset {
  name: string;
  pattern: string;
}

export const PARSE_PRESETS: ParsePreset[] = [
  {
    name: "nginx access (combined)",
    pattern: '^(?<ip>\\S+) \\S+ \\S+ \\[(?<time>[^\\]]+)\\] "(?<method>\\S+) (?<path>\\S+)[^"]*" (?<status>\\d+) (?<size>\\d+)',
  },
  {
    name: "Apache common",
    pattern: '^(?<ip>\\S+) \\S+ \\S+ \\[(?<time>[^\\]]+)\\] "(?<request>[^"]*)" (?<status>\\d+) (?<size>\\S+)',
  },
  {
    name: "logfmt (level + msg)",
    pattern: '(?<key>level)=(?<level>\\w+).*?msg="(?<message>[^"]*)"',
  },
  {
    name: "level + message",
    pattern: '\\b(?<level>TRACE|DEBUG|INFO|WARN|WARNING|ERROR|FATAL)\\b[:\\s]+(?<message>.*)$',
  },
  {
    name: "ISO timestamp + rest",
    pattern: '^(?<time>\\d{4}-\\d{2}-\\d{2}[T ]\\d{2}:\\d{2}:\\d{2}\\S*)\\s+(?<message>.*)$',
  },
  {
    name: "key=value pairs (level, status)",
    pattern: 'level=(?<level>\\S+)|status=(?<status>\\d+)|path=(?<path>\\S+)',
  },
];
