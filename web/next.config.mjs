import { fileURLToPath } from "node:url";

// Static export: `NEXT_EXPORT=1 next build` emits a fully static directory to web/out that can be dropped into
// an nginx web root. Development (next dev) leaves it unset and keeps the /api proxy and hot reload.
const isExport = process.env.NEXT_EXPORT === "1";
// Vercel demo: the whole site uses mocks, no backend, no /api proxy needed.
const isMock = process.env.NEXT_PUBLIC_MOCK === "1";

/** @type {import('next').NextConfig} */
const nextConfig = {
  // Keep a lockfile in a parent directory from affecting root inference and asset path generation.
  turbopack: { root: fileURLToPath(new URL(".", import.meta.url)) },
  reactCompiler: true,
  // Allow dev resources (HMR) from LAN IPs; adjust as needed.
  // In dev, allow any IPv4 origin to reach /_next/* and HMR (unaffected by LAN IP changes).
  // Note: Next forbids a bare "*" for security, so use per-segment wildcards; "*.*.*.*" matches any IPv4.
  allowedDevOrigins: ["*.*.*.*"],
  compiler: {
    removeConsole: process.env.NODE_ENV === "production",
  },
  ...(isExport
    ? {
        // Fully static export: no Node runtime; images are not optimized; each route emits <route>/index.html.
        output: "export",
        images: { unoptimized: true },
        trailingSlash: true,
      }
    : isMock
      ? {
          // Vercel mock demo: no backend, no /api proxy needed.
          images: { unoptimized: true },
        }
      : {
          // Development: proxy /api/* to the Go backend (default :8787, override with AUTOPENTEST_API).
          async rewrites() {
            const backend = process.env.AUTOPENTEST_API ?? "http://localhost:8787";
            return [{ source: "/api/:path*", destination: `${backend}/api/:path*` }];
          },
        }),
};

export default nextConfig;
