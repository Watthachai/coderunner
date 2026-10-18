# One image, anywhere it runs

Required on every build and edit. The same image is installed on a customer's own server and deployed on Google Cloud Run. Cloud Run keeps nothing a container writes, may run several copies at once, may stop a copy between requests, and cuts long requests. Build every app so it works in both places without a rebuild.

1. **Files users upload** go under `process.env.UPLOAD_DIR` (the image sets `/data/uploads`) and nowhere else. A customer's server mounts a volume there; Cloud Run mounts the project's Cloud Storage bucket. Use plain `node:fs` in that directory; never write uploads under `public/`, the app directory or the database. Temporary files go under `/tmp` and may vanish between requests. Store each file's path relative to `UPLOAD_DIR` in the database, and serve files through a route that checks the same permissions as the record they belong to.
2. **Scheduled work** (reminders, expiry notices, nightly totals) is declared in `crn.schedule.json` at the project root and nowhere else: no `setInterval`, `node-cron` or self-scheduling loops.
   - Each entry is `{ "name": "<lowercase-dashes>", "path": "/api/cron/<name>", "every_minutes": 5|10|15|20|30|60 }` or the same with `"daily_at": "HH:MM"` (Bangkok time) instead of `every_minutes`. No scheduled work → omit the file or write `[]`.
   - Copy `assets/scheduler.mjs` unchanged to `lib/scheduler.mjs`. Start it once from `instrumentation.ts`:

     ```ts
     export async function register() {
       if (process.env.NEXT_RUNTIME === "nodejs") {
         const { startScheduler } = await import("./lib/scheduler.mjs");
         startScheduler();
       }
     }
     ```

   - Each job is `app/api/cron/<name>/route.ts` exporting `POST`, which answers 401 unless `cronAuthorized(request)` (from `lib/scheduler.mjs`) is true, and 204 when done. Exclude `/api/cron/` from the login check in the request proxy (the suspension switch still applies). A job must be safe to run twice and must finish within 5 minutes; record what it has done in the database rather than in memory.
   - On a customer's server the app calls the jobs itself. On Cloud Run the platform's scheduler calls them with `CRON_SECRET` and `CRN_SCHEDULER=external` stops the app's own timers. Document each job in BUILD_NOTES.
3. **State shared across requests** — sessions, rate limits, carts, queues, idempotency keys — lives in the database. Module-level variables are only caches that may be empty on the next request.
4. **No request runs longer than 5 minutes.** Split long imports, exports and batch updates into steps recorded in the database and continue them from a scheduled job.

The release checker runs the customer image with a read-only root filesystem, writable `/tmp` and `/data/uploads` only, and no Linux capabilities, and validates `crn.schedule.json`. A write anywhere else, or an invalid schedule, fails the release.
