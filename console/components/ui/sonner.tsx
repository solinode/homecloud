"use client"

import { useTheme } from "next-themes"
import { AlertTriangle, CheckCircle2, Info, Loader2, XCircle } from "lucide-react"
import { Toaster as Sonner, ToasterProps } from "sonner"

/** Toaster: neutral popover surface, hairline border, semantic icon color. */
const Toaster = ({ ...props }: ToasterProps) => {
  const { resolvedTheme = "dark" } = useTheme()

  return (
    <Sonner
      theme={resolvedTheme as ToasterProps["theme"]}
      className="toaster group"
      icons={{
        success: <CheckCircle2 className="text-success size-4" />,
        error: <XCircle className="text-danger size-4" />,
        warning: <AlertTriangle className="text-warning size-4" />,
        info: <Info className="text-info size-4" />,
        loading: <Loader2 className="text-muted-foreground size-4 animate-spin" />,
      }}
      toastOptions={{
        classNames: {
          toast: "!rounded-lg !border-border !shadow-lg !font-sans !gap-2.5",
          title: "!font-medium !text-[13px]",
          description: "!text-muted-foreground !text-[13px]",
          closeButton: "!bg-popover !border-border !text-muted-foreground hover:!text-foreground",
        },
      }}
      style={
        {
          "--normal-bg": "var(--popover)",
          "--normal-text": "var(--popover-foreground)",
          "--normal-border": "var(--border-strong)",
        } as React.CSSProperties
      }
      {...props}
    />
  )
}

export { Toaster }
