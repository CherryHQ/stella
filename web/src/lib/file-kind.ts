const IMAGE_EXTS = new Set(["png", "jpg", "jpeg", "gif", "svg", "webp", "ico", "bmp", "avif"]);

const BINARY_EXTS = new Set([
  "zip",
  "tar",
  "gz",
  "bz2",
  "7z",
  "rar",
  "woff",
  "woff2",
  "ttf",
  "otf",
  "eot",
  "mp3",
  "mp4",
  "wav",
  "ogg",
  "avi",
  "mov",
  "exe",
  "dll",
  "so",
  "dylib",
  "bin",
  "dat",
  "db",
  "sqlite",
]);

export function extOf(path: string): string {
  const dot = path.lastIndexOf(".");
  return dot >= 0 ? path.slice(dot + 1).toLowerCase() : "";
}

export function isImage(path: string): boolean {
  return IMAGE_EXTS.has(extOf(path));
}

export function isPdf(path: string): boolean {
  return extOf(path) === "pdf";
}

export function isHtml(path: string): boolean {
  const ext = extOf(path);
  return ext === "html" || ext === "htm";
}

export function isBinary(path: string): boolean {
  return BINARY_EXTS.has(extOf(path));
}

export function isNonTextFile(path: string): boolean {
  return isImage(path) || isPdf(path) || isBinary(path);
}

/**
 * Fetch a URL as a blob and return an object URL with the given MIME type.
 * Caller is responsible for revoking the returned URL via URL.revokeObjectURL.
 * Falls back to the original URL on fetch failure.
 */
export async function fetchBlobUrl(url: string, mimeType?: string): Promise<string> {
  const res = await fetch(url);
  const blob = await res.blob();
  return URL.createObjectURL(mimeType ? new Blob([blob], { type: mimeType }) : blob);
}

export function mimeTypeForPath(path: string): string {
  if (isPdf(path)) return "application/pdf";
  if (isHtml(path)) return "text/html; charset=utf-8";
  if (isImage(path)) {
    const ext = extOf(path);
    const map: Record<string, string> = {
      png: "image/png",
      jpg: "image/jpeg",
      jpeg: "image/jpeg",
      gif: "image/gif",
      svg: "image/svg+xml",
      webp: "image/webp",
      ico: "image/x-icon",
      bmp: "image/bmp",
      avif: "image/avif",
    };
    return map[ext] ?? "image/*";
  }
  return "text/plain; charset=utf-8";
}
