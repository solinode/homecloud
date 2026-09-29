"use client"

import type { ReactNode } from "react"
import { SWRConfig } from "swr"

import { ThemeProvider } from "@/components/theme-provider"
import { Toaster } from "@/components/ui/sonner"
import { TooltipProvider } from "@/components/ui/tooltip"
import { swrConfig } from "@/lib/hooks"

export function Providers({ children }: { children: ReactNode }) {
  return (
    <ThemeProvider attribute="class" defaultTheme="light" enableSystem={false} disableTransitionOnChange>
      <SWRConfig value={swrConfig}>
        <TooltipProvider delayDuration={300}>
          {children}
          <Toaster position="top-right" richColors closeButton />
        </TooltipProvider>
      </SWRConfig>
    </ThemeProvider>
  )
}
