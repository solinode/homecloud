import { Skeleton } from "@/components/ui/skeleton"
import { cn } from "@/lib/utils"

export function TableSkeleton({ rows = 5, cols = 5 }: { rows?: number; cols?: number }) {
  return (
    <div className="divide-y">
      {Array.from({ length: rows }, (_, r) => (
        <div key={r} className="flex items-center gap-4 px-4 py-3">
          {Array.from({ length: cols }, (_, c) => (
            <Skeleton key={c} className={cn("h-4", c === 0 ? "w-40" : "flex-1")} />
          ))}
        </div>
      ))}
    </div>
  )
}

export function DetailSkeleton() {
  return (
    <div className="flex flex-col gap-4">
      <Skeleton className="h-4 w-48" />
      <Skeleton className="h-8 w-72" />
      <div className="bg-card grid grid-cols-1 gap-6 rounded-lg border p-4 sm:grid-cols-3">
        {Array.from({ length: 9 }, (_, i) => (
          <div key={i} className="flex flex-col gap-2">
            <Skeleton className="h-3 w-24" />
            <Skeleton className="h-4 w-40" />
          </div>
        ))}
      </div>
    </div>
  )
}

export function CardsSkeleton({ count = 4 }: { count?: number }) {
  return (
    <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4">
      {Array.from({ length: count }, (_, i) => (
        <Skeleton key={i} className="h-24 rounded-lg" />
      ))}
    </div>
  )
}
