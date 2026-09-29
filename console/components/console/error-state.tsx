import { AlertCircle, RefreshCw } from "lucide-react"

import { Button } from "@/components/ui/button"
import { errorMessage } from "@/lib/api"
import { cn } from "@/lib/utils"

/** ErrorState shows a failed request with a retry button. */
export function ErrorState({ error, onRetry, className }: { error: unknown; onRetry?: () => void; className?: string }) {
  return (
    <div
      className={cn(
        "border-destructive/30 bg-destructive/5 text-destructive flex flex-col gap-3 rounded-lg border p-4 text-sm sm:flex-row sm:items-center sm:justify-between",
        className,
      )}
    >
      <div className="flex items-start gap-2">
        <AlertCircle className="mt-0.5 size-4 shrink-0" />
        <span className="break-words">{errorMessage(error)}</span>
      </div>
      {onRetry && (
        <Button variant="outline" size="sm" onClick={onRetry} className="text-foreground shrink-0">
          <RefreshCw /> Retry
        </Button>
      )}
    </div>
  )
}
