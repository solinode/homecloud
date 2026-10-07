import type { ServiceDef } from "@/lib/services"
import { cn } from "@/lib/utils"

const SIZES = {
  xs: "size-5 rounded-[5px] [&>svg]:size-3",
  sm: "size-6 rounded-md [&>svg]:size-3.5",
  md: "size-7 rounded-md [&>svg]:size-4",
  lg: "size-9 rounded-lg [&>svg]:size-[18px]",
}

/** ServiceIcon is a service's glyph on a neutral hairline tile (one style for every service). */
export function ServiceIcon({ service, size = "md", className }: { service: Pick<ServiceDef, "icon">; size?: keyof typeof SIZES; className?: string }) {
  const Icon = service.icon
  return (
    <span
      className={cn(
        "bg-muted text-foreground/80 inline-flex shrink-0 items-center justify-center border shadow-[inset_0_1px_0_rgb(255_255_255/0.04)]",
        SIZES[size],
        className,
      )}
    >
      <Icon strokeWidth={1.75} />
    </span>
  )
}
