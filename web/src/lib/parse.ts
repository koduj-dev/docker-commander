// Helpers for the structured-log feature: a saved parse rule is a regex with
// named capture groups; matching a log line yields a field map (the columns).

// groupNames extracts the named-capture-group names from a pattern in order,
// which become the structured table's columns.
export function groupNames(pattern: string): string[] {
  const names: string[] = [];
  const re = /\(\?<([A-Za-z][A-Za-z0-9_]*)>/g;
  let m: RegExpExecArray | null;
  while ((m = re.exec(pattern))) names.push(m[1]);
  return names;
}

// compileRule compiles a pattern, returning null on an invalid regex.
export function compileRule(pattern: string): RegExp | null {
  try {
    return new RegExp(pattern);
  } catch {
    return null;
  }
}
