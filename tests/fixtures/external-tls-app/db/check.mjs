import { main, runtimePool } from './target.mjs';
await main(async () => {
  const pool = runtimePool();
  try {
    const { rows } = await pool.query('select current_user, current_schema()');
    console.log(JSON.stringify({ current_user: rows[0].current_user, current_schema: rows[0].current_schema }));
  } finally { await pool.end(); }
});
