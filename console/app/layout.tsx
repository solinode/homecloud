import type { Metadata, Viewport } from "next"
import localFont from "next/font/local"

import { Providers } from "@/components/console/providers"
import "./globals.css"

// Self-hosted Geist and Geist Mono: the same variable fonts the landing page serves.
const geist = localFont({ src: "./fonts/geist.woff2", weight: "100 900", variable: "--font-geist", display: "swap" })
const geistMono = localFont({ src: "./fonts/geist-mono.woff2", weight: "100 900", variable: "--font-geist-mono", display: "swap" })

export const metadata: Metadata = {
  title: {
    default: "HomeCloud Console",
    template: "%s | HomeCloud Console",
  },
  description: "Manage your self-hosted HomeCloud: EC2, S3, VPC, IAM, Secrets Manager, CloudWatch and CloudTrail.",
  applicationName: "HomeCloud Console",
  icons: { icon: `${process.env.NEXT_PUBLIC_BASE_PATH ?? ""}/favicon.svg` },
}

export const viewport: Viewport = {
  width: "device-width",
  initialScale: 1,
  themeColor: [
    { media: "(prefers-color-scheme: dark)", color: "#09090b" },
    { media: "(prefers-color-scheme: light)", color: "#fafaf9" },
  ],
}

export default function RootLayout({ children }: Readonly<{ children: React.ReactNode }>) {
  return (
    <html lang="en" suppressHydrationWarning className={`${geist.variable} ${geistMono.variable}`}>
      <body>
        <Providers>{children}</Providers>
      </body>
    </html>
  )
}
