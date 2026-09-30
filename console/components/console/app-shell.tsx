"use client"

import { Suspense, useEffect, useState, type ReactNode } from "react"
import { usePathname } from "next/navigation"

import { Sheet, SheetContent, SheetTitle } from "@/components/ui/sheet"
import { AuthGate } from "@/components/console/auth"
import { DemoBanner } from "@/components/console/demo-banner"
import { DetailSkeleton } from "@/components/console/loading"
import { SideNav } from "@/components/console/side-nav"
import { Logo, Topbar } from "@/components/console/topbar"
import { serviceForPath } from "@/lib/services"

function Splash() {
  return (
    <div className="flex min-h-screen items-center justify-center">
      <Logo className="text-muted-foreground animate-pulse text-lg" />
    </div>
  )
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
      <div className="flex min-h-screen flex-col">
        <DemoBanner />
        <Topbar showMenu={hasNav} onMenu={() => setMobileOpen(true)} />
        <div className="flex flex-1">
          {hasNav && service && (
            <>
              <aside className="bg-sidebar border-sidebar-border sticky top-12 hidden h-[calc(100vh-3rem)] w-60 shrink-0 border-r lg:block">
                <SideNav service={service} />
              </aside>
              <Sheet open={mobileOpen} onOpenChange={setMobileOpen}>
                <SheetContent side="left" className="bg-sidebar w-72 p-0">
                  <SheetTitle className="sr-only">{service.name} navigation</SheetTitle>
                  <SideNav service={service} onNavigate={() => setMobileOpen(false)} />
                </SheetContent>
              </Sheet>
            </>
          )}
          <main className="min-w-0 flex-1">
            <div className="mx-auto w-full max-w-[1600px] px-4 py-5 sm:px-6 lg:px-8">
              <Suspense fallback={<DetailSkeleton />}>{children}</Suspense>
            </div>
          </main>
        </div>
      </div>
    </AuthGate>
  )
}
