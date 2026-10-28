# api-recon — Reference manual

Grep recipes, the `config.json` template, and troubleshooting. All greps run against the `js/` directory. If a bundle is a single line you can run `js-beautify` or `sed 's/}/}\n/g'` first, but a raw grep with a context window is usually enough.

## Scripts

Every file in `scripts/` is a **reference template** and must be adapted to the target site before running. Typical change points:

| Script | Common adjustments |
|---|---|
| `harvest_static.py` | Endpoint regexes, webpack/Vite manifest parsing, micro-frontend publicPath, retry/concurrency |
| `runtime_harvest.js` | neutralize field names and success values, stub matching rules and body structure, route sources, WS recording, `waitUntil`/`routeTimeout`/`proxy` |
| `preload.js` | `loginPathRe`, L1 stubs, `neutralize.fields`, `apiPattern`, whether L3 is enabled, `recordDetail`, `observe.*`, `neutralizeVueRouter` |
| `spider_mpa.py` | `--exclude` for destructive links, cookie, depth/max, same-origin filtering |
| `extract_route_map.py` | `routeMap` / `routeLink` regexes, KEY naming patterns |
| `build_perm_tree.py` | `userRouteAuth` parsing, `ROOTS`/`PREFIX_PARENT` hierarchy heuristics, stub outer field names |
| `config.json` | Single entry point for all of the site-specific parameters above |

Put the adapted files in the task working directory (e.g. `recon/`) and state the specific changes relative to the reference scripts in the report.

---

## A. Reversing the three gates

### A1. Render gate — "How does it decide you are logged in?"

```bash
grep -rhoaE '.{0,40}(isLogin|isAuthenticated|loggedIn|hasLogin|requireAuth)\b.{0,80}' js | head
grep -rhoaE 'function (getUser|getToken|getAuth)[0-9]?\([^)]*\)\{.{0,200}' js | head
grep -rhoaE '(localStorage|sessionStorage)\.getItem\("[^"]+"\)' js | sort -u
grep -rhoaE '(Cookies?|cookie)\.(get|load)\("[^"]+"\)' js | sort -u
grep -rhoaE '\batob\(|JSON\.parse\(|jwt|decode' js | head
```

Find the chain `isLogin = f(getUser())` → `getUser = decode(storage.read(KEY))` and determine the **storage key**, the **container** (Cookie vs localStorage), and the **encoding**:

| Encoding | How to forge it in config |
|---|---|
| Plain string / `"1"` / token | `"value": "anything-truthy"` |
| `JSON.parse(x)` | `"value": "json:{\"id\":1,\"username\":\"admin\"}"` |
| `JSON.parse(atob(x))` | `"value": "b64json:{\"id\":1,\"username\":\"admin\"}"` |
| JWT | Unsigned/`alg:none` JWT, or sign with a key found in the bundle |
| Encrypted (SM2/AES/RSA) | Look for hard-coded keys; forge if the render gate only needs a decodable blob; otherwise fall back to static |

→ Write into `cookies` / `localStorage`.

### A2. Interceptor gate — "What triggers the jump to /login?"

```bash
grep -rhoaE '.{0,60}(interceptors\.response|axios|request\.use).{0,120}' js | head
grep -rhoaE '.{0,40}(response_code|errcode|errno|\bcode\b|\bret\b|\bstatus\b)\s*[=!]==?\s*[\-0-9]{1,4}.{0,60}' js | head -20
grep -rhoaE '.{0,40}(未登录|请重新登录|登录已过期|unauthorized|登录失效|授权|token.{0,10}invalid).{0,40}' js | head
grep -rhoaE '.{0,30}(location\.href|router\.(push|replace)|navigate)\([^)]*login[^)]*\)' js | head
```

Determine the **field name**, the **success value** (usually `0` or `200`), and the **failure values that trigger the redirect**. Verify with a junk session:

```bash
curl -sk -X POST -H 'Cookie: <fakekey>=junk' https://target/api/<protected> -d '{}' -H 'Content-Type: application/json'
```

→ Write into `neutralize.fields` + `neutralize.success`.

### A3. Content gate — "Where do menus/permissions come from?"

```bash
grep -rhoaE '"/api[^"]*(permission|perm|role|menu|acl|resource|nav)[^"]*"' js | sort -u
grep -rhoaE '.{0,30}(menus|permissions|menuList|routeList|authList|role_permissions)\b.{0,120}' js | head
grep -rhoaE 'userRouteAuth|getResultTree|routeMap|routeLink|hasPermission|checkAuth' js | head
grep -rhoaE '([A-Z_][A-Z0-9_]*):\{name:"[^"]*",link:"/[^"]+"\}' js | head
```

**Two data layers** (common in enterprise admin consoles):

| API | Typical payload | Consumer |
|---|---|---|
| `.../role_permissions` | `{ permissions: string[], role_type }` | Route guards, button-level ACL |
| `.../permissions/all` | `tree[{ code, position, children }]` | Sidebar menu rendering |
| `userRouteAuth` in the bundle | `{ CODE: { url, name? } }` | code → frontend path |
| `routeMap` in the bundle | `{ KEY: { name, link } }` | Alias resolution (webpack `o.DASHBOARD`) |

Read the consumer code to confirm how `getResultTree(tree, permissions)` filters and which field `v-if` / `hasAuth(code)` checks.

**Manual forge** (small sites): build a permissive payload → `stubs`.

**Full permission tree reconstruction** (large sites where the sidebar/submodules are still blank): see **section I**.

---

## B. config.json template

```json
{
  "baseUrl": "https://target/",
  "runtimeMode": "both",
  "chromium": "/usr/bin/chromium",

  "cookies": [
    { "name": "auth", "value": "b64json:{\"id\":1,\"username\":\"admin\",\"role\":\"admin\",\"func\":{},\"permissions\":[\"*\"]}" }
  ],
  "localStorage": { "token": "faketoken", "isLogin": "1" },

  "neutralize": {
    "fields": ["response_code", "code", "errno", "ret", "status"],
    "success": 0,
    "flags": { "success": true, "message": "ok" }
  },
  "forward": true,
  "loginUrlPattern": "/login",
  "apiPattern": "/api/|/rest/|/graphql",

  "mockTier": "L1+L2",
  "recordDetail": true,
  "observe": {
    "storageReads": false,
    "cookieReads": false,
    "xhrHeaders": true
  },
  "neutralizeVueRouter": true,
  "stubs": [
    {
      "match": "permissions/all|/menu|role_permissions",
      "body": {
        "response_code": 0, "code": 0,
        "data": {
          "permissions": ["*"],
          "menus": [
            { "name": "dashboard", "path": "/dashboard", "show": true, "children": [] },
            { "name": "alert", "path": "/alert", "show": true, "children": [] }
          ]
        }
      }
    }
  ],

  "explore": {
    "clickTabs": true,
    "clickTables": true,
    "pushStateFallback": true,
    "maxMenuItems": 50
  },

  "routes": ["/dashboard", "/alert", "/asset", "/device", "/report", "/config", "/system"],
  "waitMs": 1500, "perRouteMs": 900, "headless": true,
  "waitUntil": "domcontentloaded",
  "routeTimeout": 12000,
  "proxy": "",

  "captureResponses": true, "recordWs": true, "respMax": 600
}
```

Fields:
- `runtimeMode`: `depth` (Puppeteer), `coverage` (browser MCP), `both`
- `cookies[].value` prefixes: `b64json:` → base64(JSON); `json:` → raw JSON; no prefix → literal
- `forward: true` forwards real requests and rewrites the code fields; `false` is fully offline stubbing
- `mockTier`: layers the coverage-mode preload enables, e.g. `L1+L2`, `L1+L2+L3`
- `routes` comes from `routes.txt`; after forging menus the harness appends `<a href>` targets automatically
- `captureResponses` / `recordWs` only apply in depth mode
- `waitUntil`: use `domcontentloaded` for large SPAs to avoid `networkidle2` hangs
- `routeTimeout`: per-route `page.goto` timeout (milliseconds)
- `proxy`: Puppeteer `--proxy-server`; `HTTP_PROXY` / `HTTPS_PROXY` also work

### B1. Dual stub template (role_permissions + permissions/all)

```json
"stubs": [
  {
    "match": "role_permissions",
    "body": {
      "response_code": 0,
      "data": {
        "permissions": ["MONITOR", "MONITOR_ALERT", "THREAT", "ASSETS_RISK"],
        "role_type": "SUPER_ADMIN"
      }
    }
  },
  {
    "match": "permissions/all",
    "body": {
      "response_code": 0,
      "data": [
        {
          "code": "MONITOR",
          "position": 1,
          "children": [
            { "code": "MONITOR_ALERT", "position": 1, "children": [] }
          ]
        }
      ]
    }
  }
]
```

The outer field names (`response_code` / `code` / `data`) must match the A2 interceptor gate; `permissions` must cover every leaf code in the tree.

---

## C. coverage mode: preload configuration

Edit the `CONFIG` object at the top of `scripts/preload.js`, or replace it before injecting through CDP:

```javascript
const CONFIG = {
  loginPathRe: /\/(login|signin)(\/|$|\?)/i,
  mockTier: 'L1+L2',
  forward: true,
  recordDetail: true,
  extractUrlsFromResponse: true,
  neutralizeVueRouter: true,
  observe: { storageReads: false, cookieReads: false, xhrHeaders: true },
  neutralize: { fields: ['response_code', 'code'], success: 0 },
  stubs: [ /* same as config.json stubs */ ],
  apiPattern: /\/(api|apis|v\d+|dev|internal|graphql)\//i,
};
```

Verify: `window.__API_RECON_PRELOAD__ === true` and the pathname stays stable.

Export the recording:

```javascript
JSON.stringify({
  apis: [...window.__API_RECON_LOG__],
  detail: window.__API_RECON_DETAIL__,
  routes: [...(window.__API_RECON_ROUTES__ || [])],
  observe: window.__API_RECON_OBSERVE__,
}, null, 2)
```

---

## D. preload / runtime hook capabilities

Browser hook capabilities built into preload (coverage) and runtime_harvest (depth), and their coverage:

| Hook capability | Value for API discovery | Coverage |
|---|---|---|
| Hook fetch / XHR.open | Record request URL/method | ✅ `recordDetail` + `__API_RECON_LOG__` |
| Hook XHR.setRequestHeader | Discover Authorization and other headers | ✅ `observe.xhrHeaders` |
| Hook localStorage/cookie reads | Confirm session key names | ⚠️ optional `observe.storageReads/cookieReads` |
| Vue route extraction | Fill in frontendRoutes | ✅ `__API_RECON_ROUTES__` (loaded routes) |
| Vue route guard neutralization / login redirect blocking | Open modules so they fire APIs | ✅ `neutralizeVueRouter` + native redirect neutralization |
| React route extraction | Fill in routes | ⚠️ static + clicks; no dedicated hook |
| Navigation blocking (login path) | Stay on the page for analysis | ⚠️ only blocks the login path, so business navigation is not blocked |
| Hook crypto libraries (CryptoJS/SM, etc.) | Encrypted parameters → plaintext API body | ❌ hook the encryption function's arguments by hand; write conclusions into config |
| Anti-debugging bypass | Otherwise runtime records no APIs | ❌ handle by hand; static still works |

---

## E. Endpoint extraction regexes (when static yields too few)

Loosen `extract_endpoints` in `harvest_static.py`, or by hand:

```bash
grep -rhoaE '"/[a-z][A-Za-z0-9_/\-]{3,}"' js | sort -u
grep -rhoaE '/api/[a-zA-Z0-9_./-]+' js | sort -u
```

---

## F. Troubleshooting

| Symptom | Cause → Fix |
|---|---|
| Very few static APIs | Endpoint dialect mismatch → loosen the regexes (section D) |
| Chunk count ≪ manifest | CSS-only or undeployed chunks; 404s already retried |
| Runtime still shows the login page | Render gate wrong → recheck A1: key name, container, encoding, domain |
| In the shell but modules are blank | Content gate → forge menus (A3); `routes` paths may be wrong |
| Every route only has bootstrap/locale | Incomplete permission codes → section I permission tree reconstruction; check the `role_permissions` + `permissions/all` dual stub |
| Sidebar has items but subpages are blank | Tree is missing intermediate nodes, or codes do not match `userRouteAuth` |
| Every API redirects to login | Interceptor gate → confirm `neutralize`; nested fields need an extended walk |
| Zero WS frames | Subscribes only after user interaction; increase `perRouteMs` |
| Empty response bodies | Real responses only exist with `forward: true` |
| Chromium missing | Install chromium or set `config.chromium` / `CHROMIUM` |
| Lots of mocks but still back to login | Hook too late or missing the `location.href` setter → document-start + preload |
| All lists empty | Empty L3 arrays are normal; keep clicking tabs/settings/details |
| Redux actions mistaken for routes | Filter internal paths containing get/set/change/clear/toggle/upload |
| Vue still redirects to login | preload not at document-start → change the injection timing; or clear guards by hand with `neutralizeVueRouter: false` |
| URLs in responses not in the log | Enable `extractUrlsFromResponse`; or extract them by hand from `__API_RECON_DETAIL__` |
| Unknown Authorization header name | Enable `observe.xhrHeaders` or inspect request headers in DevTools |
| Runtime very slow / timing out | Use `waitUntil: domcontentloaded`; lower `routeTimeout`; avoid `networkidle2` |
| Proxy connection fails | Check `proxy` / environment variables; Puppeteer and curl must use the same proxy port |

---

## G. Hardened targets

When the server validates the session step by step (signed cookies that cannot be forged, server-rendered menus that cannot be stubbed), runtime will get stuck at the shell. Expected behavior:

- **Static is enough for endpoint enumeration** — module paths are in the code
- If the authorization allows it, run the same harness with a **real session**: `forward: true`, no neutralize needed, capturing real methods/params/responses

---

## H. Single-run checklist

1. Confirm the authorized scope
2. **Read** `scripts/harvest_static.py` → adapt to the target → run → review `api_static.txt`, `routes.txt`
3. **Phase 1b**: path anchor window expansion + binding layer → `param_candidates.json` (section J)
4. Reverse A1/A2/A3 → write a site-specific `config.json`
5. **Read and adapt** `runtime_harvest.js` / `preload.js` before running them
6. `runtimeMode=depth`: `npm install` → run the adapted harvest script
7. `runtimeMode=coverage/both`: inject the adapted preload at document-start → browser MCP dynamic enumeration + **parameter trigger matrix**
8. Modules do not render → **section I permission tree reconstruction** → patch stubs → rerun
9. Multi-sample parameter diff + error inference → `params_merged.json`
10. Merge → `site_map.json` + `api_merged.txt`, honestly marking coverage, gaps, and script changes

---

## I. Permission tree reconstruction (Phase 4 deep dive)

Use when forging a simple `menus: [{ path, show: true }]` does not work and submodules still do not mount.

### I1. Locate the auth module

```bash
grep -l 'userRouteAuth' js/*.js
grep -l 'routeMap\|routeLink' js/*.js
grep -rhoaE 'getResultTree|role_permissions|permissions/all' js | head
```

Record: the **permission API path**, the **response field names**, and the **consuming chunk file name**.

### I2. Extract routeMap

```bash
python3 scripts/extract_route_map.py recon/js recon/
# produces recon/route_map.json
```

If you get `[!] no routeMap pattern found`: loosen the regex in `extract_route_map.py`, or grep by hand:

```bash
grep -rhoaE '([A-Z_][A-Z0-9_]*):\{name:"[^"]*",link:"/[^"]+"\}' js | head -20
```

### I3. Build the permission tree + stubs

```bash
python3 scripts/build_perm_tree.py recon/js recon/ --config recon/config.json
```

Script logic:
1. Parse `userRouteAuth={MONITOR:{url:...},...}` (including webpack aliases `He=o.DASHBOARD`)
2. Resolve aliases → real paths using `route_map.json`
3. Infer parents from code prefixes (`MONITOR_ALERT` → `MONITOR`)
4. Write `permissions_tree.json`, `permissions_all_stub.json`, `role_permissions_stub.json`
5. With `--config`, automatically write `stubs` and extended `routes` into `config.json`

**Adapt to the target** (at the top of the script):
- `DEFAULT_ROOTS`: list of top-level module codes
- `DEFAULT_PREFIX_PARENT`: `PREFIX_` → parent mapping
- `DEFAULT_EXTRA_PARENT`: orphan nodes whose relationship is not prefix-based

### I4. Check stub consistency

```bash
# the number of permissions should be ≈ the number of userRouteAuth entries
wc -l recon/perm_codes_all.txt
# routes should cover every link in route_map
python3 -c "import json; m=json.load(open('recon/route_map.json')); r=set(json.load(open('recon/config.json'))['routes']); print('missing', [v['link'] for v in m.values() if v['link'] not in r])"
```

### I5. Rerun runtime and compare

```bash
node recon/runtime_harvest.js recon/config.json
# compare runtime_api.json entry counts before and after forging; check whether /attack, /asset, etc. now show module APIs
```

| Before forging | After forging (success) |
|---|---|
| The same 3–5 bootstrap entries on every route | Different routes fire different module APIs |
| Only `/api/locale/language` | `/api/web/...` module endpoints appear |
| Single-digit routes in `routes.txt` | 80–110+ `routes` from route_map |

### I6. If it still fails

- **coverage mode**: click the sidebar + tabs; permission gating may only request after interaction
- **stub fields**: compare the nesting of the real API (curl + real session) with the stub
- **Extra guards**: grep `hasPermission|checkRole|func.` and other button-level checks, and extend `role_permissions.permissions`
- **Static fallback**: module API paths are still in `api_static.txt`; runtime only fills in METHOD/body; keep parameters in `param_candidates.json` + recorded samples

---

## J. Parameter reverse engineering (Phase 1b / 5b / 5c)

**A methodology, not a generic script.** Find paths with regexes; find parameters with anchor window expansion + UI binding chain + multi-sample diff + error inference.

### J1. Anchor window expansion — from path to request-building object

```bash
# use paths known from Phase 1 as anchors
grep -n '"/api/user/list"' js/*.js
grep -rhoaE '.{0,120}("/api[^"]+").{0,200}' js | head
grep -rhoaE '(params|data|body|payload)\s*:\s*\{' js | head
grep -rhoaE '(get|post|put|delete|patch)\([^,]+,\s*\{' js | head
```

### J2. Wrapper layer and transport shape

```bash
# axios / shared request helper
grep -rhoaE '(axios|request)\.(get|post|put|delete|patch)\(' js | head
grep -rhoaE 'interceptors\.(request|response)' js | head

# GraphQL
grep -rhoaE '(query|mutation)\s+\w+|gql`|graphql\(' js | head
grep -rhoaE '\$[a-zA-Z_]+\s*:\s*(Int|String|Boolean|\[)' js | head

# FormData / multipart
grep -rhoaE 'FormData|\.append\(' js | head

# path parameters
grep -rhoaE 'path:\s*"/[^"]*:[^"]+"' js | head
grep -rhoaE 'useParams|route\.params|\$route\.params' js | head
```

### J3. Validation gate — required / format / enum

```bash
grep -rhoaE '(required|message|pattern|enum|validator)\s*:' js | head
grep -rhoaE 'yup\.|zod\.|async-validator|Form\.Item|a-form-item|el-form-item' js | head
grep -rhoaE 'rules\s*:\s*\[|name:\s*["\'][a-zA-Z_]+["\']' js | head
grep -rhoaE 'label.*value|options\s*:\s*\[' js | head
```

### J4. Binding layer — form → API

```bash
grep -rhoaE 'onFinish|handleSubmit|getFieldsValue|validateFields' js | head
grep -rhoaE '(pick|omit|transform|dayjs|moment)\(' js | head
```

Runtime fallback: DevTools → Network → request → **Initiator** (call stack), tracing from `fetch`/`send` up to the request-building function.

### J5. Encrypted parameters

```bash
grep -rhoaE 'encrypt|decrypt|sign|CryptoJS|sm2|sm3|sm4|RSA|AES' js | head
```

**Do not guess fields from ciphertext** — hook the encryption function's **arguments** and record the plaintext payload before encryption; write conclusions into `config.json` / `param_candidates.json`.

### J6. Parameter trigger matrix (mandatory in Phase 3)

Record once per action for each module and diff the request body/query:

| Action | Focus |
|---|---|
| List first screen | Pagination defaults |
| Search | keyword, filters |
| Advanced filter | Optional fields |
| Create/edit | Full entity |
| Bulk/export | `ids[]`, `exportType` |
| Sort/paginate | `sortField`, `order` |

Produces `param_samples.json`: `[{ "path", "method", "action": "search", "body", "query", "headers" }]`

### J7. Confidence rules

| Confidence | Condition |
|---|---|
| **High** | Static callsite + ≥2 consistent runtime samples |
| **Medium** | Static only, or a single runtime sample |
| **Low** | Inferred from responses/errors, not verified a second time |
| **Pending trigger** | Field known statically, but the UI/permissions never reached it |

### J8. Quick setups by scenario

| Scenario | Order |
|---|---|
| REST list page | J1 request-building object → J6 four-way diff → J3 rules |
| Create/edit form | J3 Form names → J4 submit chain → runtime submit + deliberately leave fields empty to see the 400 |
| GraphQL | J2 variable declarations → record variables for each operation at runtime |
| Encrypted body | J5 hook arguments → the pre-encryption fields are the real params |

### J9. Mapping to api-recon phases

| api-recon | Parameter recon |
|---|---|
| Phase 1 static | J1 anchor window expansion |
| Phase 2 A2 interceptor | Globally injected fields (tenantId, sign) |
| Phase 3 runtime | J6 trigger matrix + `param_samples.json` |
| Phase 4 permission tree | Forms differ per module → all fields fire only with enough permissions |
| Phase 5 merge | `params_merged.json` + confidence; never decide required from a single sample |

### J10. Troubleshooting

| Symptom | Fix |
|---|---|
| Field name known statically but never seen at runtime | Mark it "pending trigger"; fill in the permission tree / click advanced filters / try every option of linked selects |
| Same path with different body shapes | Normal — record separately per `action`, do not force a merged schema |
| Stub responses are fake but you want the params | **Look at the outbound request** body/headers; do not infer from stub responses |
| 400 reports a nested field | Watch for outer wrappers `data`/`bizData`/`variables` |
| GraphQL shows only the operation name | Expand the `variables` JSON; statically look for `$var: Type` |

---
