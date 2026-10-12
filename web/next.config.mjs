/** @type {import('next').NextConfig} */
const nextConfig = {
  // Static export: the site is plain files, with no server-side code at all.
  output: 'export',
  reactStrictMode: true,
  trailingSlash: true,
  images: { unoptimized: true },
};

export default nextConfig;
