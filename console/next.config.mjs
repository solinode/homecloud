/** @type {import('next').NextConfig} */
// Static demo build (see `npm run build:demo`): served from /demo/ on the landing page
// with an in-browser mock backend instead of the HomeCloud API.
const demo = process.env.NEXT_PUBLIC_DEMO === '1'
const basePath = demo ? '/demo' : ''

const nextConfig = {
  ...(demo ? { basePath, assetPrefix: basePath, distDir: '.next-demo' } : {}),
  // Always define the flags so they are inlined (and dead branches are dropped) in every build.
  env: { NEXT_PUBLIC_BASE_PATH: basePath, NEXT_PUBLIC_DEMO: demo ? '1' : '' },
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
