# Architecture

[← Manual index](README.md)

```
React + TypeScript SPA  ──REST──▶  Go backend  ──Docker Engine API──▶  dockerd
   (Tailwind, Recharts)  ◀─WebSocket (live stats + logs)─┘
```

The Go server embeds the built SPA (`go:embed`) and serves everything from one
origin, so the production artifact is a single executable.

| Layer    | Technology |
|----------|------------|
| Backend  | Go, [chi](https://github.com/go-chi/chi), [coder/websocket](https://github.com/coder/websocket), official Docker SDK |
| Storage  | SQLite via [modernc.org/sqlite](https://modernc.org/sqlite) (pure Go, no CGO); metric history in Redis or memory |
| Auth     | Argon2id, TOTP ([pquerna/otp](https://github.com/pquerna/otp)), JWT, optional LDAP |
| Frontend | React, TypeScript, Vite, Tailwind CSS, Recharts, React Flow, xterm.js |
