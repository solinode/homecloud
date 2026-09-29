/** @type {import('next').NextConfig} */
const nextConfig = {
  // The console is a static export embedded in the HomeCloud Go binary.
  output: 'export',
  trailingSlash: true,
  images: {
    unoptimized: true,
  },
  eslint: {
    ignoreDuringBuilds: true,
  },
  typescript: {
    ignoreBuildErrors: false,
  },
}

export default nextConfig
