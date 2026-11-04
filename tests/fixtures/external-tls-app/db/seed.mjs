import { main, runtimePool } from './target.mjs';
await main(async () => {
  const pool = runtimePool();
  try {
    const client = await pool.connect();
    try {
      await client.query('begin');
      const done = await client.query(`select 1 from "SystemBootstrap" where id = 'initial-administrator-v1' for update`);
      if (done.rowCount) { await client.query('commit'); console.log('First-install bootstrap: already-complete'); return; }
      const email = process.env.BOOTSTRAP_ADMIN_EMAIL;
      if (!email || !process.env.BOOTSTRAP_ADMIN_PASSWORD) throw new Error('BOOTSTRAP_ADMIN_EMAIL and BOOTSTRAP_ADMIN_PASSWORD are needed for the first start');
      await client.query('insert into "Owner"(email) values ($1)', [email]);
      await client.query(`insert into "SystemBootstrap"(id, "completedAt") values ('initial-administrator-v1', now())`);
      await client.query('commit');
      console.log('First-install bootstrap: created');
    } catch (e) { await client.query('rollback').catch(() => {}); throw e; } finally { client.release(); }
  } finally { await pool.end(); }
});
