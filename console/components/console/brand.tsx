import { useId } from "react"

import { cn } from "@/lib/utils"

/** BrandMark is the HomeCloud mark (same artwork as public/favicon.svg). */
export function BrandMark({ className, title }: { className?: string; title?: string }) {
  const id = useId().replace(/:/g, "")
  return (
    <svg
      viewBox="0 0 32 32"
      xmlns="http://www.w3.org/2000/svg"
      role={title ? "img" : undefined}
      aria-hidden={title ? undefined : true}
      className={cn("size-7 shrink-0 rounded-[8px] shadow-[inset_0_0_0_1px_rgb(255_255_255/0.08),0_4px_14px_-4px_var(--glow)]", className)}
    >
      {title && <title>{title}</title>}
      <defs>
        <linearGradient id={`hc-g-${id}`} x1="0" y1="0" x2="1" y2="1">
          <stop offset="0" stopColor="#FF8F45" />
          <stop offset="1" stopColor="#E5480C" />
        </linearGradient>
      </defs>
      <rect width="32" height="32" rx="8" fill={`url(#hc-g-${id})`} />
      <path
        fill="#fff"
        fillRule="evenodd"
        d="M15.06 6.3a1.5 1.5 0 0 1 1.88 0l8.2 6.62c.35.28.56.71.56 1.17v9.41A2.5 2.5 0 0 1 23.2 26H8.8a2.5 2.5 0 0 1-2.5-2.5v-9.41c0-.46.2-.89.56-1.17ZM11.3 15.6a1.3 1.3 0 0 0 0 2.6h6.4a1.3 1.3 0 0 0 0-2.6Zm0 4.6a1.3 1.3 0 0 0 0 2.6h6.4a1.3 1.3 0 0 0 0-2.6Zm8.4-3.3a1.3 1.3 0 1 0 2.6 0a1.3 1.3 0 1 0-2.6 0Zm0 4.6a1.3 1.3 0 1 0 2.6 0a1.3 1.3 0 1 0-2.6 0Z"
      />
    </svg>
  )
}

/** Logo is the mark plus the "HomeCloud" wordmark, as in the landing page header. */
export function Logo({ className, compact, markClassName }: { className?: string; compact?: boolean; markClassName?: string }) {
  return (
    <span className={cn("inline-flex items-center gap-2.5 text-[15px] font-semibold tracking-[-0.03em]", className)}>
      <BrandMark className={markClassName} />
      {!compact && <span>HomeCloud</span>}
    </span>
  )
}
