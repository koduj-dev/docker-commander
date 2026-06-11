const http = require("http");
const port = process.env.PORT || 3000;
http.createServer((_, res) => res.end("Node + Postgres + Redis 👋\n")).listen(port);
