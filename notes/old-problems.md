**Waitress = Python production WSGI server.**

Basically:

```txt
Browser → Nginx → Waitress → Python Flask/FastAPI-ish app
```

It receives HTTP requests and hands them to your Python backend. The warning:

```txt
WARNING:waitress.queue:Task queue depth is 20
```

means: **all available backend workers are busy, and requests are piling up in line.**

---

## Problem list / findings

### 1. Backend worker starvation

Your backend threads are getting occupied by slow/heavy requests. Then even tiny endpoints timeout.

**Evidence:** lots of `waitress.queue depth` warnings + many unrelated endpoints turning into `504`.

**Solution in Go:** never let heavy work block normal API handlers. Use goroutines/job queue/background workers for crawls.

---

### 2. Huge JSON responses

Some endpoints return massive payloads, like `/api/crawls/149` at **13.4 MB** and project URL lists over **1 MB**. That is brutal for API UX.

**Solution:** paginate everything.

```txt
GET /projects/:id/urls?page=1&limit=100
GET /crawls/:id/results?limit=100&cursor=...
```

Never return full crawl data by default.

---

### 3. Frontend request storm

The frontend appears to fetch URLs for many projects at once on app load.

**Problem:**

```txt
/projects/28/urls
/projects/20/urls
/projects/6/urls
/projects/39/urls
...
```

all within seconds.

**Solution:** fetch only visible/current project data. Lazy-load project URLs when the user opens that project.

---

### 4. Status polling causing load

`/api/crawl_status` keeps timing out. If status checks hit DB heavily, polling can become self-DDoS.

**Solution:** make status cheap.

Store latest crawl status in Redis/memory/table row. Status endpoint should be a tiny lookup, not a full crawl computation.

---

### 5. Heavy crawl start is synchronous

`POST /api/start_crawl` got a 504. That probably means the request starts doing crawl setup/work directly.

**Solution:** API should enqueue and return immediately.

```txt
POST /start_crawl → creates job → returns { job_id }
worker → runs crawl in background
GET /crawl_status/:job_id → cheap status
```

---

### 6. No response size discipline

Nginx buffering warnings mean responses are large enough that Nginx writes temp files.

**Solution:** set API payload budgets.

Example:

```txt
Normal API response: < 100 KB
Large list response: paginated
Exports/reports: separate downloadable file
```

---

### 7. Missing/weak DB indexes possible

If `project_id`, `crawl_id`, `user_id`, `created_at` queries are not indexed, load gets worse over time as data grows.

**Solution:** in Go migration, add indexes early.

```sql
CREATE INDEX idx_urls_project_id ON urls(project_id);
CREATE INDEX idx_crawls_project_id_created_at ON crawls(project_id, created_at DESC);
CREATE INDEX idx_results_crawl_id ON crawl_results(crawl_id);
```

---

### 8. No backpressure / concurrency limits

If many crawls or heavy reads can run at once, the server eats itself.

**Solution:** add limits.

```txt
max active crawls per user/project
max DB connections
max worker concurrency
rate limits on expensive endpoints
```

---

### 9. API design mixes dashboard data with bulk data

The dashboard probably needs summaries, but the API is returning full URL/result data.

**Solution:** split endpoints.

```txt
GET /projects/sidebar       → tiny summary
GET /projects/:id/overview  → tiny stats
GET /projects/:id/urls      → paginated table
GET /crawls/:id/export      → full export only when asked
```

---

## Core lesson for the Go rewrite

Do **not** just rewrite the same API in Go.

The main issue is architecture:

```txt
large responses
+ blocking handlers
+ frontend request storm
+ no pagination
+ crawl work inside web request path
= slow death over time
```

Go will survive longer than Python, but if you repeat this shape, it’ll still fail.
