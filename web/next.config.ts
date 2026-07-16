import type { NextConfig } from "next";
import { PHASE_DEVELOPMENT_SERVER } from "next/constants";

const createNextConfig = (phase: string): NextConfig => ({
  reactCompiler: true,
  output: "export",
  // Keep Turbopack inside this application. The development host contains an
  // unrelated parent package-lock.json, which otherwise makes Next scan the
  // entire tool workspace and can stall production builds.
  turbopack: { root: process.cwd() },
  ...(phase === PHASE_DEVELOPMENT_SERVER ? {} : { assetPrefix: "./" }),
});

export default createNextConfig;
