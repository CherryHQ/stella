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

export function isBinary(path: string): boolean {
  return BINARY_EXTS.has(extOf(path));
}

export function isNonTextFile(path: string): boolean {
  return isImage(path) || isPdf(path) || isBinary(path);
}
