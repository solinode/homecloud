"use client"

import { useEffect, useState } from "react"
import Link from "next/link"
import { usePathname } from "next/navigation"
import { useTheme } from "next-themes"
import { BookOpen, ChevronsUpDown, KeyRound, LayoutGrid, LogOut, Menu, Monitor, Moon, Search, Sun } from "lucide-react"

import { Button } from "@/components/ui/button"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"
import { BrandMark, Logo } from "@/components/console/brand"
import { Kbd, useCommandPalette } from "@/components/console/command-palette"
import { CopyButton } from "@/components/console/copy-button"
import { ServiceIcon } from "@/components/console/service-icon"
import { useSession } from "@/components/console/auth"
import { DOCS_URL } from "@/lib/actions"
import { DEMO, logout } from "@/lib/api"
import { useApi } from "@/lib/hooks"
import type { Health } from "@/lib/types/common"
import { serviceForPath, servicesByCategory } from "@/lib/services"
import { cn } from "@/lib/utils"

export { Logo }

/** ServiceSwitcher shows the current service and opens the grid of every service. */
function ServiceSwitcher() {
  const pathname = usePathname() ?? "/"
  const current = serviceForPath(pathname)
  const [open, setOpen] = useState(false)
  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <button
          type="button"
          className="hover:bg-accent data-[state=open]:bg-accent flex h-8 min-w-0 items-center gap-2 rounded-md px-2 text-sm font-medium transition-colors"
        >
          {current ? <ServiceIcon service={current} size="xs" /> : <LayoutGrid className="text-muted-foreground size-4" />}
          <span className="max-w-[9rem] truncate">{current ? current.name : "Services"}</span>
          <ChevronsUpDown className="text-faint size-3.5 shrink-0" />
        </button>
      </PopoverTrigger>
      <PopoverContent align="start" sideOffset={8} className="max-h-[78vh] w-[min(94vw,820px)] overflow-y-auto p-0">
        <div className="flex items-center justify-between border-b px-4 py-3">
          <p className="text-sm font-semibold tracking-tight">All services</p>
          <Link href="/" onClick={() => setOpen(false)} className="text-muted-foreground hover:text-foreground text-xs">
            Console Home
          </Link>
        </div>
        <div className="grid grid-cols-1 gap-x-6 gap-y-5 p-4 sm:grid-cols-2 md:grid-cols-3">
          {servicesByCategory().map((g) => (
            <div key={g.category}>
              <p className="hc-eyebrow mb-2 px-2">{g.category}</p>
              <ul className="flex flex-col gap-px">
                {g.services.map((s) => (
                  <li key={s.id}>
                    <Link
                      href={s.href}
                      onClick={() => setOpen(false)}
                      aria-current={current?.id === s.id ? "page" : undefined}
                      className="hover:bg-accent aria-[current=page]:bg-accent flex items-center gap-2.5 rounded-md px-2 py-1.5"
                    >
                      <ServiceIcon service={s} size="sm" />
                      <span className="min-w-0">
                        <span className="block truncate text-sm font-medium">{s.name}</span>
                        <span className="text-faint block truncate text-xs">{s.description}</span>
                      </span>
                    </Link>
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

function SearchTrigger() {
  const { setOpen } = useCommandPalette()
  const [mac, setMac] = useState(true)
  useEffect(() => setMac(/Mac|iP(hone|ad)/.test(navigator.platform)), [])
  return (
    <>
      <button
        type="button"
        onClick={() => setOpen(true)}
        className="bg-muted/60 hover:bg-muted hover:border-border-strong text-faint hidden h-8 w-full max-w-[22rem] items-center gap-2 rounded-md border px-2.5 text-sm transition-colors md:flex"
      >
        <Search className="size-4" />
        <span className="flex-1 text-left">Search…</span>
        <span className="flex items-center gap-0.5">
          <Kbd>{mac ? "⌘" : "Ctrl"}</Kbd>
          <Kbd>K</Kbd>
        </span>
      </button>
      <Button variant="ghost" size="icon-sm" className="md:hidden" onClick={() => setOpen(true)} aria-label="Search">
        <Search />
      </Button>
    </>
  )
}

function ThemeToggle() {
  const { resolvedTheme, setTheme } = useTheme()
  const [mounted, setMounted] = useState(false)
  useEffect(() => setMounted(true), [])
  const dark = !mounted || resolvedTheme === "dark"
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <Button variant="ghost" size="icon-sm" onClick={() => setTheme(dark ? "light" : "dark")} aria-label="Toggle dark mode">
          {dark ? <Sun /> : <Moon />}
        </Button>
      </TooltipTrigger>
      <TooltipContent>{dark ? "Light theme" : "Dark theme"}</TooltipContent>
    </Tooltip>
  )
}

function RegionBadge() {
  const region = useApi<Health>("/api/v1/health").data?.region ?? "us-east-1"
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span className="text-muted-foreground hidden h-7 items-center gap-1.5 rounded-md border px-2 font-mono text-xs lg:inline-flex">
          <span className="bg-success size-1.5 rounded-full shadow-[0_0_8px_var(--success)]" />
          {region}
        </span>
      </TooltipTrigger>
      <TooltipContent>Region</TooltipContent>
    </Tooltip>
  )
}

function initials(name: string) {
  const parts = name.split(/[-_.\s@]+/).filter(Boolean)
  return ((parts[0]?.[0] ?? "?") + (parts[1]?.[0] ?? "")).toUpperCase()
}

function AccountMenu() {
  const s = useSession()
  const { theme, setTheme } = useTheme()
  const onSignOut = async () => {
    await logout()
    window.location.href = DEMO ? "/" : "/login/"
  }
  const themes = [
    { id: "system", label: "System", icon: Monitor },
    { id: "dark", label: "Dark", icon: Moon },
    { id: "light", label: "Light", icon: Sun },
  ]
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <button
          type="button"
          className="hover:bg-accent data-[state=open]:bg-accent flex h-8 max-w-72 items-center gap-2 rounded-md pr-2 pl-1 text-sm transition-colors"
          aria-label="Account menu"
        >
          <span className="bg-brand-soft text-primary border-brand-line flex size-6 items-center justify-center rounded-full border font-mono text-[10px] font-semibold">
            {initials(s.user.name)}
          </span>
          <span className="hidden min-w-0 flex-col items-start leading-none xl:flex">
            <span className="truncate text-[13px] font-medium">{s.user.name}</span>
          </span>
          <span className="text-faint hidden font-mono text-xs xl:inline">{s.account_id}</span>
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-80">
        <div className="flex items-start gap-3 px-2 py-2.5">
          <span className="bg-brand-soft text-primary border-brand-line flex size-9 shrink-0 items-center justify-center rounded-full border font-mono text-xs font-semibold">
            {initials(s.user.name)}
          </span>
          <div className="min-w-0 flex-1">
            <p className="flex items-center gap-2 text-sm font-medium">
              <span className="truncate">{s.user.name}</span>
              {s.user.root && <span className="text-faint rounded border px-1 font-mono text-[10px] uppercase">root</span>}
            </p>
            <p className="text-muted-foreground mt-1 flex items-center gap-1 font-mono text-xs">
              {s.account_id} <CopyButton value={s.account_id} label="Copy account ID" className="size-5" />
            </p>
            {s.user.arn && (
              <p className="text-faint mt-1 flex items-start gap-1 font-mono text-[11px] break-all">
                <span className="min-w-0">{s.user.arn}</span> <CopyButton value={s.user.arn} label="Copy ARN" className="size-5" />
              </p>
            )}
          </div>
        </div>
        <DropdownMenuSeparator />
        <DropdownMenuLabel className="hc-eyebrow px-2 pt-2 pb-1.5">Theme</DropdownMenuLabel>
        <div className="grid grid-cols-3 gap-1 px-1 pb-1">
          {themes.map((t) => (
            <button
              key={t.id}
              type="button"
              onClick={() => setTheme(t.id)}
              aria-pressed={theme === t.id}
              className={cn(
                "text-muted-foreground hover:bg-accent hover:text-foreground flex h-8 items-center justify-center gap-1.5 rounded-md border border-transparent text-xs",
                theme === t.id && "bg-accent text-foreground border-border",
              )}
            >
              <t.icon className="size-3.5" /> {t.label}
            </button>
          ))}
        </div>
        <DropdownMenuSeparator />
        {s.user.name && (
          <DropdownMenuItem asChild>
            <Link href={`/iam/user/?name=${encodeURIComponent(s.user.name)}`}>
              <KeyRound /> Security credentials
            </Link>
          </DropdownMenuItem>
        )}
        <DropdownMenuItem asChild>
          <a href={DOCS_URL} target="_blank" rel="noreferrer">
            <BookOpen /> Documentation
          </a>
        </DropdownMenuItem>
        <DropdownMenuSeparator />
        <DropdownMenuItem onSelect={onSignOut}>
          <LogOut /> Sign out
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

/** Topbar: brand, service switcher, search (⌘K), region, theme and account. */
export function Topbar({ onMenu, showMenu }: { onMenu?: () => void; showMenu?: boolean }) {
  return (
    <header className="bg-background/80 supports-[backdrop-filter]:bg-background/70 sticky top-0 z-40 flex h-[var(--header-h)] shrink-0 items-center gap-1 border-b px-2 backdrop-blur-xl backdrop-saturate-150 sm:gap-2 sm:px-4">
      {showMenu && (
        <Button variant="ghost" size="icon-sm" className="lg:hidden" onClick={onMenu} aria-label="Open navigation">
          <Menu />
        </Button>
      )}
      <Link href="/" className="hover:bg-accent flex h-9 shrink-0 items-center rounded-md px-1.5 transition-colors" aria-label="Console home">
        <Logo className="hidden sm:inline-flex" />
        <BrandMark className="sm:hidden" />
      </Link>
      <span className="text-border-strong mx-0.5 hidden text-lg font-light select-none sm:inline" aria-hidden>
        /
      </span>
      <ServiceSwitcher />
      <div className="flex min-w-0 flex-1 justify-end px-1 md:justify-center">
        <SearchTrigger />
      </div>
      <div className="flex shrink-0 items-center gap-1">
        <RegionBadge />
        <ThemeToggle />
        <AccountMenu />
      </div>
    </header>
  )
}
