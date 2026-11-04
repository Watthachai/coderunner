import { defineConfig } from "prisma/config";
// Migrations use MIGRATION_DATABASE_URL only — no fallback to DATABASE_URL.
export default defineConfig({
  schema: "prisma/schema.prisma",
  migrations: { path: "prisma/migrations" },
  datasource: { url: process.env.MIGRATION_DATABASE_URL ?? "" },
});
