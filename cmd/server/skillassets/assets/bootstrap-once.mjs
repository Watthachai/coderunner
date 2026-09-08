// Adapt the two transaction callbacks to the application's real account model.
// SystemBootstrap must be created by a committed Prisma migration.
export async function bootstrapOnce(prisma, { hasUsers, createAdmin }, env = process.env) {
  return prisma.$transaction(async tx => {
    await tx.$queryRaw`SELECT 1 AS locked FROM pg_advisory_xact_lock(741924187)`;
    const id = 'initial-administrator-v1';
    if (await tx.systemBootstrap.findUnique({ where: { id } })) return 'already-complete';
    if (await hasUsers(tx)) {
      await tx.systemBootstrap.create({ data: { id, completedAt: new Date() } });
      return 'adopted-existing-users';
    }
    const email = (env.BOOTSTRAP_ADMIN_EMAIL ?? '').trim().toLowerCase();
    const password = env.BOOTSTRAP_ADMIN_PASSWORD ?? '';
    if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email) || password.length < 16) {
      throw new Error('First installation requires a valid bootstrap email and a unique password of at least 16 characters');
    }
    await createAdmin(tx, { email, password });
    await tx.systemBootstrap.create({ data: { id, completedAt: new Date() } });
    return 'created';
  }, { maxWait: 30_000, timeout: 60_000 });
}
