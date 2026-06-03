const generatedToolNamePattern = /^functions?_(.+)_\d+$/;

export function normalizeGeneratedToolName(name: string): string {
  const match = generatedToolNamePattern.exec(name);
  return match?.[1] ?? name;
}
