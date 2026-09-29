import type { Metadata, Viewport } from "next"
import { GeistMono } from "geist/font/mono"
import { GeistSans } from "geist/font/sans"

import { Providers } from "@/components/console/providers"
import "./globals.css"

export const metadata: Metadata = {
  title: {
    default: "HomeCloud Console",
    template: "%s | HomeCloud Console",
  },
  description: "Manage your self-hosted HomeCloud: EC2, S3, VPC, IAM, Secrets Manager, CloudWatch and CloudTrail.",
  applicationName: "HomeCloud Console",
  icons: { icon: "/favicon.svg" },
}

export const viewport: Viewport = {
  width: "device-width",
  initialScale: 1,
}

export default function RootLayout({ children }: Readonly<{ children: React.ReactNode }>) {
  return (
    <html lang="en" suppressHydrationWarning className={`${GeistSans.variable} ${GeistMono.variable}`}>
      <body>
        <Providers>{children}</Providers>
      </body>
    </html>
  )
}
