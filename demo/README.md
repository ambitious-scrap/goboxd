# goboxd demo page

Standalone Monaco-editor demo for goboxd. **Not** bundled into the service binary
and **not** a security boundary — it's just a browser client of the public API,
equivalent to `curl`. The sandbox (nsjail/seccomp/cgroups) is the only thing that
enforces safety, and it is unchanged by this page.

## Run it

1. Start goboxd with the demo CORS origin enabled (browsers block cross-origin
   `fetch` without it; `curl` does not care):

   ```sh
   GOBOXD_DEMO_CORS_ORIGIN=http://localhost:3000 ./bin/goboxd
   ```

2. Serve this folder on that origin:

   ```sh
   cd demo && python3 -m http.server 3000
   ```

3. Open <http://localhost:3000>, pick a language, write code, click **Run ▶**.

## Notes

- `GOBOXD_DEMO_CORS_ORIGIN` (or `server.demo_cors_origin` in config) must be the
  **exact** page origin. Unset by default → no CORS header → production-safe.
- Never use `*` and never enable CORS in production.
- The language list is pulled live from `GET /info`; if the API is unreachable it
  falls back to a hardcoded list.
- CORS is a browser convenience, not security. Server-side validation and the
  sandbox are the real boundary.
