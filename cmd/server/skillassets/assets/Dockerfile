# Multi-stage build for a Next.js (App Router) app using output: "standalone".
# Produces a small runtime image that runs .next/standalone/server.js as a
# non-root user. Requires `output: "standalone"` in next.config.ts.

# 1. Install dependencies only when needed.
FROM node:20-alpine AS deps
# libc6-compat helps some native deps (e.g. Prisma engines) on Alpine.
RUN apk add --no-cache libc6-compat
WORKDIR /app

# Install with whichever lockfile is present. --ignore-scripts skips lifecycle
# scripts (e.g. a `postinstall: prisma generate`) at this stage: the Prisma
# schema isn't copied yet, so postinstall would fail. The client is generated
# explicitly in the builder stage below, after the full source is copied.
COPY package.json package-lock.json* ./
RUN npm ci --ignore-scripts

# 2. Build the app (also generates the Prisma client).
FROM node:20-alpine AS builder
WORKDIR /app
COPY --from=deps /app/node_modules ./node_modules
COPY . .

# Generate Prisma Client before building. `prisma generate` needs no database.
# (Safe to keep even if the project has no Prisma — remove this line if so.)
RUN npx prisma generate

# next build must NOT require a live DB (keep DB-reading routes force-dynamic).
ENV NEXT_TELEMETRY_DISABLED=1
RUN npm run build

# 3. Production image — copy only the standalone output.
FROM node:20-alpine AS runner
WORKDIR /app

ENV NODE_ENV=production
ENV NEXT_TELEMETRY_DISABLED=1

# Run as a non-root user.
RUN addgroup --system --gid 1001 nodejs \
  && adduser --system --uid 1001 nextjs

# Public assets and the standalone server + traced node_modules.
COPY --from=builder /app/public ./public
COPY --from=builder --chown=nextjs:nodejs /app/.next/standalone ./
COPY --from=builder --chown=nextjs:nodejs /app/.next/static ./.next/static

# If the app uses Prisma at runtime, the generated client + engines are already
# traced into ./node_modules by standalone output. The image CRN actually produces is
# SELF-MIGRATING: its entrypoint runs `prisma db push` (data-safe) against the external
# DATABASE_URL, optionally `prisma db seed` when DEMO_SEED=1, then `node server.js`.
# (This reference keeps the plain `node server.js` CMD for illustration.)

USER nextjs
EXPOSE 3000
ENV PORT=3000
ENV HOSTNAME=0.0.0.0

# server.js is emitted by Next.js standalone output.
CMD ["node", "server.js"]
