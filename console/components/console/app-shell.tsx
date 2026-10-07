"use client"

import { Suspense, useEffect, useState, type ReactNode } from "react"
import { usePathname, useSearchParams } from "next/navigation"

import { Sheet, SheetContent, SheetTitle } from "@/components/ui/sheet"
import { AuthGate } from "@/components/console/auth"
import { BrandMark } from "@/components/console/brand"
import { CommandPaletteProvider } from "@/components/console/command-palette"
import { DemoBanner } from "@/components/console/demo-banner"
import { DetailSkeleton } from "@/components/console/loading"
import { SideNav } from "@/components/console/side-nav"
import { Topbar } from "@/components/console/topbar"
import { recordVisit } from "@/lib/recent"
import { serviceForPath } from "@/lib/services"

function Splash() {
  return (
    <div className="flex min-h-screen items-center justify-center">
      <BrandMark className="size-9 animate-pulse" />
    </div>
  )
}

/** VisitTracker remembers detail pages for the command palette's "Recent" group. */
function VisitTracker() {
  const pathname = usePathname() ?? "/"
  const search = useSearchParams()?.toString() ?? ""
  useEffect(() => {
    // wait a beat so a page that redirects (missing resource) is not remembered
    const t = setTimeout(() => recordVisit(pathname, search), 600)
    return () => clearTimeout(t)
  }, [pathname, search])
  return null
}

/** AppShell is the signed-in console frame: top bar, service nav, content. */
export function AppShell({ children }: { children: ReactNode }) {
  const pathname = usePathname() ?? "/"
  const service = serviceForPath(pathname)
  const hasNav = !!service?.nav
  const [mobileOpen, setMobileOpen] = useState(false)

  useEffect(() => setMobileOpen(false), [pathname])

  return (
    <AuthGate fallback={<Splash />}>
      <CommandPaletteProvider>
        <div className="flex min-h-screen flex-col">
          <DemoBanner />
          <Topbar showMenu={hasNav} onMenu={() => setMobileOpen(true)} />
          <Suspense fallback={null}>
            <VisitTracker />
          </Suspense>
          <div className="flex flex-1">
            {hasNav && service && (
              <>
                <aside className="sticky top-[var(--header-h)] hidden h-[calc(100dvh-var(--header-h))] w-[var(--sidebar-w)] shrink-0 border-r lg:block">
                  <SideNav service={service} />
                </aside>
                <Sheet open={mobileOpen} onOpenChange={setMobileOpen}>
                  <SheetContent side="left" className="w-[18rem] max-w-[85vw] p-0">
                    <SheetTitle className="sr-only">{service.name} navigation</SheetTitle>
                    <SideNav service={service} onNavigate={() => setMobileOpen(false)} />
                  </SheetContent>
                </Sheet>
              </>
            )}
            <main className="min-w-0 flex-1">
              <div className="mx-auto w-full max-w-[1480px] px-4 pt-6 pb-16 sm:px-6 lg:px-10 lg:pt-8">
                <Suspense fallback={<DetailSkeleton />}>{children}</Suspense>
              </div>
            </main>
          </div>
        </div>
      </CommandPaletteProvider>
    </AuthGate>
  )
}
