import { AppShell } from "@/components/console/app-shell"

export default function ConsoleLayout({ children }: { children: React.ReactNode }) {
  return <AppShell>{children}</AppShell>
}
