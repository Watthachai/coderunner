# Auto-written by FITT Code Runner — locked customer runtime.
FROM node:22-bookworm-slim AS deps
RUN apt-get update && apt-get install -y --no-install-recommends openssl ca-certificates && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci --ignore-scripts

FROM deps AS builder
COPY . .
RUN mkdir -p public prisma /runtime-config && for f in prisma.config.*; do if [ -f "$f" ]; then cp "$f" /runtime-config/; fi; done
ENV NEXT_TELEMETRY_DISABLED=1
# Build never connects to customer infrastructure; Prisma config may require a URL.
ENV DATABASE_URL=postgresql://build:build@127.0.0.1:1/build
RUN npm run db:generate --if-present
RUN npm run build
# Retain only lockfile-resolved production tools for migrations/bootstrap.
RUN npm prune --omit=dev --ignore-scripts

FROM node:22-bookworm-slim AS runner
RUN apt-get update && apt-get install -y --no-install-recommends openssl ca-certificates && rm -rf /var/lib/apt/lists/*
WORKDIR /app
ENV NODE_ENV=production
ENV NEXT_TELEMETRY_DISABLED=1
COPY --from=builder --chown=node:node /app/node_modules ./node_modules
COPY --from=builder --chown=node:node /app/.next/standalone ./
COPY --from=builder --chown=node:node /app/.next/static ./.next/static
COPY --from=builder --chown=node:node /app/public ./public
COPY --from=builder --chown=node:node /app/prisma ./prisma
COPY --from=builder --chown=node:node /app/package.json ./package.json
COPY --from=builder --chown=node:node /runtime-config/ ./
RUN printf '#!/bin/sh\nset -eu\nif [ -f prisma/schema.prisma ]; then\n  npm run db:deploy\n  npm run db:seed\nfi\nexec node server.js\n' > /app/docker-entrypoint.sh && chmod +x /app/docker-entrypoint.sh
USER node
EXPOSE 3000
ENV PORT=3000
ENV HOSTNAME=0.0.0.0
CMD ["/app/docker-entrypoint.sh"]
