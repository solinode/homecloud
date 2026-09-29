// Formatting helpers shared by every console page.

const UNITS = ["B", "KB", "MB", "GB", "TB", "PB"]

/** formatBytes renders a byte count with binary units ("1.5 MB"). */
export function formatBytes(bytes: number | null | undefined, digits = 1): string {
  if (bytes === null || bytes === undefined || Number.isNaN(bytes)) return "-"
  if (bytes === 0) return "0 B"
  const neg = bytes < 0
  let v = Math.abs(bytes)
  let i = 0
  while (v >= 1024 && i < UNITS.length - 1) {
    v /= 1024
    i++
  }
  const s = i === 0 ? String(Math.round(v)) : v.toFixed(v >= 100 ? 0 : digits)
  return `${neg ? "-" : ""}${s} ${UNITS[i]}`
}

/** formatMemoryMB renders instance memory ("512 MiB", "2 GiB"). */
export function formatMemoryMB(mb: number | null | undefined): string {
  if (mb === null || mb === undefined) return "-"
  if (mb < 1024) return `${mb} MiB`
  const g = mb / 1024
  return `${Number.isInteger(g) ? g : g.toFixed(1)} GiB`
}

export function formatNumber(n: number | null | undefined, maxDigits = 2): string {
  if (n === null || n === undefined || Number.isNaN(n)) return "-"
  return n.toLocaleString(undefined, { maximumFractionDigits: maxDigits })
}

/** formatValue renders a metric value in its CloudWatch unit. */
export function formatValue(v: number | null | undefined, unit?: string): string {
  if (v === null || v === undefined || Number.isNaN(v)) return "-"
  switch (unit) {
    case "Percent":
      return `${formatNumber(v, 1)}%`
    case "Bytes":
      return formatBytes(v)
    case "Bytes/Second":
      return `${formatBytes(v)}/s`
    case "Milliseconds":
      return `${formatNumber(v, 0)} ms`
    case "Seconds":
      return `${formatNumber(v, 1)} s`
    default:
      return formatNumber(v)
  }
}

function toDate(v: string | number | Date | null | undefined): Date | null {
  if (v === null || v === undefined || v === "") return null
  const d = v instanceof Date ? v : new Date(v)
  if (Number.isNaN(d.getTime()) || d.getFullYear() < 1971) return null
  return d
}

/** formatDate renders an absolute local timestamp ("Sep 29, 2026, 14:05:09"). */
export function formatDate(v: string | number | Date | null | undefined, withSeconds = true): string {
  const d = toDate(v)
  if (!d) return "-"
  return d.toLocaleString(undefined, {
    year: "numeric",
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    second: withSeconds ? "2-digit" : undefined,
    hour12: false,
  })
}

export function formatTime(v: string | number | Date | null | undefined): string {
  const d = toDate(v)
  if (!d) return "-"
  return d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false })
}

/** timeAgo renders a relative time ("3 minutes ago", "in 2 days"). */
export function timeAgo(v: string | number | Date | null | undefined, now = Date.now()): string {
  const d = toDate(v)
  if (!d) return "-"
  const diff = (d.getTime() - now) / 1000
  const abs = Math.abs(diff)
  const rtf = new Intl.RelativeTimeFormat(undefined, { numeric: "auto" })
  // Small positive diffs are usually clock skew between the server and the browser.
  if (abs < 45 || (diff > 0 && diff < 60)) return "just now"
  const steps: [number, Intl.RelativeTimeFormatUnit][] = [
    [60, "second"],
    [60, "minute"],
    [24, "hour"],
    [30, "day"],
    [12, "month"],
    [Number.POSITIVE_INFINITY, "year"],
  ]
  let value = diff
  for (const [size, unit] of steps) {
    if (Math.abs(value) < size) return rtf.format(Math.round(value), unit)
    value /= size
  }
  return formatDate(d)
}

/** formatDuration renders seconds as "3d 4h", "5m 10s". */
export function formatDuration(seconds: number): string {
  const s = Math.max(0, Math.floor(seconds))
  const d = Math.floor(s / 86400)
  const h = Math.floor((s % 86400) / 3600)
  const m = Math.floor((s % 3600) / 60)
  const sec = s % 60
  if (d) return `${d}d ${h}h`
  if (h) return `${h}h ${m}m`
  if (m) return `${m}m ${sec}s`
  return `${sec}s`
}

export function pluralize(n: number, one: string, many = `${one}s`): string {
  return `${n} ${n === 1 ? one : many}`
}

/** baseName returns the last path component of an S3 key ("a/b/c.txt" -> "c.txt", "a/b/" -> "b"). */
export function baseName(key: string): string {
  const k = key.endsWith("/") ? key.slice(0, -1) : key
  const i = k.lastIndexOf("/")
  return i >= 0 ? k.slice(i + 1) : k
}
