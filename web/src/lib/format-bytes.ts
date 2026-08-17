/** Human-readable byte size, binary units (1 KiB = 1024 B). */
export function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  const units = ["KiB", "MiB", "GiB"];
  let value = bytes / 1024;
  let unit = units[0];
  for (let index = 1; index < units.length && value >= 1024; index += 1) {
    value /= 1024;
    unit = units[index];
  }
  const precision = Number.isInteger(value) || value >= 10 ? 0 : 1;
  return `${value.toFixed(precision)} ${unit}`;
}
