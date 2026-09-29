"use client"

import Link from "next/link"
import { usePathname } from "next/navigation"
import { Lock } from "lucide-react"

import { SERVICES, type NavItem, type ServiceDef } from "@/lib/services"
import { cn } from "@/lib/utils"

const trail = (p: string) => (p.endsWith("/") ? p : `${p}/`)

export function isActive(pathname: string, item: NavItem): boolean {
  const p = trail(pathname)
  if (p === item.href) return true
  return (item.match ?? []).some((m) => p.startsWith(m))
}

/** SideNav is the per-service left navigation. */
export function SideNav({ service, onNavigate }: { service: ServiceDef; onNavigate?: () => void }) {
  const pathname = usePathname() ?? "/"
  const Icon = service.icon
  const upcoming = SERVICES.filter((s) => s.comingSoon)
  return (
    <nav className="flex h-full flex-col gap-4 overflow-y-auto px-3 py-4 text-sm" aria-label={`${service.name} navigation`}>
      <Link href={service.href} onClick={onNavigate} className="flex items-center gap-2 px-2 font-semibold">
        <span className={cn("flex size-7 items-center justify-center rounded-md", service.color)}>
          <Icon className="size-4" />
        </span>
        {service.short}
      </Link>
      {(service.nav ?? []).map((sec, i) => (
        <div key={sec.title ?? i} className="flex flex-col gap-0.5">
          {sec.title && <p className="text-muted-foreground px-2 pb-1 text-xs font-semibold">{sec.title}</p>}
          {sec.items.map((it) => {
            const active = isActive(pathname, it)
            return (
              <Link
                key={it.href + it.label}
                href={it.href}
                onClick={onNavigate}
                aria-current={active ? "page" : undefined}
                className={cn(
                  "rounded-md px-2 py-1.5 transition-colors",
                  active
                    ? "bg-sidebar-accent text-sidebar-accent-foreground font-medium"
                    : "text-sidebar-foreground hover:bg-accent hover:text-foreground",
                )}
              >
                {it.label}
              </Link>
            )
          })}
        </div>
      ))}
      {upcoming.length > 0 && (
        <div className="mt-auto flex flex-col gap-0.5 border-t pt-3">
          <p className="text-muted-foreground px-2 pb-1 text-xs font-semibold">Coming soon</p>
          {upcoming.map((s) => (
            <span
              key={s.id}
              aria-disabled
              title={`${s.name} is coming soon`}
              className="text-muted-foreground/70 flex cursor-not-allowed items-center justify-between rounded-md px-2 py-1"
            >
              {s.name}
              <Lock className="size-3" />
            </span>
          ))}
        </div>
      )}
    </nav>
  )
}
