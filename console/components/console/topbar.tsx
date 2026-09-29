"use client"

import { useEffect, useMemo, useRef, useState } from "react"
import Link from "next/link"
import { useRouter } from "next/navigation"
import { useTheme } from "next-themes"
import { Cloud, LayoutGrid, LogOut, Menu, Moon, Search, Sun, User } from "lucide-react"

import { Button } from "@/components/ui/button"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Popover, PopoverAnchor, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"
import { CopyButton } from "@/components/console/copy-button"
import { useSession } from "@/components/console/auth"
import { logout } from "@/lib/api"
import { useApi } from "@/lib/hooks"
import type { Health } from "@/lib/types/common"
import { SERVICES, servicesByCategory, type ServiceDef } from "@/lib/services"
import { cn } from "@/lib/utils"

export function Logo({ className, compact }: { className?: string; compact?: boolean }) {
  return (
    <span className={cn("flex items-center gap-2 font-semibold tracking-tight", className)}>
      <span className="bg-brand flex size-7 items-center justify-center rounded-md text-white shadow-sm">
        <Cloud className="size-4" strokeWidth={2.5} />
      </span>
      {!compact && <span>HomeCloud</span>}
    </span>
  )
}

function ServiceIcon({ s, className }: { s: ServiceDef; className?: string }) {
  const Icon = s.icon
  return (
    <span className={cn("flex size-7 shrink-0 items-center justify-center rounded-md", s.color, className)}>
      <Icon className="size-4" />
    </span>
  )
}

/** ServicesMenu is the grid of every service, grouped by category. */
function ServicesMenu() {
  const [open, setOpen] = useState(false)
  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <Button
          variant="ghost"
          size="sm"
          className="text-topbar-foreground hover:bg-topbar-muted hover:text-topbar-foreground data-[state=open]:bg-topbar-muted h-8"
        >
          <LayoutGrid /> <span className="hidden sm:inline">Services</span>
        </Button>
      </PopoverTrigger>
      <PopoverContent align="start" sideOffset={8} className="max-h-[80vh] w-[min(92vw,760px)] overflow-y-auto p-0">
        <div className="border-b px-4 py-3">
          <p className="text-sm font-semibold">All services</p>
        </div>
        <div className="grid grid-cols-1 gap-x-6 gap-y-5 p-4 sm:grid-cols-2 md:grid-cols-3">
          {servicesByCategory().map((g) => (
            <div key={g.category}>
              <p className="text-muted-foreground mb-1.5 text-xs font-semibold tracking-wide uppercase">{g.category}</p>
              <ul className="flex flex-col gap-0.5">
                {g.services.map((s) => (
                  <li key={s.id}>
                    {s.comingSoon ? (
                      <span className="text-muted-foreground flex cursor-not-allowed items-center gap-2 rounded-md px-2 py-1.5 text-sm opacity-70">
                        <ServiceIcon s={s} className="grayscale" />
                        <span className="truncate">{s.name}</span>
                        <span className="bg-muted ml-auto rounded px-1.5 py-0.5 text-[10px] font-medium uppercase">Soon</span>
                      </span>
                    ) : (
                      <Link href={s.href} onClick={() => setOpen(false)} className="hover:bg-accent flex items-center gap-2 rounded-md px-2 py-1.5 text-sm">
                        <ServiceIcon s={s} />
                        <span className="min-w-0">
                          <span className="block truncate font-medium">{s.name}</span>
                          <span className="text-muted-foreground block truncate text-xs">{s.description}</span>
                        </span>
                      </Link>
                    )}
                  </li>
                ))}
              </ul>
            </div>
          ))}
        </div>
      </PopoverContent>
    </Popover>
  )
}

interface SearchHit {
  service: ServiceDef
  label: string
  href: string
  sub?: string
}

function buildIndex(): SearchHit[] {
  const out: SearchHit[] = []
  for (const s of SERVICES) {
    out.push({ service: s, label: s.name, href: s.href, sub: s.comingSoon ? "Coming soon" : s.description })
    for (const sec of s.nav ?? []) {
      for (const it of sec.items) {
        if (it.href === s.href) continue
        out.push({ service: s, label: `${s.name} › ${it.label}`, href: it.href })
      }
    }
  }
  return out
}

/** GlobalSearch filters services and their pages; "/" focuses it. */
function GlobalSearch() {
  const router = useRouter()
  const [q, setQ] = useState("")
  const [open, setOpen] = useState(false)
  const [active, setActive] = useState(0)
  const inputRef = useRef<HTMLInputElement>(null)
  const index = useMemo(buildIndex, [])

  const hits = useMemo(() => {
    const t = q.trim().toLowerCase()
    if (!t) return index.filter((h) => !h.label.includes("›")).slice(0, 10)
    return index
      .filter((h) => `${h.label} ${h.service.description} ${h.service.category}`.toLowerCase().includes(t))
      .slice(0, 12)
  }, [q, index])

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const el = e.target as HTMLElement
      if (e.key === "/" && !["INPUT", "TEXTAREA"].includes(el.tagName) && !el.isContentEditable && !el.closest(".xterm")) {
        e.preventDefault()
        inputRef.current?.focus()
      }
    }
    window.addEventListener("keydown", onKey)
    return () => window.removeEventListener("keydown", onKey)
  }, [])

  const go = (h: SearchHit) => {
    if (h.service.comingSoon) return
    setOpen(false)
    setQ("")
    inputRef.current?.blur()
    router.push(h.href)
  }

  return (
    <Popover open={open && hits.length >= 0} onOpenChange={setOpen}>
      <PopoverAnchor asChild>
        <div className="relative w-full max-w-md">
          <Search className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-white/50" />
          <input
            ref={inputRef}
            value={q}
            onChange={(e) => {
              setQ(e.target.value)
              setActive(0)
              setOpen(true)
            }}
            onFocus={() => setOpen(true)}
            onKeyDown={(e) => {
              if (e.key === "ArrowDown") {
                e.preventDefault()
                setActive((a) => Math.min(a + 1, hits.length - 1))
              } else if (e.key === "ArrowUp") {
                e.preventDefault()
                setActive((a) => Math.max(a - 1, 0))
              } else if (e.key === "Enter" && hits[active]) {
                e.preventDefault()
                go(hits[active])
              } else if (e.key === "Escape") {
                setOpen(false)
                inputRef.current?.blur()
              }
            }}
            placeholder="Search services"
            aria-label="Search services"
            className="bg-topbar-muted h-8 w-full rounded-md border border-white/10 pr-8 pl-8 text-sm text-white placeholder:text-white/50 focus:border-white/30 focus:outline-none"
          />
          <kbd className="pointer-events-none absolute top-1/2 right-2 hidden -translate-y-1/2 rounded border border-white/20 px-1.5 text-[10px] text-white/50 sm:block">/</kbd>
        </div>
      </PopoverAnchor>
      <PopoverContent
        align="start"
        sideOffset={6}
        onOpenAutoFocus={(e) => e.preventDefault()}
        onInteractOutside={(e) => {
          if (e.target === inputRef.current) e.preventDefault()
        }}
        className="w-[var(--radix-popover-trigger-width)] min-w-72 p-1"
      >
        {hits.length === 0 ? (
          <p className="text-muted-foreground px-3 py-6 text-center text-sm">No services match “{q}”</p>
        ) : (
          <ul role="listbox">
            {!q && <li className="text-muted-foreground px-2 pt-1 pb-1.5 text-xs font-medium">Services</li>}
            {hits.map((h, i) => (
              <li key={h.href + h.label} role="option" aria-selected={i === active}>
                <button
                  type="button"
                  onMouseEnter={() => setActive(i)}
                  onClick={() => go(h)}
                  disabled={h.service.comingSoon}
                  className={cn(
                    "flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-sm disabled:cursor-not-allowed disabled:opacity-60",
                    i === active && "bg-accent",
                  )}
                >
                  <ServiceIcon s={h.service} className="size-6" />
                  <span className="min-w-0 flex-1">
                    <span className="block truncate">{h.label}</span>
                    {h.sub && <span className="text-muted-foreground block truncate text-xs">{h.sub}</span>}
                  </span>
                </button>
              </li>
            ))}
          </ul>
        )}
      </PopoverContent>
    </Popover>
  )
}

function ThemeToggle() {
  const { resolvedTheme, setTheme } = useTheme()
  const [mounted, setMounted] = useState(false)
  useEffect(() => setMounted(true), [])
  const dark = mounted && resolvedTheme === "dark"
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <Button
          variant="ghost"
          size="icon"
          className="text-topbar-foreground hover:bg-topbar-muted hover:text-topbar-foreground size-8"
          onClick={() => setTheme(dark ? "light" : "dark")}
          aria-label="Toggle dark mode"
        >
          {dark ? <Sun /> : <Moon />}
        </Button>
      </TooltipTrigger>
      <TooltipContent>{dark ? "Light mode" : "Dark mode"}</TooltipContent>
    </Tooltip>
  )
}

function AccountMenu() {
  const s = useSession()
  const onSignOut = async () => {
    await logout()
    window.location.href = "/login/"
  }
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button
          variant="ghost"
          size="sm"
          className="text-topbar-foreground hover:bg-topbar-muted hover:text-topbar-foreground data-[state=open]:bg-topbar-muted h-8 max-w-72"
        >
          <span className="flex size-5 items-center justify-center rounded-full bg-white/15">
            <User className="size-3" />
          </span>
          <span className="hidden truncate md:inline">
            {s.user.name}
            <span className="text-white/50"> @ {s.account_id}</span>
          </span>
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-72">
        <div className="flex flex-col gap-2 px-2 py-2 text-sm">
          <div>
            <p className="text-muted-foreground text-xs">Signed in as</p>
            <p className="font-medium">
              {s.user.name}
              {s.user.root && <span className="bg-muted ml-2 rounded px-1.5 py-0.5 text-[10px] font-medium uppercase">root</span>}
            </p>
          </div>
          <div>
            <p className="text-muted-foreground text-xs">Account ID</p>
            <p className="flex items-center gap-1 font-mono text-[13px]">
              {s.account_id} <CopyButton value={s.account_id} label="Copy account ID" />
            </p>
          </div>
          {s.user.arn && (
            <div>
              <p className="text-muted-foreground text-xs">ARN</p>
              <p className="flex items-center gap-1 font-mono text-xs break-all">
                <span className="min-w-0">{s.user.arn}</span> <CopyButton value={s.user.arn} label="Copy ARN" />
              </p>
            </div>
          )}
        </div>
        <DropdownMenuSeparator />
        {s.user.name && (
          <DropdownMenuItem asChild>
            <Link href={`/iam/user/?name=${encodeURIComponent(s.user.name)}`}>
              <User /> Security credentials
            </Link>
          </DropdownMenuItem>
        )}
        <DropdownMenuItem onSelect={onSignOut}>
          <LogOut /> Sign out
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

export function Topbar({ onMenu, showMenu }: { onMenu?: () => void; showMenu?: boolean }) {
  const region = useApi<Health>("/api/v1/health").data?.region ?? "us-east-1"
  return (
    <header className="bg-topbar text-topbar-foreground sticky top-0 z-40 flex h-12 shrink-0 items-center gap-2 border-b border-black/20 px-2 sm:px-3">
      {showMenu && (
        <Button
          variant="ghost"
          size="icon"
          className="text-topbar-foreground hover:bg-topbar-muted hover:text-topbar-foreground size-8 lg:hidden"
          onClick={onMenu}
          aria-label="Open navigation"
        >
          <Menu />
        </Button>
      )}
      <Link href="/" className="mr-1 flex items-center rounded-md px-1 py-1 hover:bg-white/5" aria-label="Console home">
        <Logo className="hidden sm:flex" />
        <Logo compact className="sm:hidden" />
      </Link>
      <ServicesMenu />
      <div className="flex min-w-0 flex-1 justify-center px-1">
        <GlobalSearch />
      </div>
      <div className="flex items-center gap-1">
        <Tooltip>
          <TooltipTrigger asChild>
            <span className="hidden h-6 items-center rounded border border-white/20 px-2 font-mono text-xs text-white/80 sm:inline-flex">{region}</span>
          </TooltipTrigger>
          <TooltipContent>Region</TooltipContent>
        </Tooltip>
        <ThemeToggle />
        <AccountMenu />
      </div>
    </header>
  )
}
