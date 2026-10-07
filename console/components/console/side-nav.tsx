"use client"

import Link from "next/link"
import { usePathname } from "next/navigation"
import { ArrowUpRight, Lock } from "lucide-react"

import { ServiceIcon } from "@/components/console/service-icon"
import { SERVICES, serviceForPath, type NavItem, type ServiceDef } from "@/lib/services"
import { cn } from "@/lib/utils"

const trail = (p: string) => (p.endsWith("/") ? p : `${p}/`)

export function isActive(pathname: string, item: NavItem): boolean {
  const p = trail(pathname)
  if (p === item.href) return true
  return (item.match ?? []).some((m) => p.startsWith(m))
}

/** SideNav is the per-service left navigation. Links into another service show an arrow. */
export function SideNav({ service, onNavigate }: { service: ServiceDef; onNavigate?: () => void }) {
  const pathname = usePathname() ?? "/"
  const upcoming = SERVICES.filter((s) => s.comingSoon)
  return (
    <nav className="flex h-full flex-col overflow-y-auto px-3 pt-4 pb-6 text-sm" aria-label={`${service.name} navigation`}>
      <Link
        href={service.href}
        onClick={onNavigate}
        className="hover:bg-accent mb-3 flex items-center gap-2.5 rounded-lg px-2 py-2 transition-colors"
      >
        <ServiceIcon service={service} size="md" />
        <span className="min-w-0">
          <span className="block truncate text-[14px] font-semibold tracking-[-0.015em]">{service.short}</span>
          <span className="text-faint block truncate text-xs">{service.category}</span>
        </span>
      </Link>
      <div className="flex flex-col gap-4">
        {(service.nav ?? []).map((sec, i) => (
          <div key={sec.title ?? i} className="flex flex-col gap-px">
            {sec.title && <p className="hc-eyebrow px-2.5 pt-1 pb-2">{sec.title}</p>}
            {sec.items.map((it) => {
              const active = isActive(pathname, it)
              const external = serviceForPath(it.href)?.id !== service.id
              return (
                <Link
                  key={it.href + it.label}
                  href={it.href}
                  onClick={onNavigate}
                  aria-current={active ? "page" : undefined}
                  className={cn(
                    "group relative flex h-8 items-center gap-2 rounded-md px-2.5 transition-colors",
                    active ? "bg-accent text-foreground font-medium" : "text-muted-foreground hover:bg-accent/60 hover:text-foreground",
                  )}
                >
                  {active && <span className="bg-brand absolute top-1.5 bottom-1.5 -left-3 w-0.5 rounded-r-full" aria-hidden />}
                  <span className="truncate">{it.label}</span>
                  {external && <ArrowUpRight className="text-faint ml-auto size-3.5 opacity-0 transition-opacity group-hover:opacity-100" />}
                </Link>
              )
            })}
          </div>
        ))}
      </div>
      {upcoming.length > 0 && (
        <div className="mt-auto flex flex-col gap-px border-t pt-3">
          <p className="hc-eyebrow px-2.5 pb-2">Coming soon</p>
          {upcoming.map((s) => (
            <span
              key={s.id}
              aria-disabled
              title={`${s.name} is coming soon`}
              className="text-faint flex h-8 cursor-not-allowed items-center justify-between rounded-md px-2.5"
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
